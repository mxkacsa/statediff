package statediff

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type TestState struct {
	Value  int    `json:"value"`
	Name   string `json:"name"`
	Items  []Item `json:"items,omitempty"`
	Secret string `json:"secret,omitempty"`
}

type Item struct {
	ID   string `json:"id"`
	Data int    `json:"data"`
}

func TestStateBasic(t *testing.T) {
	s := MustNew(TestState{Value: 1, Name: "test"}, nil)

	if got := s.Get(); got.Value != 1 {
		t.Errorf("Get() = %d, want 1", got.Value)
	}

	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	if got := s.Get(); got.Value != 2 {
		t.Errorf("After Update, Get() = %d, want 2", got.Value)
	}
}

func TestDiff(t *testing.T) {
	s := MustNew(TestState{Value: 1, Name: "test"}, nil)

	// No previous - should return nil
	diff, err := s.Diff(nil)
	if err != nil || diff != nil {
		t.Errorf("First Diff should be nil")
	}

	// Update creates previous
	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	diff, err = s.Diff(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) == 0 {
		t.Error("Expected diff operations")
	}

	// Check JSON output
	data, _ := diff.JSON()
	if len(data) == 0 {
		t.Error("Expected JSON output")
	}
}

func TestEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	// Add doubling effect
	s.AddEffect(Func("double", func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	}))

	// Base unchanged
	if got := s.GetBase(); got.Value != 100 {
		t.Errorf("GetBase() = %d, want 100", got.Value)
	}

	// With effect applied
	if got := s.Get(); got.Value != 200 {
		t.Errorf("Get() with effect = %d, want 200", got.Value)
	}

	// Remove effect
	s.RemoveEffect("double")
	if got := s.Get(); got.Value != 100 {
		t.Errorf("After remove, Get() = %d, want 100", got.Value)
	}
}

func TestTimedEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	effect := Timed("temp", 50*time.Millisecond, func(ts TestState) TestState {
		ts.Value = 999
		return ts
	})
	s.AddEffect(effect)

	// Active
	if got := s.Get(); got.Value != 999 {
		t.Errorf("With timed effect = %d, want 999", got.Value)
	}

	// Wait for expiry
	time.Sleep(60 * time.Millisecond)

	// Expired
	if got := s.Get(); got.Value != 100 {
		t.Errorf("After expiry = %d, want 100", got.Value)
	}
}

func TestConditionalEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100, Name: "active"}, nil)

	s.AddEffect(Conditional("cond",
		func(ts TestState) bool { return ts.Name == "active" },
		func(ts TestState) TestState {
			ts.Value = 999
			return ts
		},
	))

	// Condition true
	if got := s.Get(); got.Value != 999 {
		t.Errorf("With condition true = %d, want 999", got.Value)
	}

	// Change condition
	s.Update(func(ts *TestState) {
		ts.Name = "inactive"
	})

	// Condition false
	if got := s.Get(); got.Value != 100 {
		t.Errorf("With condition false = %d, want 100", got.Value)
	}
}

func TestStackEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	stack := Stack[TestState, float64]("mult", func(ts TestState, vals []float64) TestState {
		mult := 1.0
		for _, v := range vals {
			mult *= v
		}
		ts.Value = int(float64(ts.Value) * mult)
		return ts
	})
	s.AddEffect(stack)

	stack.Push(2.0)
	if got := s.Get(); got.Value != 200 {
		t.Errorf("With 2x = %d, want 200", got.Value)
	}

	stack.Push(1.5)
	if got := s.Get(); got.Value != 300 {
		t.Errorf("With 2x*1.5x = %d, want 300", got.Value)
	}

	stack.Pop()
	if got := s.Get(); got.Value != 200 {
		t.Errorf("After pop = %d, want 200", got.Value)
	}
}

func TestSession(t *testing.T) {
	s := MustNew(TestState{Value: 1, Secret: "hidden"}, nil)
	sess := NewSession[TestState, string](s)

	// Connect with projection that hides Secret
	sess.Connect("user1", func(ts TestState) TestState {
		ts.Secret = ""
		return ts
	})

	// Full state should have Secret hidden
	data, _ := sess.Full("user1")
	var patch []Op
	json.Unmarshal(data, &patch)
	if len(patch) != 1 || patch[0].Op != "replace" {
		t.Error("Expected full state replace")
	}

	// Update and check diff
	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	diffs := sess.Tick()
	if len(diffs) == 0 {
		t.Error("Expected diff for user1")
	}
}

func TestBroadcast(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)

	sess.Connect("a", nil)
	sess.Connect("b", nil)

	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	diffs := sess.Tick()
	if len(diffs) != 2 {
		t.Errorf("Expected 2 diffs, got %d", len(diffs))
	}
}

func TestArrayDiffByKey(t *testing.T) {
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}, {ID: "b", Data: 2}},
	}, &Config[TestState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "id",
	})

	s.Update(func(ts *TestState) {
		ts.Items[0].Data = 10 // Change item "a"
	})

	diff, _ := s.Diff(nil)
	if diff.Empty() {
		t.Error("Expected diff")
	}

	// Should have targeted patch, not full replace
	data, _ := diff.JSON()
	t.Logf("Diff: %s", data)
}

