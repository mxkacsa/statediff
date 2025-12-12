package statediff

import (
	"sync"
	"time"
)

// Effect is a reversible state transformation.
// Effects don't mutate base state - they transform on read.
// T is the state type, A is the activator type (e.g., string for playerID).
// Use *A or a wrapper type if you need to distinguish "no activator" from zero value.
type Effect[T, A any] interface {
	ID() string
	Apply(state T, activator A) T
	Activator() A
	SetActivator(activator A)
}

// Func creates a simple effect from a function.
// The function receives the state and activator.
func Func[T, A any](id string, fn func(state T, activator A) T) *FuncEffect[T, A] {
	return &FuncEffect[T, A]{id: id, fn: fn}
}

// FuncEffect is a simple function-based effect
type FuncEffect[T, A any] struct {
	mu        sync.RWMutex
	id        string
	fn        func(T, A) T
	activator A
}

func (e *FuncEffect[T, A]) ID() string { return e.id }

func (e *FuncEffect[T, A]) Apply(s T, activator A) T {
	return e.fn(s, activator)
}

func (e *FuncEffect[T, A]) Activator() A {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activator
}

func (e *FuncEffect[T, A]) SetActivator(activator A) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activator = activator
}

// Timed creates an effect that expires after duration.
// The effect is active immediately and expires after dur.
// Uses time.Now by default - set TimeFunc to nil to disable time checks,
// or provide a custom time function for testing.
func Timed[T, A any](id string, dur time.Duration, fn func(state T, activator A) T) *TimedEffect[T, A] {
	now := time.Now()
	return &TimedEffect[T, A]{
		id:        id,
		fn:        fn,
		startsAt:  now,
		expiresAt: now.Add(dur),
		TimeFunc:  time.Now,
	}
}

