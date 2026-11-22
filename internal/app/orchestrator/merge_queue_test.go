package orchestrator

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func queueCandidate(pr int, dependsOn ...string) MergeCandidate {
	return MergeCandidate{
		Watch:     Watch{ID: "w" + strconv.Itoa(pr), PR: pr, Verdict: VerdictReady},
		DependsOn: dependsOn,
	}
}

func candidateIDs(candidates []MergeCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, mergeCandidateID(c))
	}
	return ids
}

func assertCandidateIDs(t *testing.T, got []MergeCandidate, want ...string) {
	t.Helper()
	gotIDs := candidateIDs(got)
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("candidates = %v, want %v", gotIDs, want)
	}
}

func TestSafeMergeOrderIndependentWatches(t *testing.T) {
	ordered, deferred := SafeMergeOrder([]MergeCandidate{
		queueCandidate(7),
		queueCandidate(3),
		queueCandidate(11),
	})
	assertCandidateIDs(t, ordered, "w3", "w7", "w11")
	if len(deferred) != 0 {
		t.Fatalf("deferred = %v, want none", candidateIDs(deferred))
	}
}

func TestSafeMergeOrderRespectsDependencyEdges(t *testing.T) {
	ordered, deferred := SafeMergeOrder([]MergeCandidate{
		queueCandidate(9, "w5"),
		queueCandidate(5),
		queueCandidate(7),
	})
	assertCandidateIDs(t, ordered, "w5", "w7", "w9")
	if len(deferred) != 0 {
		t.Fatalf("deferred = %v, want none", candidateIDs(deferred))
	}
}

func TestSafeMergeOrderDefersDependencyNotInMain(t *testing.T) {
	ordered, deferred := SafeMergeOrder([]MergeCandidate{
		queueCandidate(3, "w2"),
		queueCandidate(4, "w3"),
		queueCandidate(1),
	})
	assertCandidateIDs(t, ordered, "w1")
	assertCandidateIDs(t, deferred, "w3", "w4")
}

func TestSafeMergeOrderDefersCycle(t *testing.T) {
	ordered, deferred := SafeMergeOrder([]MergeCandidate{
		queueCandidate(1, "w2"),
		queueCandidate(2, "w1"),
		queueCandidate(3),
	})
	assertCandidateIDs(t, ordered, "w3")
	assertCandidateIDs(t, deferred, "w1", "w2")
}

func TestSafeMergeOrderUsesPRIdentityWhenIDMissing(t *testing.T) {
	ordered, _ := SafeMergeOrder([]MergeCandidate{
		{Watch: Watch{PR: 12}, DependsOn: []string{"pr/4"}},
		{Watch: Watch{PR: 4}},
	})
	assertCandidateIDs(t, ordered, "pr/4", "pr/12")
}

func TestDeferredChainResumesOnlyOnMatchingSignal(t *testing.T) {
	chain := NewDeferredChain([]MergeCandidate{queueCandidate(3), queueCandidate(5)})
	chain.PauseOnConflict("w3")
	if got := chain.PausedOn(); got != "w3" {
		t.Fatalf("pausedOn = %q, want w3", got)
	}
	if _, err := chain.ConflictResolved("w5"); err == nil {
		t.Fatal("wrong conflict signal resumed the chain")
	}
	if got := chain.PausedOn(); got != "w3" {
		t.Fatalf("pausedOn = %q after wrong signal, want w3", got)
	}
	resumed, err := chain.ConflictResolved("w3")
	if err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}
	assertCandidateIDs(t, resumed, "w3", "w5")
	if got := chain.PausedOn(); got != "" {
		t.Fatalf("pausedOn = %q after resume, want empty", got)
	}
	chain.Advance("w3")
	assertCandidateIDs(t, chain.pending, "w5")
}

type fakeMergeQueuePort struct {
	candidates  []MergeCandidate
	conflict    string
	readyErr    error
	conflictErr error
}

func (p *fakeMergeQueuePort) ReadyCandidates(context.Context) ([]MergeCandidate, error) {
	return append([]MergeCandidate(nil), p.candidates...), p.readyErr
}

func (p *fakeMergeQueuePort) ConflictResolved(context.Context) (string, error) {
	return p.conflict, p.conflictErr
}

func TestNextMergeBuildsOrderAndReturnsDeferred(t *testing.T) {
	port := &fakeMergeQueuePort{candidates: []MergeCandidate{
		queueCandidate(9, "w2"),
		queueCandidate(1),
	}}
	ordered, deferred, err := NextMerge(context.Background(), port, nil)
	if err != nil {
		t.Fatalf("next merge: %v", err)
	}
	assertCandidateIDs(t, ordered, "w1")
	assertCandidateIDs(t, deferred, "w9")
}

func TestNextMergeResumesPausedChain(t *testing.T) {
	chain := NewDeferredChain([]MergeCandidate{queueCandidate(9), queueCandidate(1)})
	chain.PauseOnConflict("w9")
	port := &fakeMergeQueuePort{conflict: "w9"}
	ordered, deferred, err := NextMerge(context.Background(), port, chain)
	if err != nil {
		t.Fatalf("next merge: %v", err)
	}
	assertCandidateIDs(t, ordered, "w9", "w1")
	if len(deferred) != 0 {
		t.Fatalf("deferred = %v, want none", candidateIDs(deferred))
	}
}

func TestNextMergePropagatesPortErrors(t *testing.T) {
	wantErr := errors.New("graph unavailable")
	port := &fakeMergeQueuePort{readyErr: wantErr}
	if _, _, err := NextMerge(context.Background(), port, nil); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	chain := NewDeferredChain(nil)
	chain.PauseOnConflict("w9")
	port = &fakeMergeQueuePort{conflictErr: wantErr}
	if _, _, err := NextMerge(context.Background(), port, chain); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