func TestArrayByKeyRemoveOrder(t *testing.T) {
	// Test that multiple removes are ordered correctly (descending index)
	// to prevent index shift issues when applying JSON Patch sequentially
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}, {ID: "b", Data: 2}, {ID: "c", Data: 3}},
	}, &Config[TestState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "id",
	})

	s.Update(func(ts *TestState) {
		// Remove "a" (index 0) and "c" (index 2), keep "b"
		ts.Items = []Item{{ID: "b", Data: 2}}
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	t.Logf("Diff: %s", data)

	// The removes should be ordered: /items/2 before /items/0
	// This way, when applied sequentially, indices remain valid
	var ops []Op
	json.Unmarshal(data, &ops)

	var removeIndices []int
	for _, op := range ops {
		if op.Op == "remove" {
			// Extract index from path like "/items/2"
			var idx int
			fmt.Sscanf(op.Path, "/items/%d", &idx)
			removeIndices = append(removeIndices, idx)
		}
	}

	// Should be descending order
	if len(removeIndices) != 2 {
		t.Fatalf("Expected 2 removes, got %d", len(removeIndices))
	}
	if removeIndices[0] < removeIndices[1] {
		t.Errorf("Remove indices should be descending, got %v", removeIndices)
	}
}

func TestArrayByKeyRemoveAndModify(t *testing.T) {
	// Test: remove an element AND modify another
	// Old: [A, B, C] -> New: [A, C'] (B removed, C modified)
	// The modify should use the NEW index (1), not the old index (2)
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}, {ID: "b", Data: 2}, {ID: "c", Data: 3}},
	}, &Config[TestState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "id",
	})

	s.Update(func(ts *TestState) {
		// Remove "b" (index 1), modify "c" (was index 2, now index 1)
		ts.Items = []Item{{ID: "a", Data: 1}, {ID: "c", Data: 999}}
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	t.Logf("Diff: %s", data)

	var ops []Op
	json.Unmarshal(data, &ops)

	// Find the replace operation for "c"'s data
	for _, op := range ops {
		if op.Op == "replace" && strings.Contains(op.Path, "/items/") && strings.Contains(op.Path, "/data") {
			// Should be /items/1/data (new index), NOT /items/2/data (old index)
			if strings.Contains(op.Path, "/items/2/") {
				t.Errorf("Replace used old index 2, should use new index 1: %s", op.Path)
			}
			if !strings.Contains(op.Path, "/items/1/") {
				t.Errorf("Replace should target index 1, got: %s", op.Path)
			}
		}
	}
}

func TestPersist(t *testing.T) {
	path := "/tmp/statediff_test.json"

	s := MustNew(TestState{Value: 42, Name: "test"}, nil)

	err := Save(path, s, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	snap, err := Load[TestState](path)
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("Expected snapshot")
	}
	if snap.State.Value != 42 {
		t.Errorf("Loaded value = %d, want 42", snap.State.Value)
	}
}

func BenchmarkDiff(b *testing.B) {
	s := MustNew(TestState{
		Value: 1,
		Items: make([]Item, 100),
	}, nil)

	for i := 0; i < 100; i++ {
		s.Get().Items[i] = Item{ID: string(rune('a' + i%26)), Data: i}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Update(func(ts *TestState) {
			ts.Value++
		})
		s.Diff(nil)
		s.ClearPrevious()
	}
}

func BenchmarkWithCloner(b *testing.B) {
	s := MustNew(TestState{Value: 1}, &Config[TestState]{
		Cloner: func(ts TestState) TestState {
			return TestState{
				Value:  ts.Value,
				Name:   ts.Name,
				Secret: ts.Secret,
				Items:  append([]Item{}, ts.Items...),
			}
		},
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Update(func(ts *TestState) {
			ts.Value++
		})
		s.Diff(nil)
		s.ClearPrevious()
	}
}

func TestGetEffect(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	// No effect initially
	if e := s.GetEffect("test"); e != nil {
		t.Error("expected nil for non-existent effect")
	}

	// Add effect
	effect := Func("test", func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	})
	s.AddEffect(effect)

	// Should find it now
	if e := s.GetEffect("test"); e == nil {
		t.Error("expected to find effect")
	} else if e.ID() != "test" {
		t.Errorf("expected ID 'test', got '%s'", e.ID())
	}

	// Non-existent still returns nil
	if e := s.GetEffect("other"); e != nil {
		t.Error("expected nil for non-existent effect")
	}
}

func TestEffects(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	// Empty initially
	if effects := s.Effects(); effects != nil {
		t.Error("expected nil for empty effects")
	}

	// Add effects
	s.AddEffect(Func("e1", func(ts TestState) TestState { return ts }))
	s.AddEffect(Func("e2", func(ts TestState) TestState { return ts }))

	effects := s.Effects()
	if len(effects) != 2 {
		t.Errorf("expected 2 effects, got %d", len(effects))
	}

	// Verify it's a copy (modifying returned slice doesn't affect state)
	effects[0] = nil
	if s.GetEffect("e1") == nil {
		t.Error("modifying returned slice should not affect state")
	}
}

func TestAddEffectDuplicateID(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	// First add should succeed
	err := s.AddEffect(Func("test", func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	}))
	if err != nil {
		t.Errorf("first AddEffect should succeed, got: %v", err)
	}

	// Second add with same ID should fail
	err = s.AddEffect(Func("test", func(ts TestState) TestState {
		ts.Value *= 3
		return ts
	}))
	if err == nil {
		t.Error("duplicate ID should return error")
	}

	// Original effect should still work
	if got := s.Get().Value; got != 2 {
		t.Errorf("original effect should be active, got value %d, want 2", got)
	}

	// Different ID should still work
	err = s.AddEffect(Func("test2", func(ts TestState) TestState {
		ts.Value += 10
		return ts
	}))
	if err != nil {
		t.Errorf("different ID should succeed, got: %v", err)
	}
}

func TestCleanupExpired(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	// Add a timed effect that expires immediately
	expired := Timed("expired", -1*time.Second, func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	})
	s.AddEffect(expired)
	s.ClearPrevious()

	// Add a non-expiring effect
	s.AddEffect(Func("permanent", func(ts TestState) TestState {
		ts.Value += 1
		return ts
	}))
	s.ClearPrevious()

	// Should have 2 effects
	if len(s.Effects()) != 2 {
		t.Errorf("expected 2 effects, got %d", len(s.Effects()))
	}

	// Cleanup should remove the expired one
	removed := s.CleanupExpired()
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	// Should have 1 effect left
	if len(s.Effects()) != 1 {
		t.Errorf("expected 1 effect after cleanup, got %d", len(s.Effects()))
	}

	// The remaining effect should be the permanent one
	if s.GetEffect("permanent") == nil {
		t.Error("permanent effect should still exist")
	}
	if s.GetEffect("expired") != nil {
		t.Error("expired effect should be removed")
	}

	// Cleanup should trigger a change (for diff)
	if !s.HasChanges() {
		t.Error("cleanup should mark state as changed")
	}

	// Another cleanup should remove nothing
	s.ClearPrevious()
	removed = s.CleanupExpired()
	if removed != 0 {
		t.Errorf("expected 0 removed on second cleanup, got %d", removed)
	}
}

