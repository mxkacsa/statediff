package statediff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Patch is a list of operations (RFC 6902 JSON Patch compatible)
type Patch []Op

// Op represents a single patch operation
type Op struct {
	Op    string `json:"op"`              // "add", "remove", "replace"
	Path  string `json:"path"`            // JSON Pointer
	Value any    `json:"value,omitempty"` // New value
}

// JSON returns the patch as JSON bytes
func (p Patch) JSON() ([]byte, error) {
	if len(p) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(p)
}

// Empty returns true if patch has no operations
func (p Patch) Empty() bool {
	return len(p) == 0
}

// ArrayConfig configures array diff behavior
type ArrayConfig struct {
	Strategy ArrayStrategy
	KeyField string // For ByKey strategy
}

// ArrayStrategy determines how arrays are diffed
type ArrayStrategy int

const (
	ArrayReplace ArrayStrategy = iota // Replace entire array (default)
	ArrayByIndex                      // Diff per index
	ArrayByKey                        // Match by key field (NOTE: does not track order changes)
)

// calcDiff computes the diff between two values
func calcDiff[T any](old, new T, cfg ArrayConfig) (Patch, error) {
	oldData, err := json.Marshal(old)
	if err != nil {
		return nil, err
	}
	newData, err := json.Marshal(new)
	if err != nil {
		return nil, err
	}

	var oldMap, newMap map[string]any
	if err := json.Unmarshal(oldData, &oldMap); err != nil {
		return nil, fmt.Errorf("unmarshal old state: %w", err)
	}
	if err := json.Unmarshal(newData, &newMap); err != nil {
		return nil, fmt.Errorf("unmarshal new state: %w", err)
	}

	return diffMaps("", oldMap, newMap, cfg), nil
}

func diffMaps(path string, old, new map[string]any, cfg ArrayConfig) Patch {
	var ops Patch

	// Collect keys and sort for deterministic output
	// Map iteration order is random in Go - we must sort for consistent patches
	var oldKeys []string
	for k := range old {
		oldKeys = append(oldKeys, k)
	}
	sort.Strings(oldKeys)

	var newKeys []string
	for k := range new {
		newKeys = append(newKeys, k)
	}
	sort.Strings(newKeys)

	// Removed and changed (in sorted order)
	for _, k := range oldKeys {
		kPath := path + "/" + escapePtr(k)
		newV, exists := new[k]
		if !exists {
			ops = append(ops, Op{Op: "remove", Path: kPath})
		} else {
			ops = append(ops, diffValues(kPath, old[k], newV, cfg)...)
		}
	}

	// Added (in sorted order)
	for _, k := range newKeys {
		if _, exists := old[k]; !exists {
			ops = append(ops, Op{Op: "add", Path: path + "/" + escapePtr(k), Value: new[k]})
		}
	}

	return ops
}

func diffValues(path string, old, new any, cfg ArrayConfig) Patch {
	if reflect.DeepEqual(old, new) {
		return nil
	}

	// Type mismatch
	if reflect.TypeOf(old) != reflect.TypeOf(new) {
		return Patch{{Op: "replace", Path: path, Value: new}}
	}

	// Nested object
	if oldMap, ok := old.(map[string]any); ok {
		return diffMaps(path, oldMap, new.(map[string]any), cfg)
	}

	// Array
	if oldArr, ok := old.([]any); ok {
		return diffArrays(path, oldArr, new.([]any), cfg)
	}

	// Primitive
	return Patch{{Op: "replace", Path: path, Value: new}}
}

func diffArrays(path string, old, new []any, cfg ArrayConfig) Patch {
	switch cfg.Strategy {
	case ArrayByIndex:
		return diffArraysByIndex(path, old, new, cfg)
	case ArrayByKey:
		return diffArraysByKey(path, old, new, cfg)
	default:
		if !reflect.DeepEqual(old, new) {
			return Patch{{Op: "replace", Path: path, Value: new}}
		}
		return nil
	}
}

func diffArraysByIndex(path string, old, new []any, cfg ArrayConfig) Patch {
	var ops Patch
	minLen := min(len(old), len(new))

	// Compare existing
	for i := 0; i < minLen; i++ {
		ops = append(ops, diffValues(fmt.Sprintf("%s/%d", path, i), old[i], new[i], cfg)...)
	}

	// Removed (from end)
	for i := len(old) - 1; i >= minLen; i-- {
		ops = append(ops, Op{Op: "remove", Path: fmt.Sprintf("%s/%d", path, i)})
	}

	// Added
	for i := minLen; i < len(new); i++ {
		ops = append(ops, Op{Op: "add", Path: path + "/-", Value: new[i]})
	}

	return ops
}

func diffArraysByKey(path string, old, new []any, cfg ArrayConfig) Patch {
	if cfg.KeyField == "" {
		return Patch{{Op: "replace", Path: path, Value: new}}
	}

	getKey := func(v any) (string, bool) {
		if m, ok := v.(map[string]any); ok {
			if k, ok := m[cfg.KeyField]; ok {
				return fmt.Sprint(k), true
			}
		}
		return "", false
	}

	oldIdx := make(map[string]int)
	newIdx := make(map[string]int)

	for i, v := range old {
		if k, ok := getKey(v); ok {
			oldIdx[k] = i
		}
	}
	for i, v := range new {
		if k, ok := getKey(v); ok {
			newIdx[k] = i
		}
	}

	var ops Patch

	// Collect removed indices and sort descending
	// This is critical: JSON Patch operations are applied sequentially,
	// so removing index 0 before index 2 would shift indices incorrectly.
	var removedIndices []int
	for k, i := range oldIdx {
		if _, exists := newIdx[k]; !exists {
			removedIndices = append(removedIndices, i)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(removedIndices)))
	for _, i := range removedIndices {
		ops = append(ops, Op{Op: "remove", Path: fmt.Sprintf("%s/%d", path, i)})
	}

	// Added and changed - iterate over 'new' slice (not map!) to preserve order
	// This is critical: map iteration order is random in Go, which would cause
	// non-deterministic patch order and corrupted client state.
	for ni, v := range new {
		k, hasKey := getKey(v)
		if !hasKey {
			continue // Skip elements without key field
		}

		if oi, existed := oldIdx[k]; !existed {
			// New element - add to end
			ops = append(ops, Op{Op: "add", Path: path + "/-", Value: v})
		} else {
			// Existing element - use ni (new index) for the path
			ops = append(ops, diffValues(fmt.Sprintf("%s/%d", path, ni), old[oi], new[ni], cfg)...)
		}
	}

	return ops
}

// escapePtr escapes JSON Pointer special chars
func escapePtr(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
