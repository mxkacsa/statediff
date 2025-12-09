// Package statediff provides deterministic state synchronization with automatic
// JSON diff generation, reversible effects, and player-specific projections.
//
// Key design principles:
//   - Deterministic: same inputs always produce same outputs
//   - Minimal memory: only stores one previous state, not per-session
//   - Projection-based: each viewer sees a filtered view of the state
//   - Effect system: reversible transformations that don't mutate base state
package statediff

import (
	"encoding/json"
	"fmt"
	"sync"
)

// State manages game state with effects and projections.
// Thread-safe for concurrent access.
type State[T any] struct {
	mu sync.RWMutex

	current  T    // Current base state
	previous T    // Previous state (with effects) for diff calculation
	hasPrevi bool // Whether previous is valid
	effects  []Effect[T]
	cloner   func(T) T
	arrayCfg ArrayConfig
}

// Config for State initialization
type Config[T any] struct {
	// Cloner for deep copies. If nil, uses JSON marshal/unmarshal.
	// Implementing a manual cloner is ~40x faster.
	Cloner func(T) T

	// ArrayStrategy configures how array diffs are calculated
	ArrayStrategy ArrayStrategy
	// ArrayKeyField is the field name used as ID when ArrayStrategy is ByKey
	ArrayKeyField string
}

// New creates a new State with the given initial value.
// Returns an error if the configuration is invalid or the state type cannot be serialized.
func New[T any](initial T, cfg *Config[T]) (*State[T], error) {
	s := &State[T]{current: initial}
	if cfg != nil {
		s.cloner = cfg.Cloner
		s.arrayCfg = ArrayConfig{Strategy: cfg.ArrayStrategy, KeyField: cfg.ArrayKeyField}

		// Validate ArrayConfig
		if cfg.ArrayStrategy == ArrayByKey && cfg.ArrayKeyField == "" {
			return nil, fmt.Errorf("statediff: ArrayByKey strategy requires ArrayKeyField to be set")
		}
	}

	// Validate that state type can be JSON serialized (only if no custom cloner)
	if s.cloner == nil {
		data, err := json.Marshal(initial)
		if err != nil {
			return nil, fmt.Errorf("statediff: state type cannot be JSON marshaled: %w", err)
		}
		var test T
		if err := json.Unmarshal(data, &test); err != nil {
			return nil, fmt.Errorf("statediff: state type cannot be JSON unmarshaled: %w", err)
		}
	}

	return s, nil
}

// MustNew creates a new State, panicking if there's an error.
// Use New() for error handling in production code.
func MustNew[T any](initial T, cfg *Config[T]) *State[T] {
	s, err := New(initial, cfg)
	if err != nil {
		panic(err)
	}
	return s
}

// clone creates a deep copy.
// If no custom cloner is set, uses JSON marshal/unmarshal (slower but universal).
// Note: New() validates that the type can be serialized, so errors here indicate
// a bug (e.g., state was modified to include unserializable fields).
func (s *State[T]) clone(src T) T {
	if s.cloner != nil {
		return s.cloner(src)
	}
	var dst T
	data, err := json.Marshal(src)
	if err != nil {
		// This shouldn't happen if New() validated the type correctly.
		// Panic because silent failure would cause diff corruption.
		panic(fmt.Sprintf("statediff: clone marshal failed (type changed after New?): %v", err))
	}
	if err := json.Unmarshal(data, &dst); err != nil {
		panic(fmt.Sprintf("statediff: clone unmarshal failed: %v", err))
	}
	return dst
}

// withEffects returns state with all effects applied
func (s *State[T]) withEffects(state T) T {
	result := s.clone(state)
	for _, e := range s.effects {
		result = e.Apply(result)
	}
	return result
}

// Get returns current state with effects applied
func (s *State[T]) Get() T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.withEffects(s.current)
}

// GetBase returns current state without effects
func (s *State[T]) GetBase() T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clone(s.current)
}

