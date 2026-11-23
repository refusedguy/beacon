// Package shared holds small generic helpers that depend only on the standard
// library. Every production layer may import it; see layers.json.
package shared

// DistinctBy returns items in their original order after dropping entries with
// a repeated key. The first entry for a key wins. The result is non-nil even
// when items is empty.
func DistinctBy[T comparable](items []T, key func(T) string) []T {
	seen := make(map[string]bool, len(items))
	out := make([]T, 0, len(items))
	for _, item := range items {
		k := key(item)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item)
	}
	return out
}

// Contains reports whether v equals an entry in items.
func Contains[T comparable](items []T, v T) bool {
	for _, item := range items {
		if item == v {
			return true
		}
	}
	return false
}

// Filter returns the entries for which keep reports true, in original order.
// The result is non-nil even when items is empty.
func Filter[T any](items []T, keep func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}
