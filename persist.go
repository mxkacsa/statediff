package statediff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Snapshot represents saved state that can be restored
type Snapshot[T any] struct {
	Version int             `json:"version"` // Schema version for forward compatibility
	State   T               `json:"state"`
	Effects []EffectMeta    `json:"effects,omitempty"`
	SavedAt time.Time       `json:"savedAt"`
	Extra   json.RawMessage `json:"extra,omitempty"`
}

// Current snapshot version
const SnapshotVersion = 1

// RestoreResult contains the restored state and any non-fatal errors
type RestoreResult[T, A any] struct {
	State        *State[T, A]
	EffectErrors []error // Errors from effect factory (non-fatal)
}

// EffectMeta stores effect info for recreation
type EffectMeta struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params,omitempty"`
}

// EffectFactory recreates effects from metadata.
// T is the state type, A is the activator type.
type EffectFactory[T, A any] func(meta EffectMeta) (Effect[T, A], error)

// Save writes state to a JSON file (atomic write)
func Save[T, A any](path string, state *State[T, A], effects []EffectMeta, extra any) error {
	var extraJSON json.RawMessage
	if extra != nil {
		var err error
		extraJSON, err = json.Marshal(extra)
		if err != nil {
			return fmt.Errorf("marshal extra: %w", err)
		}
	}

	snap := Snapshot[T]{
		Version: SnapshotVersion,
		State:   state.GetBase(),
		Effects: effects,
		SavedAt: time.Now(),
		Extra:   extraJSON,
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Atomic write: temp file + rename
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// Load reads state from a JSON file
func Load[T any](path string) (*Snapshot[T], error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No saved state
		}
		return nil, fmt.Errorf("read: %w", err)
	}

	var snap Snapshot[T]
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	return &snap, nil
}

// Restore loads state and recreates effects.
// Returns RestoreResult which includes both the state and any effect recreation errors.
// Effect errors are non-fatal - the state is still returned with successfully recreated effects.
// Note: Restored effects have zero-value activator - set them after restore if needed.
func Restore[T, A any](path string, cfg *Config[T], factory EffectFactory[T, A]) (*RestoreResult[T, A], error) {
	snap, err := Load[T](path)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, nil // No saved state
	}

	state, err := New[T, A](snap.State, cfg)
	if err != nil {
		return nil, fmt.Errorf("create state: %w", err)
	}
	result := &RestoreResult[T, A]{State: state}

	// Recreate effects (restored effects have zero-value activator - they can be re-set after load)
	if factory != nil {
		for _, meta := range snap.Effects {
			effect, err := factory(meta)
			if err != nil {
				result.EffectErrors = append(result.EffectErrors,
					fmt.Errorf("effect %q (type %s): %w", meta.ID, meta.Type, err))
				continue
			}
			if effect != nil {
				var zeroActivator A
				if err := state.AddEffect(effect, zeroActivator); err != nil {
					result.EffectErrors = append(result.EffectErrors, err)
					continue
				}
			}
		}
		// Clear the "previous" state that AddEffect created
		state.ClearPrevious()
	}

	return result, nil
}

// MakeEffectMeta creates metadata for an effect.
// Returns an error if params cannot be marshaled to JSON.
func MakeEffectMeta(id, typ string, params any) (EffectMeta, error) {
	var p json.RawMessage
	if params != nil {
		var err error
		p, err = json.Marshal(params)
		if err != nil {
			return EffectMeta{}, fmt.Errorf("statediff: failed to marshal effect params: %w", err)
		}
	}
	return EffectMeta{ID: id, Type: typ, Params: p}, nil
}

// ParseParams unmarshals effect params
func ParseParams[P any](meta EffectMeta) (P, error) {
	var p P
	if len(meta.Params) > 0 {
		err := json.Unmarshal(meta.Params, &p)
		return p, err
	}
	return p, nil
}