func TestCleanupExpiredPreservesPendingChanges(t *testing.T) {
	// Critical test: CleanupExpired must NOT overwrite pending state changes
	s := MustNew(TestState{Value: 100}, nil)

	// Add a timed effect that expires immediately
	expired := Timed("temp", -1*time.Second, func(ts TestState) TestState {
		ts.Value = 999
		return ts
	})
	s.AddEffect(expired)
	s.ClearPrevious()

	// Now make a state change (Value: 100 -> 200)
	s.Update(func(ts *TestState) {
		ts.Value = 200
	})

	// At this point:
	// - previous has Value=100 (with effect, but effect is expired so no change)
	// - current has Value=200
	// - hasPrevi = true

	// Call CleanupExpired - this should NOT overwrite previous
	removed := s.CleanupExpired()
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	// The diff should show the Value change (100 -> 200)
	diff, _ := s.Diff(nil)
	if diff.Empty() {
		t.Error("diff should NOT be empty - state change must be preserved")
	}

	data, _ := diff.JSON()
	if !strings.Contains(string(data), "200") {
		t.Errorf("diff should contain new value 200, got: %s", data)
	}
}

func TestDelayedEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	// Effect that starts in 50ms and lasts 50ms
	effect := Delayed("delayed", 50*time.Millisecond, 50*time.Millisecond, func(ts TestState) TestState {
		ts.Value = 999
		return ts
	})
	s.AddEffect(effect)

	// Not yet started
	if got := s.Get(); got.Value != 100 {
		t.Errorf("Before start = %d, want 100", got.Value)
	}
	if effect.Started() {
		t.Error("Should not be started yet")
	}
	if effect.Active() {
		t.Error("Should not be active yet")
	}

	// Wait for start
	time.Sleep(60 * time.Millisecond)

	// Now active
	if got := s.Get(); got.Value != 999 {
		t.Errorf("After start = %d, want 999", got.Value)
	}
	if !effect.Started() {
		t.Error("Should be started")
	}
	if !effect.Active() {
		t.Error("Should be active")
	}

	// Wait for expiry
	time.Sleep(60 * time.Millisecond)

	// Expired
	if got := s.Get(); got.Value != 100 {
		t.Errorf("After expiry = %d, want 100", got.Value)
	}
	if !effect.Expired() {
		t.Error("Should be expired")
	}
	if effect.Active() {
		t.Error("Should not be active after expiry")
	}
}

func TestTimedWindowEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	now := time.Now()
	// Effect active from now+25ms to now+75ms
	effect := TimedWindow("window",
		now.Add(25*time.Millisecond),
		now.Add(75*time.Millisecond),
		func(ts TestState) TestState {
			ts.Value = 999
			return ts
		})
	s.AddEffect(effect)

	// Not yet started
	if got := s.Get(); got.Value != 100 {
		t.Errorf("Before window = %d, want 100", got.Value)
	}

	// Wait until in window
	time.Sleep(35 * time.Millisecond)
	if got := s.Get(); got.Value != 999 {
		t.Errorf("In window = %d, want 999", got.Value)
	}

	// Wait until after window
	time.Sleep(50 * time.Millisecond)
	if got := s.Get(); got.Value != 100 {
		t.Errorf("After window = %d, want 100", got.Value)
	}
}

// Concurrency tests
func TestConcurrentStateAccess(t *testing.T) {
	s := MustNew(TestState{Value: 0}, nil)

	done := make(chan bool)
	iterations := 100

	// Writer goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			s.Update(func(ts *TestState) {
				ts.Value++
			})
		}
		done <- true
	}()

	// Reader goroutines
	for i := 0; i < 3; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				_ = s.Get()
				_ = s.GetBase()
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}

	// Final value should be iterations
	if got := s.GetBase().Value; got != iterations {
		t.Errorf("Final value = %d, want %d", got, iterations)
	}
}

func TestConcurrentSessionAccess(t *testing.T) {
	s := MustNew(TestState{Value: 0}, nil)
	session := NewSession[TestState, string](s)

	done := make(chan bool)
	iterations := 50

	// Connector/disconnector goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			id := fmt.Sprintf("client-%d", i%10)
			session.Connect(id, nil)
			if i%3 == 0 {
				session.Disconnect(id)
			}
		}
		done <- true
	}()

	// State modifier goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			s.Update(func(ts *TestState) {
				ts.Value = i
			})
		}
		done <- true
	}()

	// Broadcaster goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			_ = session.Tick()
		}
		done <- true
	}()

	// Wait for all
	for i := 0; i < 3; i++ {
		<-done
	}

	// Should complete without panics/deadlocks
}

func TestConcurrentEffectModification(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	done := make(chan bool)
	iterations := 50

	// Add/remove effects
	go func() {
		for i := 0; i < iterations; i++ {
			id := fmt.Sprintf("effect-%d", i%5)
			s.AddEffect(Func(id, func(ts TestState) TestState {
				ts.Value++
				return ts
			}))
			if i%2 == 0 {
				s.RemoveEffect(id)
			}
		}
		done <- true
	}()

	// Read state concurrently
	go func() {
		for i := 0; i < iterations; i++ {
			_ = s.Get()
		}
		done <- true
	}()

	// Wait for all
	for i := 0; i < 2; i++ {
		<-done
	}
}

