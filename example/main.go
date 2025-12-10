package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mxkacsa/statediff"
)

// Game state
type Game struct {
	Round   int      `json:"round"`
	Phase   string   `json:"phase"`
	Players []Player `json:"players"`
}

type Player struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Score int    `json:"score"`
	Hand  []int  `json:"hand,omitempty"` // Hidden from others
}

// Clone for performance (optional but ~40x faster)
func (g Game) Clone() Game {
	c := Game{Round: g.Round, Phase: g.Phase, Players: make([]Player, len(g.Players))}
	for i, p := range g.Players {
		c.Players[i] = Player{ID: p.ID, Name: p.Name, Score: p.Score, Hand: append([]int{}, p.Hand...)}
	}
	return c
}

// Activator type - using *string so nil means "system activated"
type Activator = *string

func strPtr(s string) *string { return &s }

func main() {
	fmt.Println("=== StateDiff Demo ===")
	fmt.Println()

	// Create state with custom cloner
	// Using *string as activator type - nil = system, *string = player ID
	state, err := statediff.New[Game, Activator](Game{
		Round: 1,
		Phase: "lobby",
		Players: []Player{
			{ID: "alice", Name: "Alice", Score: 0, Hand: []int{1, 2, 3}},
			{ID: "bob", Name: "Bob", Score: 0, Hand: []int{4, 5, 6}},
		},
	}, &statediff.Config[Game]{
		Cloner: func(g Game) Game { return g.Clone() },
	})
	if err != nil {
		panic(err)
	}

	// Create session manager
	session := statediff.NewSession[Game, Activator, string](state)

	// Players connect with their projection (hides other hands)
	for _, id := range []string{"alice", "bob"} {
		pid := id // capture
		session.Connect(id, func(g Game) Game {
			result := g.Clone()
			for i := range result.Players {
				if result.Players[i].ID != pid {
					result.Players[i].Hand = nil
				}
			}
			return result
		})
	}

	// Initial sync - full state
	fmt.Println("--- Initial sync ---")
	for _, id := range []string{"alice", "bob"} {
		data, _ := session.Full(id)
		fmt.Printf("%s: %s\n", id, truncate(string(data)))
	}

	// Game starts
	fmt.Println("\n--- Game starts ---")
	state.Update(func(g *Game) {
		g.Phase = "playing"
		g.Players[0].Score = 100
	})

	// Broadcast diffs
	for id, data := range session.Tick() {
		fmt.Printf("%s: %s\n", id, truncate(string(data)))
	}

	// Add timed effect (double score for 5 seconds) - activated by "alice"
	// The activator is immune to the effect (only affects others)
	fmt.Println("\n--- Double score effect (5s) - Alice activates, immune to self ---")
	doubleScore := statediff.Timed[Game, Activator]("double", 5*time.Second, func(g Game, activator Activator) Game {
		var activatorID string
		if activator != nil {
			activatorID = *activator
		}
		for i := range g.Players {
			// Skip the activator - they are immune to their own effect
			if g.Players[i].ID == activatorID {
				continue
			}
			g.Players[i].Score *= 2
		}
		return g
	})
	state.AddEffect(doubleScore, strPtr("alice")) // Alice activates it

	for id, data := range session.Tick() {
		fmt.Printf("%s: %s\n", id, truncate(string(data)))
	}

	// Check current values
	fmt.Println("\n--- Current state (with effect) ---")
	current := state.Get()
	fmt.Printf("Alice score: %d (activator, immune - stays 100)\n", current.Players[0].Score)
	fmt.Printf("Bob score: %d (affected by 2x effect)\n", current.Players[1].Score)

	// Remove effect
	fmt.Println("\n--- Remove effect ---")
	state.RemoveEffect("double")
	for id, data := range session.Tick() {
		fmt.Printf("%s: %s\n", id, truncate(string(data)))
	}

	// Stacking multipliers - system activated (nil activator)
	fmt.Println("\n--- Stacking multipliers (system effect) ---")
	multipliers := statediff.Stack[Game, Activator, float64]("mult", func(g Game, mults []float64, activator Activator) Game {
		total := 1.0
		for _, m := range mults {
			total *= m
		}
		for i := range g.Players {
			g.Players[i].Score = int(float64(g.Players[i].Score) * total)
		}
		return g
	})
	state.AddEffect(multipliers, nil) // System activated - affects everyone
	session.Tick()                    // Clear

	multipliers.Push(1.5)
	state.Update(func(g *Game) {}) // Trigger change detection
	fmt.Printf("With 1.5x: Alice=%d\n", state.Get().Players[0].Score)

	multipliers.Push(2.0)
	state.Update(func(g *Game) {})
	fmt.Printf("With 1.5x * 2x: Alice=%d\n", state.Get().Players[0].Score)

	multipliers.Pop()
	state.Update(func(g *Game) {})
	fmt.Printf("After pop: Alice=%d\n", state.Get().Players[0].Score)

	// Save for restart
	fmt.Println("\n--- Save state ---")
	effectMeta, _ := statediff.MakeEffectMeta("mult", "multiplier", nil)
	effects := []statediff.EffectMeta{effectMeta}
	savePath := filepath.Join(os.TempDir(), "game.json")
	err = statediff.Save(savePath, state, effects, nil)
	if err != nil {
		fmt.Printf("Save error: %v\n", err)
	} else {
		fmt.Printf("Saved to %s\n", savePath)
	}

	// Load (simulating restart)
	fmt.Println("\n--- Load state ---")
	snap, _ := statediff.Load[Game](savePath)
	if snap != nil {
		fmt.Printf("Loaded: Round=%d, Phase=%s, SavedAt=%s\n",
			snap.State.Round, snap.State.Phase, snap.SavedAt.Format(time.RFC3339))
	}

	fmt.Println("\n=== Done ===")
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// prettyJSON formats JSON for display
func prettyJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}
