# statediff

[![Go](https://github.com/mxkacsa/statediff/actions/workflows/ci.yml/badge.svg)](https://github.com/mxkacsa/statediff/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/mxkacsa/statediff/graph/badge.svg)](https://codecov.io/gh/mxkacsa/statediff)
[![Go Reference](https://pkg.go.dev/badge/github.com/mxkacsa/statediff.svg)](https://pkg.go.dev/github.com/mxkacsa/statediff)

Deterministic state synchronization for Go. Automatic JSON diff generation, reversible effects, player-specific projections.

```
┌────────────────────────────────────────────────────────────────────────────┐
│                              SERVER                                        │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                         State[T]                                    │   │
│  │  ┌──────────────┐    ┌──────────────┐    ┌──────────────────────┐   │   │
│  │  │  Base State  │──▶│   Effects    │──▶ │  Effective State     │   │   │
│  │  │  {round: 5}  │    │  [buff x2]   │    │  {round: 5, hp: 200} │   │   │
│  │  └──────────────┘    └──────────────┘    └──────────────────────┘   │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                    │                                       │
│                                    ▼                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                      Session[T, ID]                                 │   │
│  │                                                                     │   │
│  │   ┌─────────────┐      ┌─────────────┐      ┌─────────────┐         │   │
│  │   │   Alice     │      │    Bob      │      │   Admin     │         │   │
│  │   │ projection  │      │ projection  │      │ (no proj)   │         │   │
│  │   │ hides other │      │ hides other │      │ sees all    │         │   │
│  │   │   hands     │      │   hands     │      │             │         │   │
│  │   └──────┬──────┘      └──────┬──────┘      └──────┬──────┘         │   │
│  └──────────│─────────────────────│─────────────────────│──────────────┘   │
│             │                     │                     │                  │
│             ▼                     ▼                     ▼                  │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐          │
│  │   JSON Patch     │  │   JSON Patch     │  │   JSON Patch     │          │
│  │ [{op:"replace",  │  │ [{op:"replace",  │  │ [{op:"replace",  │          │
│  │   path:"/hp",    │  │   path:"/hp",    │  │   path:"/hp",    │          │
│  │   value:200}]    │  │   value:200}]    │  │   value:200},    │          │
│  │                  │  │                  │  │  {op:"replace",  │          │
│  │ (cards hidden)   │  │ (cards hidden)   │  │   path:"/cards", │          │
│  │                  │  │                  │  │   value:[...]}]  │          │
│  └────────┬─────────┘  └────────┬─────────┘  └────────┬─────────┘          │
└───────────│─────────────────────│─────────────────────│────────────────────┘
            │                     │                     │
            ▼                     ▼                     ▼
┌───────────────────┐  ┌───────────────────┐  ┌───────────────────┐
│  Alice's Client   │  │   Bob's Client    │  │  Admin's Client   │
│  ┌─────────────┐  │  │  ┌─────────────┐  │  │  ┌─────────────┐  │
│  │ Local State │  │  │  │ Local State │  │  │  │ Local State │  │
│  │ apply patch │  │  │  │ apply patch │  │  │  │ apply patch │  │
│  └─────────────┘  │  │  └─────────────┘  │  │  └─────────────┘  │
└───────────────────┘  └───────────────────┘  └───────────────────┘
```

## Features

- **Deterministic** - Same inputs, same outputs. Always.
- **Minimal memory** - One previous state, not per-session
- **Reversible effects** - Add/remove without mutating base state
- **Projections** - Each viewer sees filtered data
- **Fast** - Custom cloner = ~90x speedup
- **Code generation** - `clonegen` tool generates `Clone()` methods

## Install

```bash
go get github.com/mxkacsa/statediff
```

## Quick Start

```go
// Create state
state := statediff.New(GameState{Round: 1}, nil)

// Session manager
session := statediff.NewSession[GameState, string](state)

// Player connects with projection (hides other hands)
session.Connect("alice", func(g GameState) GameState {
    for i := range g.Players {
        if g.Players[i].ID != "alice" {
            g.Players[i].Hand = nil
        }
    }
    return g
})

// Initial sync - full state
data, _ := session.Full("alice")
ws.Send(data)

// Transaction-based updates (recommended)
// Batches multiple updates and automatically broadcasts
for playerID, diff := range session.Transaction(func(tx *statediff.Tx[GameState, string]) {
    tx.Update(func(g *GameState) { g.Round++ })
    tx.Update(func(g *GameState) { g.Phase = "draw" })
}) {
    ws.Send(playerID, diff)
}

// Single update shorthand
for playerID, diff := range session.ApplyUpdate(func(g *GameState) {
    g.Round++
}) {
    ws.Send(playerID, diff)
}
```

## Clone Generator

Generate `Clone()` methods automatically for ~90x faster performance:

```bash
# Install the generator
go install github.com/mxkacsa/statediff/cmd/clonegen@latest
```

Add to your code:

```go
//go:generate clonegen -type=GameState,Player

type GameState struct {
    Round   int
    Phase   string
    Players []Player
}

type Player struct {
    ID    string
    Name  string
    Hand  []int
}
```

Run:

```bash
go generate ./...
```

This creates `Clone()` methods that handle slices, maps, pointers, and nested structs.

See [cmd/clonegen/README.md](cmd/clonegen/README.md) for full documentation.

## API

### State

```go
// New returns error if config invalid or type not serializable
state, err := statediff.New(initial, &statediff.Config[T]{
    Cloner: func(t T) T { return t.Clone() },  // Optional, ~90x faster
})

// MustNew panics on error (for tests/init)
state := statediff.MustNew(initial, nil)

state.Get()                    // Current state with effects
state.GetBase()                // Without effects
state.Update(func(s *T) {...}) // Modify (saves previous for diff)
state.Set(newState)            // Replace

state.Diff(projection)         // Diff since last change
state.FullState(projection)    // Complete state
state.ClearPrevious()          // Clear after broadcasting
state.HasChanges()             // Check if there are pending changes
```

### Session

```go
session := statediff.NewSession[T, string](state)

session.Connect(id, projection) // Register client
session.Disconnect(id)          // Remove client
session.Full(id)                // Full state JSON
session.Diff(id)                // Diff JSON
session.Tick()                  // Broadcast + clear
session.Count()                 // Connected clients count
session.IDs()                   // List of connected client IDs

// Transaction-based API (recommended)
session.Transaction(func(tx *Tx[T, A]) {
    tx.Update(func(s *T) {...})  // Batch multiple updates
    tx.Set(newState)              // Replace entire state
    tx.Get()                      // Read current state (with effects)
    tx.GetBase()                  // Read base state (without effects)
})                                // Returns diffs automatically

// Shorthand for single updates
session.ApplyUpdate(func(s *T) {...}) // Update + broadcast in one call
```

### Effects

```go
// Simple - returns error if ID already exists
err := state.AddEffect(statediff.Func("id", func(s T) T { return s }))

// Timed (auto-expires after duration)
state.AddEffect(statediff.Timed("buff", 30*time.Second, fn))

// Timed with window (active between start and end times)
state.AddEffect(statediff.TimedWindow("event", startTime, endTime, fn))

// Delayed (activates after delay, lasts for duration)
state.AddEffect(statediff.Delayed("powerup", 5*time.Second, 30*time.Second, fn))

// TimedEffect methods
timed := statediff.Timed("buff", 30*time.Second, fn)
timed.Active()                  // Currently active (started and not expired)
timed.Started()                 // Has started
timed.Expired()                 // Has expired
timed.Remaining()               // Duration until expiration
timed.UntilStart()              // Duration until activation
timed.Extend(10*time.Second)    // Extend expiration
timed.SetStartsAt(time.Now())   // Change start time
timed.SetExpiresAt(time.Now())  // Change expiration

// TimedEffect time source (defaults to time.Now)
timed.TimeFunc = nil            // Disable time checks (always active)
timed.TimeFunc = myGameTime     // Custom time source for testing/replay

// Conditional (applies when condition true)
state.AddEffect(statediff.Conditional("phase", condition, fn))

// Toggle (enable/disable)
toggle := statediff.Toggle("debug", fn)
toggle.Disable()
toggle.Enable()
toggle.SetEnabled(true)
toggle.IsEnabled()

// Stack (accumulate values)
mult := statediff.Stack[T, float64]("mult", combineFn)
mult.Push(1.5)
mult.Push(2.0)
mult.Pop()
mult.Count()
mult.Clear()

// Query effects
state.HasEffect("id")           // Check if effect exists
state.GetEffect("id")           // Get effect by ID
state.Effects()                 // List all effects

// Remove
state.RemoveEffect("id")
state.ClearEffects()            // Remove all
state.CleanupExpired()          // Remove expired TimedEffects
```

### Persistence

```go
// Save
statediff.Save("/path/state.json", state, effectMeta, extra)

// Load
snap, _ := statediff.Load[T]("/path/state.json")

// Restore with effect recreation
state, _ := statediff.Restore("/path", config, effectFactory)
```

## Frontend

```javascript
import { applyPatch } from 'fast-json-patch';

let state = {};

ws.onmessage = (msg) => {
    const patches = JSON.parse(msg.data);
    state = applyPatch(state, patches).newDocument;
};
```

## Thread Safety

All types are safe for concurrent access from multiple goroutines.

### State
- All methods use `sync.RWMutex` internally
- `Get()`, `GetBase()`, `Diff()`, `FullState()`, `HasChanges()`, `HasEffect()`, `GetEffect()`, `Effects()` use read locks
- `Update()`, `Set()`, `AddEffect()`, `RemoveEffect()`, `ClearEffects()`, `ClearPrevious()`, `CleanupExpired()` use write locks

### Session
- Client map protected by `sync.RWMutex`
- `Full()` and `Diff()` hold read locks during state access to prevent races
- Safe to call `Connect()`, `Disconnect()`, `Tick()` concurrently

### Effects
- `ToggleEffect`: Internal mutex protects enabled state, safe to call `Enable()`, `Disable()`, `IsEnabled()` concurrently
- `StackEffect`: Internal mutex protects values slice, safe to call `Push()`, `Pop()`, `Count()`, `Clear()` concurrently
- `TimedEffect`: Internal mutex protects time fields, all methods (`Apply()`, `Active()`, `Expired()`, `Extend()`, `SetStartsAt()`, `SetExpiresAt()`) are thread-safe

### Typical Usage Pattern

```go
// Game server with multiple goroutines
state, _ := statediff.New(GameState{}, nil)
session := statediff.NewSession[GameState, string](state)

// Goroutine 1: Handle player connections
go func() {
    for conn := range connections {
        session.Connect(conn.ID, makeProjection(conn.ID))
    }
}()

// Goroutine 2: Game loop with Transaction (recommended)
// Transaction ensures updates are batched and broadcast together
go func() {
    ticker := time.NewTicker(50 * time.Millisecond)
    for range ticker.C {
        for id, diff := range session.Transaction(func(tx *statediff.Tx[GameState, string]) {
            tx.Update(func(g *GameState) {
                // Update game logic - all updates batched
                g.Tick++
            })
        }) {
            send(id, diff)
        }
    }
}()

// Goroutine 3: Handle player actions
// ApplyUpdate for simple single-update actions
go func() {
    for action := range actions {
        for id, diff := range session.ApplyUpdate(func(g *GameState) {
            // Process action - automatically broadcast
        }) {
            send(id, diff)
        }
    }
}()
```

## Performance

| Operation | JSON Clone | Custom Clone |
|-----------|------------|--------------|
| Diff cycle | 350μs | 4μs |

Use `clonegen` or implement `Clone()` manually:

```go
func (g GameState) Clone() GameState {
    c := GameState{Round: g.Round, Phase: g.Phase}
    c.Players = make([]Player, len(g.Players))
    copy(c.Players, g.Players)
    return c
}

state := statediff.New(initial, &statediff.Config[GameState]{
    Cloner: func(g GameState) GameState { return g.Clone() },
})
```

## Files

```
state.go           - Core state management
diff.go            - JSON diff calculation
effect.go          - Effect types
session.go         - Multi-client management
persist.go         - Save/load
cmd/clonegen/      - Clone() code generator
```

## License

MIT