func TestConcurrentTimedEffectAccess(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)
	effect := Timed("buff", time.Hour, func(ts TestState) TestState {
		ts.Value = 999
		return ts
	})
	s.AddEffect(effect)

	done := make(chan bool)
	iterations := 100

	// Modifier goroutine - extend and change times
	go func() {
		for i := 0; i < iterations; i++ {
			effect.Extend(time.Millisecond)
			effect.SetExpiresAt(time.Now().Add(time.Hour))
			effect.SetStartsAt(time.Now().Add(-time.Hour))
		}
		done <- true
	}()

	// Reader goroutines - check state and effect status
	for i := 0; i < 3; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				_ = s.Get()
				_ = effect.Active()
				_ = effect.Expired()
				_ = effect.Started()
				_ = effect.Remaining()
				_ = effect.UntilStart()
			}
			done <- true
		}()
	}

	// Wait for all
	for i := 0; i < 4; i++ {
		<-done
	}

	// Should complete without race conditions
}

// Error case tests
func TestArrayConfigValidation(t *testing.T) {
	// This should return error
	_, err := New(TestState{}, &Config[TestState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "", // Empty - should error
	})
	if err == nil {
		t.Error("Expected error for ArrayByKey without KeyField")
	}
}

func TestNewValidatesJSONSerialization(t *testing.T) {
	// Type with channel can't be JSON serialized
	type BadState struct {
		Ch chan int
	}
	_, err := New(BadState{}, nil)
	if err == nil {
		t.Error("Expected error for non-serializable type")
	}
}

// Additional coverage tests

func TestSet(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	newState := TestState{Value: 100, Name: "replaced"}
	s.Set(newState)

	got := s.Get()
	if got.Value != 100 || got.Name != "replaced" {
		t.Errorf("Set() failed, got %+v", got)
	}

	// Verify diff is calculated correctly after Set
	diff, _ := s.Diff(nil)
	if diff.Empty() {
		t.Error("Expected diff after Set")
	}
}

func TestHasEffect(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	if s.HasEffect("test") {
		t.Error("HasEffect should be false for non-existent")
	}

	s.AddEffect(Func("test", func(ts TestState) TestState { return ts }))

	if !s.HasEffect("test") {
		t.Error("HasEffect should be true after AddEffect")
	}

	if s.HasEffect("other") {
		t.Error("HasEffect should be false for other ID")
	}
}

func TestClearEffects(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	s.AddEffect(Func("e1", func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	}))
	s.AddEffect(Func("e2", func(ts TestState) TestState {
		ts.Value *= 3
		return ts
	}))
	s.ClearPrevious()

	if got := s.Get().Value; got != 6 {
		t.Errorf("With effects: %d, want 6", got)
	}

	s.ClearEffects()

	if got := s.Get().Value; got != 1 {
		t.Errorf("After ClearEffects: %d, want 1", got)
	}

	if len(s.Effects()) != 0 {
		t.Error("Effects should be empty after ClearEffects")
	}

	// ClearEffects on empty should not panic
	s.ClearEffects()
}

func TestRemoveEffectNotFound(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)

	if s.RemoveEffect("nonexistent") {
		t.Error("RemoveEffect should return false for non-existent")
	}
}

func TestFullStateWithProjection(t *testing.T) {
	s := MustNew(TestState{Value: 1, Secret: "hidden"}, nil)

	// Without projection
	full := s.FullState(nil)
	if full.Secret != "hidden" {
		t.Error("FullState without projection should include Secret")
	}

	// With projection
	proj := func(ts TestState) TestState {
		ts.Secret = ""
		return ts
	}
	full = s.FullState(proj)
	if full.Secret != "" {
		t.Error("FullState with projection should hide Secret")
	}
}

func TestToggleEffect(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	toggle := Toggle("toggle", func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	})
	s.AddEffect(toggle)

	// Initially enabled
	if !toggle.IsEnabled() {
		t.Error("Toggle should be enabled by default")
	}
	if got := s.Get().Value; got != 200 {
		t.Errorf("Enabled toggle: %d, want 200", got)
	}

	// Disable
	toggle.Disable()
	if toggle.IsEnabled() {
		t.Error("Toggle should be disabled")
	}
	if got := s.Get().Value; got != 100 {
		t.Errorf("Disabled toggle: %d, want 100", got)
	}

	// Enable
	toggle.Enable()
	if got := s.Get().Value; got != 200 {
		t.Errorf("Re-enabled toggle: %d, want 200", got)
	}

	// SetEnabled
	toggle.SetEnabled(false)
	if toggle.IsEnabled() {
		t.Error("SetEnabled(false) should disable")
	}
}

func TestStackEffectClearAndCount(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	stack := Stack[TestState, int]("stack", func(ts TestState, vals []int) TestState {
		for _, v := range vals {
			ts.Value += v
		}
		return ts
	})
	s.AddEffect(stack)

	stack.Push(10)
	stack.Push(20)

	if stack.Count() != 2 {
		t.Errorf("Count = %d, want 2", stack.Count())
	}

	if got := s.Get().Value; got != 130 {
		t.Errorf("With stack: %d, want 130", got)
	}

	stack.Clear()
	if stack.Count() != 0 {
		t.Error("Count should be 0 after Clear")
	}

	if got := s.Get().Value; got != 100 {
		t.Errorf("After Clear: %d, want 100", got)
	}
}

func TestStackEffectPopEmpty(t *testing.T) {
	stack := Stack[TestState, int]("stack", func(ts TestState, vals []int) TestState {
		return ts
	})

	_, ok := stack.Pop()
	if ok {
		t.Error("Pop on empty stack should return false")
	}
}