// TimedWindow creates an effect that activates at startsAt and expires at expiresAt.
// If startsAt is zero, the effect is active immediately.
// If expiresAt is zero, the effect never expires (only startsAt matters).
// Uses time.Now by default - set TimeFunc to nil to disable time checks,
// or provide a custom time function for testing.
//
// This is ideal for restoring effects from persistence - use StartsAt() and ExpiresAt()
// getters to save the times, then recreate with TimedWindow on restore:
//
//	// Save
//	params := map[string]any{
//	    "startsAt":  effect.StartsAt(),
//	    "expiresAt": effect.ExpiresAt(),
//	}
//
//	// Restore
//	effect := TimedWindow[T, A](id, params["startsAt"], params["expiresAt"], fn)
func TimedWindow[T, A any](id string, startsAt, expiresAt time.Time, fn func(state T, activator A) T) *TimedEffect[T, A] {
	return &TimedEffect[T, A]{
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
func Delayed[T, A any](id string, delay, duration time.Duration, fn func(state T, activator A) T) *TimedEffect[T, A] {
	now := time.Now()
	return &TimedEffect[T, A]{
		id:        id,
		fn:        fn,
		startsAt:  now.Add(delay),
		expiresAt: now.Add(delay + duration),
		TimeFunc:  time.Now,
	}
}

// Schedulable is an interface for effects that can schedule automatic expiration callbacks.
// Effects implementing this interface can notify the system when they expire,
// enabling automatic cleanup without polling.
type Schedulable interface {
	// ScheduleExpiration starts a timer that calls the callback when the effect expires.
	// The callback receives the effect ID. Safe to call multiple times (restarts timer).
	// Returns false if the effect has no expiration, already expired, or no TimeFunc.
	ScheduleExpiration(onExpire func(effectID string)) bool

	// CancelScheduledExpiration stops any pending expiration timer.
	// Safe to call even if no timer is scheduled.
	CancelScheduledExpiration()
}

// TimedEffect is an effect with optional start time and expiration.
// The effect is only active between startsAt and expiresAt.
// Thread-safe: all methods can be called concurrently.
//
// Time handling: If TimeFunc is nil, time checks are skipped and the effect
// is always active. Set TimeFunc to time.Now for real-time behavior, or
// provide a custom function for deterministic/testable time.
//
// Automatic expiration: Call ScheduleExpiration() to receive a callback when
// the effect expires. The timer is automatically cancelled if CancelScheduledExpiration()
// is called (e.g., when the effect is removed manually).
type TimedEffect[T, A any] struct {
	mu        sync.RWMutex
	id        string
	fn        func(T, A) T
	activator A
	startsAt  time.Time        // Zero means active immediately
	expiresAt time.Time        // Zero means never expires
	TimeFunc  func() time.Time // If nil, time checks are skipped

	// Expiration scheduling
	expireTimer *time.Timer
}

func (e *TimedEffect[T, A]) ID() string { return e.id }

func (e *TimedEffect[T, A]) Apply(s T, activator A) T {
	e.mu.RLock()
	startsAt := e.startsAt
	expiresAt := e.expiresAt
	timeFunc := e.TimeFunc
	e.mu.RUnlock()

	// If no time function provided, skip time checks (always active)
	if timeFunc == nil {
		return e.fn(s, activator)
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

	return e.fn(s, activator)
}

func (e *TimedEffect[T, A]) Activator() A {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activator
}

func (e *TimedEffect[T, A]) SetActivator(activator A) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activator = activator
}

// Active returns true if the effect is currently active (started and not expired).
// Returns true if TimeFunc is nil (no time checks).
func (e *TimedEffect[T, A]) Active() bool {
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
func (e *TimedEffect[T, A]) Started() bool {
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
func (e *TimedEffect[T, A]) Expired() bool {
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
func (e *TimedEffect[T, A]) Remaining() time.Duration {
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
func (e *TimedEffect[T, A]) UntilStart() time.Duration {
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
func (e *TimedEffect[T, A]) Extend(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.expiresAt.IsZero() {
		return // No expiration to extend
	}
	e.expiresAt = e.expiresAt.Add(d)
}

// SetStartsAt changes the start time
func (e *TimedEffect[T, A]) SetStartsAt(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startsAt = t
}

// SetExpiresAt changes the expiration time
func (e *TimedEffect[T, A]) SetExpiresAt(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expiresAt = t
}

// StartsAt returns the start time (zero if active immediately)
func (e *TimedEffect[T, A]) StartsAt() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.startsAt
}

// ExpiresAt returns the expiration time (zero if never expires)
func (e *TimedEffect[T, A]) ExpiresAt() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.expiresAt
}

// ScheduleExpiration starts a timer that calls the callback when the effect expires.
// The callback receives the effect ID. Safe to call multiple times (restarts timer).
// Returns false if the effect has no expiration, already expired, or no TimeFunc.
func (e *TimedEffect[T, A]) ScheduleExpiration(onExpire func(effectID string)) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Cancel any existing timer
	if e.expireTimer != nil {
		e.expireTimer.Stop()
		e.expireTimer = nil
	}

	// Cannot schedule if no time function or no expiration
	if e.TimeFunc == nil || e.expiresAt.IsZero() {
		return false
	}

	// Calculate remaining time
	remaining := e.expiresAt.Sub(e.TimeFunc())
	if remaining <= 0 {
		// Already expired
		return false
	}

	// Store effect ID for the closure
	id := e.id

	e.expireTimer = time.AfterFunc(remaining, func() {
		e.mu.Lock()
		// Clear timer reference since it has fired
		e.expireTimer = nil
		e.mu.Unlock()

		// Call the callback
		if onExpire != nil {
			onExpire(id)
		}
	})

	return true
}

// CancelScheduledExpiration stops any pending expiration timer.
// Safe to call even if no timer is scheduled.
func (e *TimedEffect[T, A]) CancelScheduledExpiration() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.expireTimer != nil {
		e.expireTimer.Stop()
		e.expireTimer = nil
	}
}

// Conditional creates an effect that only applies when condition is true.
// Both condition and function receive the state and activator.
func Conditional[T, A any](id string, cond func(state T, activator A) bool, fn func(state T, activator A) T) *CondEffect[T, A] {
	return &CondEffect[T, A]{id: id, cond: cond, fn: fn}
}

// CondEffect is a conditional effect
type CondEffect[T, A any] struct {
	mu        sync.RWMutex
	id        string
	cond      func(T, A) bool
	fn        func(T, A) T
	activator A
}

func (e *CondEffect[T, A]) ID() string { return e.id }

func (e *CondEffect[T, A]) Apply(s T, activator A) T {
	if e.cond(s, activator) {
		return e.fn(s, activator)
	}
	return s
}

func (e *CondEffect[T, A]) Activator() A {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activator
}

func (e *CondEffect[T, A]) SetActivator(activator A) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activator = activator
}

// Toggle creates an effect that can be enabled/disabled.
func Toggle[T, A any](id string, fn func(state T, activator A) T) *ToggleEffect[T, A] {
	return &ToggleEffect[T, A]{id: id, fn: fn, enabled: true}
}

// ToggleEffect can be switched on/off
type ToggleEffect[T, A any] struct {
	mu        sync.RWMutex
	id        string
	fn        func(T, A) T
	activator A
	enabled   bool
}

func (e *ToggleEffect[T, A]) ID() string { return e.id }

func (e *ToggleEffect[T, A]) Apply(s T, activator A) T {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.enabled {
		return e.fn(s, activator)
	}
	return s
}

func (e *ToggleEffect[T, A]) Activator() A {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activator
}

func (e *ToggleEffect[T, A]) SetActivator(activator A) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activator = activator
}

func (e *ToggleEffect[T, A]) Enable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = true
}

func (e *ToggleEffect[T, A]) Disable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = false
}

func (e *ToggleEffect[T, A]) SetEnabled(b bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled = b
}

func (e *ToggleEffect[T, A]) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// Stack creates a stackable effect where multiple values combine.
// The combine function receives the state, accumulated values, and activator.
func Stack[T, A, V any](id string, combine func(state T, values []V, activator A) T) *StackEffect[T, A, V] {
	return &StackEffect[T, A, V]{id: id, combine: combine}
}

// StackEffect accumulates values
type StackEffect[T, A, V any] struct {
	mu        sync.RWMutex
	id        string
	values    []V
	activator A
	combine   func(T, []V, A) T
}

func (e *StackEffect[T, A, V]) ID() string { return e.id }

func (e *StackEffect[T, A, V]) Apply(s T, activator A) T {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.values) == 0 {
		return s
	}
	return e.combine(s, e.values, activator)
}

func (e *StackEffect[T, A, V]) Activator() A {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activator
}

func (e *StackEffect[T, A, V]) SetActivator(activator A) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activator = activator
}

func (e *StackEffect[T, A, V]) Push(v V) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = append(e.values, v)
}

func (e *StackEffect[T, A, V]) Pop() (V, bool) {
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

func (e *StackEffect[T, A, V]) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = nil
}

func (e *StackEffect[T, A, V]) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.values)
}
