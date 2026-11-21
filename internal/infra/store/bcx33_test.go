package store

import (
	"strconv"
	"testing"
)

// TestFencingTokenReadersShareOneValue pins the BCX-33 contract: the binding
// reader and the scoped reader both take fencing_token from the canonical
// INTEGER column, so the same stored row yields the same token in both paths.
func TestFencingTokenReadersShareOneValue(t *testing.T) {
	d := newWatchBindingTestDB(t)
	const token int64 = 42
	if _, err := d.WriteWatchBinding(watchBindingEntry("bcx33-watch", 3, token, "cfg")); err != nil {
		t.Fatalf("WriteWatchBinding: %v", err)
	}

	binding, found, err := d.LatestWatchBinding("bcx33-watch")
	if err != nil {
		t.Fatalf("LatestWatchBinding: %v", err)
	}
	if !found {
		t.Fatal("binding not found by the direct reader")
	}
	if binding.FencingToken != token {
		t.Fatalf("direct reader token = %d, want %d", binding.FencingToken, token)
	}

	scope, err := NewProjectScope("project-a")
	if err != nil {
		t.Fatalf("NewProjectScope: %v", err)
	}
	scoped, err := NewScopedRegistry(d, scope)
	if err != nil {
		t.Fatalf("NewScopedRegistry: %v", err)
	}
	scopedBinding, found, err := scoped.LatestWatchBinding("bcx33-watch")
	if err != nil {
		t.Fatalf("scoped LatestWatchBinding: %v", err)
	}
	if !found {
		t.Fatal("binding not found by the scoped reader")
	}
	if want := strconv.FormatInt(token, 10); scopedBinding.FencingToken != want {
		t.Fatalf("scoped reader token = %q, want %q", scopedBinding.FencingToken, want)
	}
}