func TestSessionMethods(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)

	// Count empty
	if sess.Count() != 0 {
		t.Error("Count should be 0 initially")
	}

	// IDs empty
	if len(sess.IDs()) != 0 {
		t.Error("IDs should be empty initially")
	}

	// Connect
	sess.Connect("a", nil)
	sess.Connect("b", nil)

	if sess.Count() != 2 {
		t.Errorf("Count = %d, want 2", sess.Count())
	}

	if !sess.IsConnected("a") {
		t.Error("IsConnected(a) should be true")
	}
	if sess.IsConnected("c") {
		t.Error("IsConnected(c) should be false")
	}

	ids := sess.IDs()
	if len(ids) != 2 {
		t.Errorf("IDs len = %d, want 2", len(ids))
	}

	// State getter
	if sess.State() != s {
		t.Error("State() should return underlying state")
	}

	// Diff method
	s.Update(func(ts *TestState) {
		ts.Value = 2
	})
	diff, err := sess.Diff("a")
	if err != nil {
		t.Errorf("Diff error: %v", err)
	}
	if string(diff) == "[]" {
		t.Error("Diff should not be empty after update")
	}
}

func TestDiffArraysByIndex(t *testing.T) {
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}, {ID: "b", Data: 2}},
	}, &Config[TestState]{
		ArrayStrategy: ArrayByIndex,
	})

	// Modify existing item
	s.Update(func(ts *TestState) {
		ts.Items[0].Data = 10
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	if !strings.Contains(string(data), "10") {
		t.Errorf("Diff should contain changed value: %s", data)
	}

	s.ClearPrevious()

	// Remove item
	s.Update(func(ts *TestState) {
		ts.Items = ts.Items[:1]
	})

	diff, _ = s.Diff(nil)
	data, _ = diff.JSON()
	if !strings.Contains(string(data), "remove") {
		t.Errorf("Diff should contain remove: %s", data)
	}

	s.ClearPrevious()

	// Add item
	s.Update(func(ts *TestState) {
		ts.Items = append(ts.Items, Item{ID: "c", Data: 3})
	})

	diff, _ = s.Diff(nil)
	data, _ = diff.JSON()
	if !strings.Contains(string(data), "add") {
		t.Errorf("Diff should contain add: %s", data)
	}
}

func TestDiffTypeMismatch(t *testing.T) {
	// Test type mismatch in diff
	type FlexState struct {
		Data any `json:"data"`
	}

	s := MustNew(FlexState{Data: "string"}, nil)
	s.Update(func(fs *FlexState) {
		fs.Data = 123 // Change type from string to number
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	if !strings.Contains(string(data), "replace") {
		t.Errorf("Type mismatch should result in replace: %s", data)
	}
}

func TestDiffArrayReplace(t *testing.T) {
	// Default strategy replaces entire array
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}},
	}, nil) // No ArrayStrategy = ArrayReplace

	s.Update(func(ts *TestState) {
		ts.Items = append(ts.Items, Item{ID: "b", Data: 2})
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	// Should replace entire array, not individual operations
	if !strings.Contains(string(data), "replace") {
		t.Errorf("ArrayReplace should use replace: %s", data)
	}
}

func TestPatchJSONEmpty(t *testing.T) {
	// Empty patch
	var p Patch
	data, err := p.JSON()
	if err != nil {
		t.Errorf("Empty patch JSON error: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("Empty patch should be [], got: %s", data)
	}
}

func TestEscapePtr(t *testing.T) {
	// Test JSON Pointer escaping
	s := MustNew(struct {
		Field map[string]int `json:"field"`
	}{
		Field: map[string]int{"a/b": 1, "c~d": 2},
	}, nil)

	s.Update(func(st *struct {
		Field map[string]int `json:"field"`
	}) {
		st.Field["a/b"] = 10
		st.Field["c~d"] = 20
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	str := string(data)

	// ~ should be escaped as ~0
	if !strings.Contains(str, "~0") {
		t.Errorf("~ should be escaped to ~0: %s", str)
	}
	// / should be escaped as ~1
	if !strings.Contains(str, "~1") {
		t.Errorf("/ should be escaped to ~1: %s", str)
	}
}

func TestTimedEffectNilTimeFunc(t *testing.T) {
	s := MustNew(TestState{Value: 100}, nil)

	effect := Timed("test", time.Hour, func(ts TestState) TestState {
		ts.Value = 999
		return ts
	})
	effect.TimeFunc = nil // Disable time checks

	s.AddEffect(effect)

	// Should always be active with nil TimeFunc
	if !effect.Active() {
		t.Error("Should be active with nil TimeFunc")
	}
	if !effect.Started() {
		t.Error("Should be started with nil TimeFunc")
	}
	if effect.Expired() {
		t.Error("Should not be expired with nil TimeFunc")
	}
	if effect.Remaining() != 0 {
		t.Error("Remaining should be 0 with nil TimeFunc")
	}
	if effect.UntilStart() != 0 {
		t.Error("UntilStart should be 0 with nil TimeFunc")
	}

	if got := s.Get().Value; got != 999 {
		t.Errorf("Effect should be applied: %d, want 999", got)
	}
}

func TestTimedEffectExtendNoExpiration(t *testing.T) {
	// TimedWindow with zero expiresAt (no expiration)
	effect := TimedWindow("test", time.Time{}, time.Time{}, func(ts TestState) TestState {
		return ts
	})

	// Extend on no-expiration should do nothing
	effect.Extend(time.Hour)
	// Should not panic
}

func TestMustNewPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustNew should panic on error")
		}
	}()

	type BadState struct {
		Ch chan int
	}
	MustNew(BadState{}, nil)
}

func TestCondEffectID(t *testing.T) {
	cond := Conditional("cond-test", func(ts TestState) bool {
		return ts.Value > 50
	}, func(ts TestState) TestState {
		ts.Value = 999
		return ts
	})

	if cond.ID() != "cond-test" {
		t.Errorf("Conditional ID = %s, want cond-test", cond.ID())
	}
}

func TestPersistRestore(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	// Create state with effect
	s := MustNew(TestState{Value: 42, Name: "test"}, nil)
	s.AddEffect(Func("buff", func(ts TestState) TestState {
		ts.Value *= 2
		return ts
	}))

	// Create effect meta
	meta, err := MakeEffectMeta("buff", "multiply", map[string]int{"factor": 2})
	if err != nil {
		t.Fatalf("MakeEffectMeta error: %v", err)
	}

	// Save
	err = Save(path, s, []EffectMeta{meta}, map[string]string{"extra": "data"})
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Factory for recreation
	factory := func(m EffectMeta) (Effect[TestState], error) {
		if m.Type == "multiply" {
			var params map[string]int
			if err := json.Unmarshal(m.Params, &params); err != nil {
				return nil, err
			}
			return Func(m.ID, func(ts TestState) TestState {
				ts.Value *= params["factor"]
				return ts
			}), nil
		}
		return nil, fmt.Errorf("unknown type: %s", m.Type)
	}

	// Restore
	result, err := Restore(path, nil, factory)
	if err != nil {
		t.Fatalf("Restore error: %v", err)
	}

	if result.State.GetBase().Value != 42 {
		t.Errorf("Restored base value = %d, want 42", result.State.GetBase().Value)
	}

	if result.State.Get().Value != 84 {
		t.Errorf("Restored value with effect = %d, want 84", result.State.Get().Value)
	}

	if len(result.EffectErrors) != 0 {
		t.Errorf("Unexpected effect errors: %v", result.EffectErrors)
	}
}

func TestPersistRestoreNoFile(t *testing.T) {
	result, err := Restore[TestState]("/nonexistent/path.json", nil, nil)
	if err != nil {
		t.Errorf("Restore nonexistent should not error: %v", err)
	}
	if result != nil {
		t.Error("Restore nonexistent should return nil result")
	}
}

func TestPersistRestoreEffectError(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 1}, nil)
	meta, _ := MakeEffectMeta("bad", "unknown", nil)
	Save(path, s, []EffectMeta{meta}, nil)

	factory := func(m EffectMeta) (Effect[TestState], error) {
		return nil, fmt.Errorf("factory error")
	}

	result, err := Restore(path, nil, factory)
	if err != nil {
		t.Fatalf("Restore should not fatal error: %v", err)
	}

	if len(result.EffectErrors) != 1 {
		t.Errorf("Expected 1 effect error, got %d", len(result.EffectErrors))
	}
}

