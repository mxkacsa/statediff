package statediff

import (
	"sync"
	"time"
)

// Effect is a reversible state transformation.
// Effects don't mutate base state - they transform on read.
type Effect[T any] interface {
	ID() string
	Apply(T) T
}

// Func creates a simple effect from a function
func Func[T any](id string, fn func(T) T) Effect[T] {
	return &funcEffect[T]{id: id, fn: fn}
}

type funcEffect[T any] struct {
	id string
	fn func(T) T
}

func (e *funcEffect[T]) ID() string  { return e.id }
func (e *funcEffect[T]) Apply(s T) T { return e.fn(s) }

// Timed creates an effect that expires after duration.
// The effect is active immediately and expires after dur.
// Uses time.Now by default - set TimeFunc to nil to disable time checks,
// or provide a custom time function for testing.
func Timed[T any](id string, dur time.Duration, fn func(T) T) *TimedEffect[T] {
	now := time.Now()
	return &TimedEffect[T]{
		id:        id,
		fn:        fn,
		startsAt:  now,
		expiresAt: now.Add(dur),
		TimeFunc:  time.Now,
	}
}

// TimedWindow creates an effect that activates at startAt and expires at expiresAt.
// If startAt is zero, the effect is active immediately.
// If expiresAt is zero, the effect never expires (only startAt matters).
// Uses time.Now by default - set TimeFunc to nil to disable time checks,
// or provide a custom time function for testing.
func TimedWindow[T any](id string, startsAt, expiresAt time.Time, fn func(T) T) *TimedEffect[T] {
	return &TimedEffect[T]{
		id:        id,
		fn:        fn,
		startsAt:  startsAt,
		expiresAt: expiresAt,
		TimeFunc:  time.Now,
	}
}

// Delayed creates an effect that activates after delay and lasts for duration.
// Uses time.Now by default - set TimeFunc to nil to disable time checks,
// or provide a custom time function for testing.
func Delayed[T any](id string, delay, duration time.Duration, fn func(T) T) *TimedEffect[T] {
	now := time.Now()
	return &TimedEffect[T]{
		id:        id,
		fn:        fn,
		startsAt:  now.Add(delay),
		expiresAt: now.Add(delay + duration),
		TimeFunc:  time.Now,
	}
}

// TimedEffect is an effect with optional start time and expiration.
// The effect is only active between startsAt and expiresAt.
// Thread-safe: all methods can be called concurrently.
//
// Time handling: If TimeFunc is nil, time checks are skipped and the effect
// is always active. Set TimeFunc to time.Now for real-time behavior, or
// provide a custom function for deterministic/testable time.
type TimedEffect[T any] struct {
	mu        sync.RWMutex
	id        string
	fn        func(T) T
	startsAt  time.Time        // Zero means active immediately
	expiresAt time.Time        // Zero means never expires
	TimeFunc  func() time.Time // If nil, time checks are skipped
}

func (e *TimedEffect[T]) ID() string { return e.id }

func (e *TimedEffect[T]) Apply(s T) T {
	e.mu.RLock()
	startsAt := e.startsAt
	expiresAt := e.expiresAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	// If no time function provided, skip time checks (always active)
	if timeFunc == nil {
		return e.fn(s)
	}

	now := timeFunc()

	// Check if not yet started
	if !startsAt.IsZero() && now.Before(startsAt) {
		return s // Not yet active
	}

	// Check if expired
	if !expiresAt.IsZero() && now.After(expiresAt) {
		return s // Expired, no-op
	}

	return e.fn(s)
}

// Active returns true if the effect is currently active (started and not expired).
// Returns true if TimeFunc is nil (no time checks).
func (e *TimedEffect[T]) Active() bool {
	e.mu.RLock()
	startsAt := e.startsAt
	expiresAt := e.expiresAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	if timeFunc == nil {
		return true // No time checks, always active
	}

	now := timeFunc()
	if !startsAt.IsZero() && now.Before(startsAt) {
		return false
	}
	if !expiresAt.IsZero() && now.After(expiresAt) {
		return false
	}
	return true
}

