package orchestrator

import (
	"strconv"
	"testing"
)

func TestApprovalCacheEvictsOldestFirst(t *testing.T) {
	var c ApprovalCache
	for i := 0; i < maxApprovalCache+1; i++ {
		key := ApprovalKey{EnvironmentID: "env", Category: ApprovalCategoryAction, Path: "/p/" + strconv.Itoa(i)}
		c.Remember(key, i%2 == 0)
	}

	if got := c.Len(); got != maxApprovalCache {
		t.Fatalf("Len = %d, want %d", got, maxApprovalCache)
	}
	oldest := ApprovalKey{EnvironmentID: "env", Category: ApprovalCategoryAction, Path: "/p/0"}
	if _, ok := c.Lookup(oldest); ok {
		t.Fatal("oldest approval survived eviction")
	}
	newest := ApprovalKey{EnvironmentID: "env", Category: ApprovalCategoryAction, Path: "/p/" + strconv.Itoa(maxApprovalCache)}
	if allowed, ok := c.Lookup(newest); !ok || !allowed {
		t.Fatalf("newest approval = %v, %v; want true, true", allowed, ok)
	}
}

func TestApprovalCacheRepeatKeepsDecisionAndSize(t *testing.T) {
	var c ApprovalCache
	key := ApprovalKey{EnvironmentID: "env", Category: ApprovalMerge, Path: "/merge"}
	c.Remember(key, true)
	c.Remember(key, true)
	c.Remember(key, false)

	if got := c.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	if allowed, ok := c.Lookup(key); !ok || allowed {
		t.Fatalf("Lookup after repeat = %v, %v; want false, true", allowed, ok)
	}
}