// Update modifies the state. Saves previous for diff calculation.
func (s *State[T]) Update(fn func(*T)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previous = s.withEffects(s.current)
	s.hasPrevi = true
	fn(&s.current)
}

// Set replaces the entire state
func (s *State[T]) Set(newState T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previous = s.withEffects(s.current)
	s.hasPrevi = true
	s.current = s.clone(newState)
}

// AddEffect adds a reversible effect.
// Returns an error if an effect with the same ID already exists.
func (s *State[T]) AddEffect(e Effect[T]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicate ID
	for _, existing := range s.effects {
		if existing.ID() == e.ID() {
			return fmt.Errorf("statediff: effect with ID %q already exists", e.ID())
		}
	}

	s.previous = s.withEffects(s.current)
	s.hasPrevi = true
	s.effects = append(s.effects, e)
	return nil
}

// RemoveEffect removes an effect by ID
func (s *State[T]) RemoveEffect(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.effects {
		if e.ID() == id {
			s.previous = s.withEffects(s.current)
			s.hasPrevi = true
			s.effects = append(s.effects[:i], s.effects[i+1:]...)
			return true
		}
	}
	return false
}

// HasEffect checks if an effect is active
func (s *State[T]) HasEffect(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.effects {
		if e.ID() == id {
			return true
		}
	}
	return false
}

// ClearEffects removes all effects
func (s *State[T]) ClearEffects() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.effects) > 0 {
		s.previous = s.withEffects(s.current)
		s.hasPrevi = true
		s.effects = nil
	}
}

// Diff calculates diff between previous and current state for a viewer.
// If no previous state exists, returns nil (caller should send full state).
func (s *State[T]) Diff(project func(T) T) (Patch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.hasPrevi {
		return nil, nil
	}

	current := s.withEffects(s.current)

	oldProj := s.previous
	newProj := current
	if project != nil {
		oldProj = project(s.previous)
		newProj = project(current)
	}

	return calcDiff(oldProj, newProj, s.arrayCfg)
}

// FullState returns the complete state for a viewer (for initial sync)
func (s *State[T]) FullState(project func(T) T) T {
	s.mu.RLock()
	defer s.mu.RUnlock()

	current := s.withEffects(s.current)
	if project != nil {
		return project(current)
	}
	return current
}

// ClearPrevious clears the previous state.
// Call after broadcasting to all clients.
func (s *State[T]) ClearPrevious() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasPrevi = false
}

// HasChanges returns true if there are changes to broadcast
func (s *State[T]) HasChanges() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasPrevi
}

// GetEffect returns an effect by ID, or nil if not found
func (s *State[T]) GetEffect(id string) Effect[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.effects {
		if e.ID() == id {
			return e
		}
	}
	return nil
}

// Effects returns a copy of all active effects
func (s *State[T]) Effects() []Effect[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.effects) == 0 {
		return nil
	}
	return append([]Effect[T]{}, s.effects...)
}

// Expirable interface for effects that can expire
type Expirable interface {
	Expired() bool
}

// CleanupExpired removes all expired effects.
// Returns the number of effects removed.
func (s *State[T]) CleanupExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.effects) == 0 {
		return 0
	}

	// Check if any effects are expirable and expired
	hasExpired := false
	for _, e := range s.effects {
		if exp, ok := e.(Expirable); ok && exp.Expired() {
			hasExpired = true
			break
		}
	}

	if !hasExpired {
		return 0
	}

	// Only save previous state if there's no pending change already.
	// If hasPrevi is true, an Update() happened this cycle and we must NOT
	// overwrite previous, or we'll lose the state change diff.
	if !s.hasPrevi {
		s.previous = s.withEffects(s.current)
		s.hasPrevi = true
	}

	// Filter out expired effects
	removed := 0
	active := s.effects[:0]
	for _, e := range s.effects {
		if exp, ok := e.(Expirable); ok && exp.Expired() {
			removed++
			continue
		}
		active = append(active, e)
	}
	s.effects = active

	return removed
}