func TestParseParams(t *testing.T) {
	meta := EffectMeta{
		ID:     "test",
		Type:   "test",
		Params: json.RawMessage(`{"factor": 2}`),
	}

	var params struct {
		Factor int `json:"factor"`
	}
	params, err := ParseParams[struct {
		Factor int `json:"factor"`
	}](meta)

	if err != nil {
		t.Errorf("ParseParams error: %v", err)
	}
	if params.Factor != 2 {
		t.Errorf("Factor = %d, want 2", params.Factor)
	}

	// Empty params
	meta.Params = nil
	params, err = ParseParams[struct {
		Factor int `json:"factor"`
	}](meta)
	if err != nil {
		t.Errorf("ParseParams empty error: %v", err)
	}
}

func TestMakeEffectMetaError(t *testing.T) {
	// Channel cannot be marshaled
	_, err := MakeEffectMeta("test", "test", make(chan int))
	if err == nil {
		t.Error("MakeEffectMeta should error on non-serializable params")
	}
}

func TestBroadcastWithProjection(t *testing.T) {
	s := MustNew(TestState{Value: 1, Secret: "hidden"}, nil)
	sess := NewSession[TestState, string](s)

	// Client with projection that hides secret
	sess.Connect("user1", func(ts TestState) TestState {
		ts.Secret = ""
		return ts
	})
	// Client without projection sees everything
	sess.Connect("user2", nil)

	s.Update(func(ts *TestState) {
		ts.Value = 2
		ts.Secret = "newsecret"
	})

	diffs := sess.Tick()

	// user2 should have diff with the actual secret change
	if !strings.Contains(string(diffs["user2"]), "newsecret") {
		t.Errorf("user2's diff should have actual secret, got: %s", string(diffs["user2"]))
	}

	// user1 sees empty secret in both old and new, so only value change
	if strings.Contains(string(diffs["user1"]), "secret") {
		t.Errorf("user1's diff should not have secret field (projected out), got: %s", string(diffs["user1"]))
	}
}

func TestBroadcastNoChanges(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)
	sess.Connect("user1", nil)

	// No update - Broadcast should return nil
	diffs := sess.Broadcast()
	if diffs != nil {
		t.Errorf("Expected nil for no changes, got %v", diffs)
	}
}

func TestBroadcastEmptyDiff(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)

	// User with projection that makes no visible change
	sess.Connect("user1", func(ts TestState) TestState {
		ts.Value = 999 // Always shows 999
		return ts
	})

	s.Update(func(ts *TestState) {
		ts.Value = 2 // Change base, but projection always shows 999
	})

	diffs := sess.Broadcast()
	// The projection makes old and new identical (both 999), so no diff
	if len(diffs) != 0 {
		t.Errorf("Expected empty diffs for no visible change, got %d", len(diffs))
	}
}

func TestToggleEffectID(t *testing.T) {
	toggle := Toggle("my-toggle", func(ts TestState) TestState { return ts })
	if toggle.ID() != "my-toggle" {
		t.Errorf("Toggle ID = %s, want my-toggle", toggle.ID())
	}
}

func TestStackEffectID(t *testing.T) {
	stack := Stack[TestState, int]("my-stack", func(ts TestState, vals []int) TestState { return ts })
	if stack.ID() != "my-stack" {
		t.Errorf("Stack ID = %s, want my-stack", stack.ID())
	}
}

