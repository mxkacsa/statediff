package statediff

import (
	"encoding/json"
	"sync"
	"time"
)

// Session manages multiple client connections.
// T is the state type, A is the activator type, ID is the client identifier type.
// Each client has a projection function that determines what they see.
type Session[T, A any, ID comparable] struct {
	mu      sync.RWMutex
	state   *State[T, A]
	clients map[ID]func(T) T // ID -> projection function

	// Debounce support
	debounceMu    sync.Mutex
	debounce      time.Duration
	debounceTimer *time.Timer
	onBroadcast   func(map[ID][]byte)
}

// NewSession creates a session manager for the given state
func NewSession[T, A any, ID comparable](state *State[T, A]) *Session[T, A, ID] {
	return &Session[T, A, ID]{
		state:   state,
		clients: make(map[ID]func(T) T),
	}
}

// Connect registers a client with their projection function.
// Projection can be nil if client sees full state.
func (s *Session[T, A, ID]) Connect(id ID, project func(T) T) {
	s.mu.Lock()
	s.clients[id] = project
	s.mu.Unlock()
}

// Disconnect removes a client
func (s *Session[T, A, ID]) Disconnect(id ID) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
}

// IsConnected checks if a client is registered
func (s *Session[T, A, ID]) IsConnected(id ID) bool {
	s.mu.RLock()
	_, ok := s.clients[id]
	s.mu.RUnlock()
	return ok
}

// Count returns number of connected clients
func (s *Session[T, A, ID]) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// IDs returns all connected client IDs
func (s *Session[T, A, ID]) IDs() []ID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]ID, 0, len(s.clients))
	for id := range s.clients {
		ids = append(ids, id)
	}
	return ids
}

// Full returns the full state for a client (for initial sync).
// Thread-safe: holds lock during state access to prevent races.
func (s *Session[T, A, ID]) Full(id ID) ([]byte, error) {
	s.mu.RLock()
	project := s.clients[id]
	state := s.state.FullState(project)
	s.mu.RUnlock()

	// Wrap as replace operation
	patch := Patch{{Op: "replace", Path: "", Value: state}}
	return json.Marshal(patch)
}

// Diff returns the diff for a client since last change.
// Thread-safe: holds lock during diff calculation to prevent races.
func (s *Session[T, A, ID]) Diff(id ID) ([]byte, error) {
	s.mu.RLock()
	project := s.clients[id]
	patch, err := s.state.Diff(project)
	s.mu.RUnlock()

	if err != nil {
		return nil, err
	}
	if patch == nil || patch.Empty() {
		return []byte("[]"), nil
	}
	return patch.JSON()
}

// Broadcast returns diffs for all connected clients.
// Only includes clients with actual changes.
// Optimized: caches the diff for clients with nil projection (full state view).
func (s *Session[T, A, ID]) Broadcast() map[ID][]byte {
	if !s.state.HasChanges() {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[ID][]byte, len(s.clients))

	// Cache for nil projection (full state view) - computed once, reused for all
	var fullDiff []byte
	var fullDiffComputed bool

	for id, project := range s.clients {
		var data []byte

		if project == nil {
			// Use cached full diff
			if !fullDiffComputed {
				patch, err := s.state.Diff(nil)
				if err != nil || patch.Empty() {
					fullDiff = nil
				} else {
					fullDiff, _ = patch.JSON()
				}
				fullDiffComputed = true
			}
			data = fullDiff
		} else {
			// Compute individual diff for custom projection
			patch, err := s.state.Diff(project)
			if err != nil || patch.Empty() {
				continue
			}
			data, _ = patch.JSON()
		}

		if data != nil {
			result[id] = data
		}
	}

	return result
}

// Tick cleans up expired effects, broadcasts changes, and clears previous state.
// This is the recommended way to use the library - just call Tick() after state updates.
// Typical game loop: Update state -> Tick -> Send to clients
func (s *Session[T, A, ID]) Tick() map[ID][]byte {
	s.state.CleanupExpired() // Automatically handle expired effects
	result := s.Broadcast()
	s.state.ClearPrevious()
	return result
}

// State returns the underlying state for modifications
func (s *Session[T, A, ID]) State() *State[T, A] {
	return s.state
}

// Tx represents a transaction scope for state modifications.
// All updates within a transaction are batched and broadcast together.
type Tx[T, A any] struct {
	state *State[T, A]
}

// Update modifies the state within the transaction
func (tx *Tx[T, A]) Update(fn func(*T)) {
	tx.state.Update(fn)
}

// Set replaces the entire state within the transaction
func (tx *Tx[T, A]) Set(newState T) {
	tx.state.Set(newState)
}

