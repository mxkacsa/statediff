package statediff

import (
	"encoding/json"
	"sync"
)

// Session manages multiple client connections.
// T is the state type, A is the activator type, ID is the client identifier type.
// Each client has a projection function that determines what they see.
type Session[T, A any, ID comparable] struct {
	mu      sync.RWMutex
	state   *State[T, A]
	clients map[ID]func(T) T // ID -> projection function
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
