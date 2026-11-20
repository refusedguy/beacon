// Package registry holds the author-owned agent registry config helpers.
//
// This file computes the configuration digest from T5.13. A digest identifies
// the content of the author file, not the state of the registry: reordering
// fields or roster entries must not change it, while editing a roster entry
// must produce a different value.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Digest returns the hex SHA-256 of the canonical JSON form of config. The
// digest is stable across field order and roster order, and changes when any
// config value changes.
//
// Config is a decoded document. Every value it can contain is supported by
// encoding/json, so the marshal in CanonicalJSON cannot fail.
func Digest(config map[string]any) string {
	sum := sha256.Sum256(CanonicalJSON(config))
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON serializes config with sorted object keys and sorted lists of
// records. encoding/json already sorts string keys of a map; canonicalValue
// also normalizes the value tree so record lists do not depend on author
// order. The result is the byte string hashed by Digest.
func CanonicalJSON(config map[string]any) []byte {
	encoded, _ := json.Marshal(canonicalValue(config))
	return encoded
}

// canonicalValue rebuilds a decoded value with deterministic ordering. A list
// of mappings is a roster-style list: its entries are sorted by id with the
// canonical entry as a tie-breaker. Lists of scalars keep their order, so a
// preset's member order remains meaningful.
func canonicalValue(value any) any {
	switch x := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, item := range x {
			out[key] = canonicalValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for key, item := range x {
			out[fmt.Sprint(key)] = canonicalValue(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = canonicalValue(item)
		}
		sortRecords(out)
		return out
	case []string:
		out := make([]string, len(x))
		copy(out, x)
		return out
	default:
		return value
	}
}

func sortRecords(items []any) {
	if len(items) < 2 {
		return
	}
	keys := make([]string, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return
		}
		keys[i] = recordKey(obj)
	}
	indexes := make([]int, len(items))
	for i := range indexes {
		indexes[i] = i
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		return keys[indexes[i]] < keys[indexes[j]]
	})
	sorted := make([]any, len(items))
	for i, index := range indexes {
		sorted[i] = items[index]
	}
	copy(items, sorted)
}

func recordKey(record map[string]any) string {
	encoded, _ := json.Marshal(record)
	if id, ok := record["id"].(string); ok {
		return id + "\x00" + string(encoded)
	}
	return string(encoded)
}