// Get returns the current state with effects applied
func (tx *Tx[T, A]) Get() T {
	return tx.state.Get()
}

// GetBase returns the current state without effects
func (tx *Tx[T, A]) GetBase() T {
	return tx.state.GetBase()
}

// Transaction executes a function with batched state updates and automatically broadcasts changes.
// This is the recommended way to modify state - it ensures all updates are broadcast together
// and makes it impossible to forget the broadcast.
//
// Example:
//
//	diffs := session.Transaction(func(tx *Tx[Game, string]) {
//	    tx.Update(func(g *Game) { g.Round++ })
//	    tx.Update(func(g *Game) { g.Phase = "draw" })
//	})
//	// diffs are automatically generated and ready to send
func (s *Session[T, A, ID]) Transaction(fn func(tx *Tx[T, A])) map[ID][]byte {
	tx := &Tx[T, A]{state: s.state}
	fn(tx)
	return s.Tick()
}

// ApplyUpdate is a shorthand for a single state update with automatic broadcast.
// Use Transaction() for multiple updates that should be batched together.
//
// Example:
//
//	diffs := session.ApplyUpdate(func(g *Game) { g.Round++ })
func (s *Session[T, A, ID]) ApplyUpdate(fn func(*T)) map[ID][]byte {
	s.state.Update(fn)
	return s.Tick()
}

// OnEffectExpired is a callback type for effect expiration notifications.
// The callback receives the effect ID and should return the diffs to broadcast.
type OnEffectExpired[ID comparable] func(effectID string) map[ID][]byte

// AddEffectWithExpiration adds an effect and schedules automatic expiration.
// When the effect expires, onExpire is called which triggers Tick() and returns diffs.
// The onExpire callback is called from a goroutine - use the returned diffs to broadcast.
//
// For effects that don't implement Schedulable, this behaves like State.AddEffect.
//
// Example:
//
//	effect := Timed[Game, string]("powerup", 30*time.Second, func(g Game, a string) Game {
//	    g.Speed *= 2
//	    return g
//	})
//	session.AddEffectWithExpiration(effect, "player1", func(id string) map[string][]byte {
//	    diffs := session.Tick()
//	    for clientID, data := range diffs {
//	        sendToClient(clientID, data)
//	    }
//	    return diffs
//	})
func (s *Session[T, A, ID]) AddEffectWithExpiration(e Effect[T, A], activator A, onExpire OnEffectExpired[ID]) error {
	if err := s.state.AddEffect(e, activator); err != nil {
		return err
	}

	// Schedule expiration if the effect supports it
	if sched, ok := any(e).(Schedulable); ok && onExpire != nil {
		sched.ScheduleExpiration(func(effectID string) {
			onExpire(effectID)
		})
	}

	return nil
}

// SetDebounce sets the debounce duration for broadcasts.
// When set to a non-zero value, ScheduleBroadcast will wait for the specified
// duration before broadcasting, accumulating any changes that occur during that time.
// Set to 0 to disable debouncing (default behavior - immediate broadcast).
func (s *Session[T, A, ID]) SetDebounce(d time.Duration) {
	s.debounceMu.Lock()
	defer s.debounceMu.Unlock()
	s.debounce = d
}

// SetBroadcastCallback sets the callback function that will be called
// when a debounced broadcast is triggered.
// The callback receives the result of Tick() - a map of client IDs to their diffs.
func (s *Session[T, A, ID]) SetBroadcastCallback(fn func(map[ID][]byte)) {
	s.debounceMu.Lock()
	defer s.debounceMu.Unlock()
	s.onBroadcast = fn
}

// ScheduleBroadcast schedules a broadcast to all clients.
// If debounce is not set (0), it immediately calls Tick() and the broadcast callback.
// If debounce is set, it waits for the debounce duration before broadcasting,
// accumulating any additional changes that occur during that time.
// Returns immediately - the actual broadcast happens asynchronously when debounce is set.
func (s *Session[T, A, ID]) ScheduleBroadcast() {
	s.debounceMu.Lock()
	defer s.debounceMu.Unlock()

	// No debounce - immediate broadcast
	if s.debounce == 0 {
		diffs := s.Tick()
		if s.onBroadcast != nil && len(diffs) > 0 {
			s.onBroadcast(diffs)
		}
		return
	}

	// Debounced broadcast - reset timer if already running
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
	}

	s.debounceTimer = time.AfterFunc(s.debounce, func() {
		s.debounceMu.Lock()
		callback := s.onBroadcast
		s.debounceTimer = nil
		s.debounceMu.Unlock()

		diffs := s.Tick()
		if callback != nil && len(diffs) > 0 {
			callback(diffs)
		}
	})
}