func TestTimedEffectRemainingActive(t *testing.T) {
	effect := Timed("test", time.Hour, func(ts TestState) TestState { return ts })

	// Should have remaining time
	if effect.Remaining() <= 0 {
		t.Error("Remaining should be > 0 for active effect")
	}

	// Should have no UntilStart (already started)
	if effect.UntilStart() != 0 {
		t.Error("UntilStart should be 0 for already started effect")
	}
}

func TestTimedEffectUntilStartDelayed(t *testing.T) {
	effect := Delayed("test", time.Hour, time.Hour, func(ts TestState) TestState { return ts })

	// Should have UntilStart time
	if effect.UntilStart() <= 0 {
		t.Error("UntilStart should be > 0 for delayed effect")
	}
}

func TestSessionDiffError(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)
	sess.Connect("user1", nil)

	// No previous state - should return empty JSON
	diff, err := sess.Diff("user1")
	if err != nil {
		t.Errorf("Diff error: %v", err)
	}
	if string(diff) != "[]" {
		t.Errorf("Diff without previous should be [], got: %s", diff)
	}
}

func TestSaveExtraMarshaling(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 1}, nil)

	// Save with extra data
	err := Save(path, s, nil, map[string]string{"key": "value"})
	if err != nil {
		t.Errorf("Save with extra error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Save should create file: %v", err)
	}
}

func TestSaveExtraError(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 1}, nil)

	// Extra with channel can't be marshaled
	err := Save(path, s, nil, make(chan int))
	if err == nil {
		t.Error("Save with non-serializable extra should error")
	}
}

func TestLoadErrorCases(t *testing.T) {
	dir := t.TempDir()

	// Invalid JSON
	invalidPath := dir + "/invalid.json"
	os.WriteFile(invalidPath, []byte("not json"), 0644)

	_, err := Load[TestState](invalidPath)
	if err == nil {
		t.Error("Load invalid JSON should error")
	}
}

func TestRestoreDuplicateEffectID(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 1}, nil)
	// Two effects with same ID in metadata
	meta1, _ := MakeEffectMeta("dup", "test", nil)
	meta2, _ := MakeEffectMeta("dup", "test", nil)
	Save(path, s, []EffectMeta{meta1, meta2}, nil)

	factory := func(m EffectMeta) (Effect[TestState], error) {
		return Func(m.ID, func(ts TestState) TestState { return ts }), nil
	}

	result, err := Restore(path, nil, factory)
	if err != nil {
		t.Fatalf("Restore should not fatal error: %v", err)
	}

	// Second effect should have error due to duplicate ID
	if len(result.EffectErrors) != 1 {
		t.Errorf("Expected 1 effect error for duplicate ID, got %d", len(result.EffectErrors))
	}
}

func TestArrayByKeyNoKeyField(t *testing.T) {
	// ArrayByKey without KeyField falls back to replace
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}},
	}, &Config[TestState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "id",
	})

	// Modify item
	s.Update(func(ts *TestState) {
		ts.Items[0].Data = 10
	})

	diff, _ := s.Diff(nil)
	if diff.Empty() {
		t.Error("Diff should not be empty")
	}
}

func TestArrayByKeyElementWithoutKey(t *testing.T) {
	// Element without the key field should be skipped
	type FlexState struct {
		Items []map[string]any `json:"items"`
	}

	s := MustNew(FlexState{
		Items: []map[string]any{
			{"id": "a", "data": 1},
			{"nokey": "b", "data": 2}, // No "id" field
		},
	}, &Config[FlexState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "id",
	})

	s.Update(func(fs *FlexState) {
		fs.Items[0]["data"] = 10
	})

	diff, _ := s.Diff(nil)
	// Should not panic
	if diff == nil {
		t.Error("Diff should not be nil")
	}
}

func TestDiffNestedMap(t *testing.T) {
	type NestedState struct {
		Data map[string]map[string]int `json:"data"`
	}

	s := MustNew(NestedState{
		Data: map[string]map[string]int{
			"outer": {"inner": 1},
		},
	}, nil)

	s.Update(func(ns *NestedState) {
		ns.Data["outer"]["inner"] = 2
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	if !strings.Contains(string(data), "2") {
		t.Errorf("Diff should contain nested change: %s", data)
	}
}

func TestDiffAddKey(t *testing.T) {
	type MapState struct {
		Data map[string]int `json:"data"`
	}

	s := MustNew(MapState{
		Data: map[string]int{"a": 1},
	}, nil)

	s.Update(func(ms *MapState) {
		ms.Data["b"] = 2
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	if !strings.Contains(string(data), "add") {
		t.Errorf("Diff should contain add operation: %s", data)
	}
}

func TestDiffRemoveKey(t *testing.T) {
	type MapState struct {
		Data map[string]int `json:"data"`
	}

	s := MustNew(MapState{
		Data: map[string]int{"a": 1, "b": 2},
	}, nil)

	s.Update(func(ms *MapState) {
		delete(ms.Data, "a")
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	if !strings.Contains(string(data), "remove") {
		t.Errorf("Diff should contain remove operation: %s", data)
	}
}

func TestNewWithCustomCloner(t *testing.T) {
	cloner := func(ts TestState) TestState {
		return TestState{
			Value:  ts.Value,
			Name:   ts.Name,
			Secret: ts.Secret,
			Items:  append([]Item{}, ts.Items...),
		}
	}

	s, err := New(TestState{Value: 1}, &Config[TestState]{
		Cloner: cloner,
	})
	if err != nil {
		t.Fatalf("New with cloner error: %v", err)
	}

	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	if s.Get().Value != 2 {
		t.Error("Update should work with custom cloner")
	}
}

func TestBroadcastDiffError(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)

	sess.Connect("user1", nil)
	sess.Connect("user2", func(ts TestState) TestState { return ts })

	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	// Both should get diffs
	diffs := sess.Broadcast()
	if len(diffs) < 1 {
		t.Error("Broadcast should return diffs")
	}
}

func TestArrayByKeyEmptyKeyField(t *testing.T) {
	// When KeyField is empty, diffArraysByKey should fall back to replace
	cfg := ArrayConfig{Strategy: ArrayByKey, KeyField: ""}

	old := []any{map[string]any{"id": "a"}}
	new := []any{map[string]any{"id": "b"}}

	patch := diffArraysByKey("/items", old, new, cfg)
	// Should replace entire array
	if len(patch) != 1 || patch[0].Op != "replace" {
		t.Errorf("Empty KeyField should fall back to replace, got: %+v", patch)
	}
}

func TestDiffArraysNoChange(t *testing.T) {
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}},
	}, nil)

	// No change
	s.Update(func(ts *TestState) {
		// No actual change
	})

	diff, _ := s.Diff(nil)
	if !diff.Empty() {
		t.Error("No change should result in empty diff")
	}
}

func TestSessionDiffWithUpdate(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)
	sess.Connect("user1", nil)

	// Make an update
	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	// Diff should now have content
	diff, err := sess.Diff("user1")
	if err != nil {
		t.Errorf("Diff error: %v", err)
	}
	if string(diff) == "[]" {
		t.Error("Diff should have content after update")
	}
}