// Started returns true if the effect has started (or startsAt is zero).
// Returns true if TimeFunc is nil (no time checks).
func (e *TimedEffect[T]) Started() bool {
	e.mu.RLock()
	startsAt := e.startsAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	if timeFunc == nil {
		return true
	}
	return startsAt.IsZero() || !timeFunc().Before(startsAt)
}

// Expired returns true if the effect has expired.
// Returns false if TimeFunc is nil (no time checks).
func (e *TimedEffect[T]) Expired() bool {
	e.mu.RLock()
	expiresAt := e.expiresAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	if timeFunc == nil {
		return false // No time checks, never expires
	}
	return !expiresAt.IsZero() && timeFunc().After(expiresAt)
}

// Remaining returns the duration until expiration (0 if expired, no expiration, or no TimeFunc)
func (e *TimedEffect[T]) Remaining() time.Duration {
	e.mu.RLock()
	expiresAt := e.expiresAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	if timeFunc == nil || expiresAt.IsZero() {
		return 0
	}
	if r := expiresAt.Sub(timeFunc()); r > 0 {
		return r
	}
	return 0
}

// UntilStart returns the duration until the effect starts (0 if already started or no TimeFunc)
func (e *TimedEffect[T]) UntilStart() time.Duration {
	e.mu.RLock()
	startsAt := e.startsAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	if timeFunc == nil || startsAt.IsZero() {
		return 0
	}
	if r := startsAt.Sub(timeFunc()); r > 0 {
		return r
	}
	return 0
}

// Extend extends the expiration time by the given duration
func (e *TimedEffect[T]) Extend(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.expiresAt.IsZero() {
		return // No expiration to extend
	}
	e.expiresAt = e.expiresAt.Add(d)
}

// SetStartsAt changes the start time
func (e *TimedEffect[T]) SetStartsAt(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startsAt = t
}

// SetExpiresAt changes the expiration time
func (e *TimedEffect[T]) SetExpiresAt(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expiresAt = t
}

// Conditional creates an effect that only applies when condition is true
func Conditional[T any](id string, cond func(T) bool, fn func(T) T) Effect[T] {
	return &condEffect[T]{id: id, cond: cond, fn: fn}
}

type condEffect[T any] struct {
	id   string
	cond func(T) bool
	fn   func(T) T
}

func (e *condEffect[T]) ID() string { return e.id }

func (e *condEffect[T]) Apply(s T) T {
	if e.cond(s) {
		return e.fn(s)
	}
	return s
}

// Toggle creates an effect that can be enabled/disabled
func Toggle[T any](id string, fn func(T) T) *ToggleEffect[T] {
	return &ToggleEffect[T]{id: id, fn: fn, enabled: true}
}

// ToggleEffect can be switched on/off
type ToggleEffect[T any] struct {
	mu      sync.RWMutex
	id      string
	fn      func(T) T
	enabled bool
}

func (e *ToggleEffect[T]) ID() string { return e.id }

func (e *ToggleEffect[T]) Apply(s T) T {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.enabled {
		return e.fn(s)
	}
	return s
}

func (e *ToggleEffect[T]) Enable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = true
}

func (e *ToggleEffect[T]) Disable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = false
}

func (e *ToggleEffect[T]) SetEnabled(b bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = b
}

func (e *ToggleEffect[T]) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// Stack creates a stackable effect where multiple values combine
func Stack[T any, V any](id string, combine func(T, []V) T) *StackEffect[T, V] {
	return &StackEffect[T, V]{id: id, combine: combine}
}

// StackEffect accumulates values
type StackEffect[T any, V any] struct {
	mu      sync.RWMutex
	id      string
	values  []V
	combine func(T, []V) T
}

func (e *StackEffect[T, V]) ID() string { return e.id }

func (e *StackEffect[T, V]) Apply(s T) T {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.values) == 0 {
		return s
	}
	return e.combine(s, e.values)
}

func (e *StackEffect[T, V]) Push(v V) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = append(e.values, v)
}

func (e *StackEffect[T, V]) Pop() (V, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.values) == 0 {
		var zero V
		return zero, false
	}
	v := e.values[len(e.values)-1]
	e.values = e.values[:len(e.values)-1]
	return v, true
}

func (e *StackEffect[T, V]) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = nil
}

func (e *StackEffect[T, V]) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.values)
}
