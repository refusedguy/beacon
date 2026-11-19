package domain

import (
	"fmt"
	"testing"
)

func TestNextRevisionMonotonicNoRepeats(t *testing.T) {
	const steps = 1000
	seen := make(map[uint64]bool, steps+1)
	current := uint64(0)
	seen[current] = true

	for i := 0; i < steps; i++ {
		next := NextRevision(current)
		if next != current+1 {
			t.Fatalf("step %d: NextRevision(%d) = %d, want %d", i, current, next, current+1)
		}
		if seen[next] {
			t.Fatalf("step %d: revision %d repeated", i, next)
		}
		seen[next] = true
		current = next
	}

	if got := len(seen); got != steps+1 {
		t.Fatalf("got %d distinct revisions, want %d", got, steps+1)
	}
}

func TestNextRevisionFromNonZero(t *testing.T) {
	if got := NextRevision(41); got != 42 {
		t.Fatalf("NextRevision(41) = %d, want 42", got)
	}
}

func TestNextRevisionOverflowDoesNotReuse(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NextRevision(max uint64) must panic instead of reusing or lowering the revision")
		}
	}()
	_ = NextRevision(^uint64(0))
}

func TestNextRevisionMonotonicUnderEventPermutation(t *testing.T) {
	events := []string{"rebase-1", "head-changed", "rebase-2", "merged", "rebase-3"}
	orders := [][]int{
		{0, 1, 2, 3, 4},
		{4, 3, 2, 1, 0},
		{2, 0, 4, 1, 3},
	}

	for _, order := range orders {
		current := uint64(0)
		seen := make(map[uint64]bool, len(events))
		for _, idx := range order {
			_ = events[idx]
			current = NextRevision(current)
			if seen[current] {
				t.Fatalf("order %v: revision %d repeated", order, current)
			}
			seen[current] = true
		}
		for rev := uint64(1); rev <= uint64(len(events)); rev++ {
			if !seen[rev] {
				t.Fatalf("order %v: missing revision %d", order, rev)
			}
		}
	}
}

func TestObligationKeyStableOnRepeat(t *testing.T) {
	first := ObligationKey("w1", 3, "s1")
	for i := 0; i < 3; i++ {
		if got := ObligationKey("w1", 3, "s1"); got != first {
			t.Fatalf("call %d: key changed: %q, want %q", i, got, first)
		}
	}
}

func TestObligationKeyChangesWithComponent(t *testing.T) {
	base := ObligationKey("w1", 3, "s1")
	cases := []struct {
		component string
		key       string
	}{
		{"watchID", ObligationKey("w2", 3, "s1")},
		{"revision", ObligationKey("w1", 4, "s1")},
		{"subscriberID", ObligationKey("w1", 3, "s2")},
	}
	for _, tc := range cases {
		if tc.key == base {
			t.Errorf("changing %s did not change the key: %q", tc.component, base)
		}
	}
}

func TestObligationKeySingleChangeWithSeparators(t *testing.T) {
	base := ObligationKey("w:1", 2, "s")
	if got := ObligationKey("w", 2, "s"); got == base {
		t.Errorf("watchID change collided: %q", base)
	}
	if got := ObligationKey("w:1", 3, "s"); got == base {
		t.Errorf("revision change collided: %q", base)
	}
	if got := ObligationKey("w:1", 2, "2:s"); got == base {
		t.Errorf("subscriberID change collided: %q", base)
	}
}

func TestObligationKeyInjectiveAcrossTriples(t *testing.T) {
	// A plain "watchID:revision:subscriberID" join maps both triples below to
	// "w:1:2:s". Escaping the components keeps the keys distinct.
	a := ObligationKey("w:1", 2, "s")
	b := ObligationKey("w", 1, "2:s")
	if a == b {
		t.Fatalf("different triples share key %q", a)
	}
}

func TestIdempotencyKeyStableOnRepeat(t *testing.T) {
	first := IdempotencyKey(StartNamespace, "req-abc")
	for i := 0; i < 3; i++ {
		if got := IdempotencyKey(StartNamespace, "req-abc"); got != first {
			t.Fatalf("call %d: key changed: %q, want %q", i, got, first)
		}
	}
}

func TestIdempotencyKeySeparateFromClientRequestID(t *testing.T) {
	// ADR 0031 idempotency_key and ADR 0028 clientRequestId are different keys.
	// The namespace makes the start key differ from the raw client request id
	// and from the same value scoped under another namespace.
	raw := "req-abc"
	start := IdempotencyKey(StartNamespace, raw)
	if start == raw {
		t.Fatalf("start key must be namespaced, got raw %q", raw)
	}
	if other := IdempotencyKey("client-request", raw); other == start {
		t.Fatalf("different namespaces share key %q", start)
	}
}

func TestIdempotencyKeyInjectiveWithSeparators(t *testing.T) {
	base := IdempotencyKey(StartNamespace, "req:1")
	cases := []struct {
		namespace string
		key       string
	}{
		{"task-start", "req:2"},
		{"other", "req:1"},
	}
	for _, tc := range cases {
		if got := IdempotencyKey(tc.namespace, tc.key); got == base {
			t.Errorf("(%q, %q) collided with %q", tc.namespace, tc.key, base)
		}
	}
	if IdempotencyKey("a:b", "c") == IdempotencyKey("a", "b:c") {
		t.Error("escaping failed: two different pairs share a key")
	}
}

func TestRejectDuplicateConflict(t *testing.T) {
	err := NewRejectDuplicate("req-abc", "task-1")
	if err.Code != CodeRejectDuplicate {
		t.Errorf("Code = %q, want %q", err.Code, CodeRejectDuplicate)
	}
	if err.Status != 409 {
		t.Errorf("Status = %d, want 409", err.Status)
	}
	if !IsRejectDuplicate(err) {
		t.Error("IsRejectDuplicate must recognize the conflict")
	}
	if IsRejectDuplicate(NewAppError(CodeInvalidInput, "bad")) {
		t.Error("IsRejectDuplicate must reject an unrelated error")
	}
	if !IsRejectDuplicate(fmt.Errorf("wrapped: %w", err)) {
		t.Error("IsRejectDuplicate must recognize a wrapped conflict")
	}
}