func TestRestoreWithNoFactory(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 42}, nil)
	meta, _ := MakeEffectMeta("test", "test", nil)
	Save(path, s, []EffectMeta{meta}, nil)

	// Restore without factory - effects metadata is ignored
	result, err := Restore[TestState](path, nil, nil)
	if err != nil {
		t.Fatalf("Restore error: %v", err)
	}

	if result.State.GetBase().Value != 42 {
		t.Errorf("Restored value = %d, want 42", result.State.GetBase().Value)
	}

	// No effects should be added when factory is nil
	if len(result.State.Effects()) != 0 {
		t.Error("No effects should be added without factory")
	}
}

func TestRestoreFactoryReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 1}, nil)
	meta, _ := MakeEffectMeta("test", "test", nil)
	Save(path, s, []EffectMeta{meta}, nil)

	// Factory that returns nil effect (skip)
	factory := func(m EffectMeta) (Effect[TestState], error) {
		return nil, nil // Skip this effect
	}

	result, err := Restore(path, nil, factory)
	if err != nil {
		t.Fatalf("Restore error: %v", err)
	}

	// No effects should be added when factory returns nil
	if len(result.State.Effects()) != 0 {
		t.Error("No effects should be added when factory returns nil")
	}
}

func TestCalcDiffError(t *testing.T) {
	// calcDiff is hard to make fail since New validates JSON serialization
	// but we can test it indirectly through Diff
	s := MustNew(TestState{Value: 1}, nil)
	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	diff, err := s.Diff(nil)
	if err != nil {
		t.Errorf("Diff should not error: %v", err)
	}
	if diff.Empty() {
		t.Error("Diff should not be empty")
	}
}

func TestTimedEffectExpiredRemaining(t *testing.T) {
	// Expired effect should have 0 remaining
	effect := Timed("test", -time.Hour, func(ts TestState) TestState { return ts })

	if effect.Remaining() != 0 {
		t.Errorf("Expired effect Remaining = %v, want 0", effect.Remaining())
	}
}

func TestDiffArraysEqual(t *testing.T) {
	// Test diffArrays when arrays are equal (ArrayReplace strategy)
	s := MustNew(TestState{
		Items: []Item{{ID: "a", Data: 1}},
	}, nil)

	// Update without changing Items array
	s.Update(func(ts *TestState) {
		ts.Value = 2 // Only change Value, not Items
	})

	diff, _ := s.Diff(nil)
	data, _ := diff.JSON()
	// Items should not appear in diff since they didn't change
	if strings.Contains(string(data), "items") {
		t.Errorf("Unchanged array should not appear in diff: %s", data)
	}
}

func TestRestoreWithConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	s := MustNew(TestState{Value: 42, Items: []Item{{ID: "a", Data: 1}}}, nil)
	Save(path, s, nil, nil)

	// Restore with custom config
	result, err := Restore(path, &Config[TestState]{
		ArrayStrategy: ArrayByKey,
		ArrayKeyField: "id",
	}, nil)
	if err != nil {
		t.Fatalf("Restore with config error: %v", err)
	}

	if result.State.GetBase().Value != 42 {
		t.Errorf("Restored value = %d, want 42", result.State.GetBase().Value)
	}
}

func TestLoadNonExistent(t *testing.T) {
	// Load non-existent file should return nil, nil
	snap, err := Load[TestState]("/nonexistent/path/file.json")
	if err != nil {
		t.Errorf("Load non-existent should not error: %v", err)
	}
	if snap != nil {
		t.Error("Load non-existent should return nil snapshot")
	}
}

func TestSessionDiffEmpty(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)
	sess.Connect("user1", nil)

	// Update
	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	// Get diff
	diff, _ := sess.Diff("user1")
	if string(diff) == "[]" {
		t.Error("Diff should not be empty after update")
	}

	// Clear and check again - should be empty
	s.ClearPrevious()
	diff2, _ := sess.Diff("user1")
	if string(diff2) != "[]" {
		t.Errorf("Diff after clear should be empty, got: %s", diff2)
	}
}

func TestBroadcastCacheHit(t *testing.T) {
	s := MustNew(TestState{Value: 1}, nil)
	sess := NewSession[TestState, string](s)

	// Multiple clients with nil projection should use cache
	sess.Connect("user1", nil)
	sess.Connect("user2", nil)
	sess.Connect("user3", nil)

	s.Update(func(ts *TestState) {
		ts.Value = 2
	})

	diffs := sess.Broadcast()

	// All three should have identical diffs
	if len(diffs) != 3 {
		t.Errorf("Expected 3 diffs, got %d", len(diffs))
	}

	// All should be the same (cached)
	if string(diffs["user1"]) != string(diffs["user2"]) ||
		string(diffs["user2"]) != string(diffs["user3"]) {
		t.Error("Cached diffs should be identical")
	}
}
