package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

type mergeStatusResolverFunc func(ref string) (bool, error)

func (f mergeStatusResolverFunc) Satisfied(ref string) (bool, error) { return f(ref) }

func TestMergeHoldReasons(t *testing.T) {
	base := MergeHoldPolicy{AutoMerge: true, MergerBound: true}
	allSatisfied := mergeStatusResolverFunc(func(string) (bool, error) { return true, nil })
	mainUnsatisfied := mergeStatusResolverFunc(func(ref string) (bool, error) {
		return ref != "#77", nil
	})

	tests := []struct {
		name     string
		policy   MergeHoldPolicy
		resolver MergeDependencyResolver
		want     string
	}{
		{
			name:   "require_approval",
			policy: MergeHoldPolicy{AutoMerge: true, MergerBound: true, RequireApproval: true},
			want:   "require_approval",
		},
		{
			name:   "resolver_unavailable",
			policy: MergeHoldPolicy{AutoMerge: true, MergerBound: true, WaitFor: []string{"#12"}},
			want:   "resolver_unavailable",
		},
		{
			name:   "wait_for",
			policy: MergeHoldPolicy{AutoMerge: true, MergerBound: true, WaitFor: []string{"#12"}},
			resolver: mergeStatusResolverFunc(func(string) (bool, error) {
				return false, nil
			}),
			want: "wait_for:#12",
		},
		{
			name:     "wait_for_main",
			policy:   MergeHoldPolicy{AutoMerge: true, MergerBound: true, WaitForMain: []string{"#77"}},
			resolver: mainUnsatisfied,
			want:     "wait_for_main:#77",
		},
		{
			name:   "merge allowed",
			policy: base,
			want:   "",
		},
		{
			name:     "all references satisfied",
			policy:   MergeHoldPolicy{AutoMerge: true, MergerBound: true, WaitFor: []string{"#12"}, WaitForMain: []string{"#77"}},
			resolver: allSatisfied,
			want:     "",
		},
		{
			name:   "auto merge disabled",
			policy: MergeHoldPolicy{MergerBound: true},
			want:   "auto_merge_disabled",
		},
		{
			name:   "merger not bound",
			policy: MergeHoldPolicy{AutoMerge: true},
			want:   "merger_unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergeHold(tt.policy, tt.resolver); got != tt.want {
				t.Errorf("MergeHold = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMergeHoldResolverErrorKeepsHold(t *testing.T) {
	policy := MergeHoldPolicy{AutoMerge: true, MergerBound: true, WaitFor: []string{"T3.42"}}
	resolver := mergeStatusResolverFunc(func(string) (bool, error) {
		return false, errors.New("resolver offline")
	})
	if got := MergeHold(policy, resolver); got != "wait_for:T3.42" {
		t.Errorf("MergeHold = %q, want wait_for:T3.42", got)
	}
}

type fakeMergeStatusStore struct {
	watches     map[string]domain.Watch
	obligations map[string]domain.Obligation
}

func newFakeMergeStatusStore() *fakeMergeStatusStore {
	return &fakeMergeStatusStore{
		watches:     make(map[string]domain.Watch),
		obligations: make(map[string]domain.Obligation),
	}
}

func (f *fakeMergeStatusStore) SaveWatch(w domain.Watch) error {
	f.watches[w.ID] = w
	return nil
}

func (f *fakeMergeStatusStore) Watch(id string) (domain.Watch, error) {
	w, ok := f.watches[id]
	if !ok {
		return domain.Watch{}, errors.New("watch not found")
	}
	return w, nil
}

func (f *fakeMergeStatusStore) ActiveWatches() ([]domain.Watch, error) { return nil, nil }

func (f *fakeMergeStatusStore) AllWatches() ([]domain.Watch, error) { return nil, nil }

func (f *fakeMergeStatusStore) SaveObligation(o domain.Obligation) error {
	f.obligations[o.ID] = o
	return nil
}

func (f *fakeMergeStatusStore) Obligation(id string) (domain.Obligation, error) {
	o, ok := f.obligations[id]
	if !ok {
		return domain.Obligation{}, errors.New("obligation not found")
	}
	return o, nil
}

func (f *fakeMergeStatusStore) PendingObligations() ([]domain.Obligation, error) { return nil, nil }

type staticRebaseUsage int

func (s staticRebaseUsage) RebaseAttempts(string) int { return int(s) }

func TestMergeStatusReaderCountsWakesAndRebases(t *testing.T) {
	store := newFakeMergeStatusStore()
	w := domain.Watch{
		ID:          "w3",
		Verdict:     domain.VerdictReady,
		Status:      domain.StatusParked,
		Policy:      domain.Policy{MaxWakesPerWatch: 6, MaxRebases: 2},
		MergePolicy: domain.DefaultMergePolicy(),
		Subscribers: []domain.Subscriber{{ID: "ses_exec"}, {ID: "ses_orch"}},
	}
	store.watches[w.ID] = w
	counterID := domain.ObligationKey("wakes:w3:ses_exec", 0, "count")
	store.obligations[counterID] = domain.Obligation{
		ID:       counterID,
		WatchID:  w.ID,
		Attempts: make([]domain.Attempt, 6),
	}

	reader := NewMergeStatusReader(store)
	reader.SetMergerBound(true)
	reader.SetRebaseUsage(staticRebaseUsage(2))
	facts, err := reader.MergeStatus(context.Background(), "w3")
	if err != nil {
		t.Fatalf("MergeStatus: %v", err)
	}
	if facts.WakeUsed != 6 {
		t.Errorf("WakeUsed = %d, want 6", facts.WakeUsed)
	}
	if facts.WakeCap != 6 {
		t.Errorf("WakeCap = %d, want 6", facts.WakeCap)
	}
	if facts.RebaseUsed != 2 || facts.RebaseBudget != 2 {
		t.Errorf("rebase = %d/%d, want 2/2", facts.RebaseUsed, facts.RebaseBudget)
	}
	if facts.HoldReason != "" {
		t.Errorf("HoldReason = %q, want empty for an allowed merge", facts.HoldReason)
	}
}

func TestMergeStatusHandlerProjectsFacts(t *testing.T) {
	w := domain.Watch{
		ID:          "w1",
		Verdict:     domain.VerdictReady,
		MergePolicy: domain.DefaultMergePolicy(),
		BlockerID:   "blocker-9",
	}
	facts := MergeStatusFacts{
		Watch:        w,
		HoldReason:   "wait_for_main:#77",
		WakeUsed:     4,
		WakeCap:      6,
		RebaseUsed:   1,
		RebaseBudget: 3,
		RetryBudget:  2,
		MergerBound:  true,
	}
	handler := NewMergeStatusHandler(MergeStatusReaderFunc(func(_ context.Context, watchID string) (MergeStatusFacts, error) {
		if watchID != "w1" {
			t.Errorf("reader watchID = %q, want w1", watchID)
		}
		return facts, nil
	}))
	result, out, err := handler.Handle(context.Background(), nil, MergeStatusInput{WatchID: "w1"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result == nil {
		t.Fatal("call result is nil")
	}
	if out.WatchID != "w1" || out.Verdict != string(domain.VerdictReady) {
		t.Errorf("identity = %q/%q", out.WatchID, out.Verdict)
	}
	if out.HoldReason != "wait_for_main:#77" {
		t.Errorf("HoldReason = %q", out.HoldReason)
	}
	if out.BlockerID != "blocker-9" {
		t.Errorf("BlockerID = %q", out.BlockerID)
	}
	if !out.AutoMerge || !out.MergerBound {
		t.Errorf("policy = %v/%v, want true/true", out.AutoMerge, out.MergerBound)
	}
	if out.WakeUsed != 4 || out.WakeCap != 6 || out.RebaseUsed != 1 || out.RebaseBudget != 3 || out.RetryBudget != 2 {
		t.Errorf("budgets = %+v", out)
	}
}

func TestMergeStatusReportsWatchWithoutObligation(t *testing.T) {
	store := newFakeMergeStatusStore()
	store.watches["w2"] = domain.Watch{
		ID:          "w2",
		Verdict:     domain.VerdictReady,
		MergePolicy: domain.DefaultMergePolicy(),
		Subscribers: []domain.Subscriber{{ID: "ses_exec"}},
	}
	reader := NewMergeStatusReader(store)
	reader.SetMergerBound(true)
	handler := NewMergeStatusHandler(reader)

	_, out, err := handler.Handle(context.Background(), nil, MergeStatusInput{WatchID: "w2"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out.Verdict != string(domain.VerdictReady) {
		t.Errorf("Verdict = %q, want ready", out.Verdict)
	}
	if out.WakeUsed != 0 {
		t.Errorf("WakeUsed = %d, want 0 when no wake counter exists", out.WakeUsed)
	}
}

func TestMergeStatusRequiresWatchID(t *testing.T) {
	handler := NewMergeStatusHandler(MergeStatusReaderFunc(func(context.Context, string) (MergeStatusFacts, error) {
		t.Fatal("reader must not be called without a watchID")
		return MergeStatusFacts{}, nil
	}))
	if _, _, err := handler.Handle(context.Background(), nil, MergeStatusInput{}); err == nil {
		t.Fatal("Handle accepted an empty watchID")
	}
}

func TestMergeStatusOutputSchema(t *testing.T) {
	schema, err := MergeStatusOutputSchema()
	if err != nil {
		t.Fatalf("MergeStatusOutputSchema: %v", err)
	}
	fields := []string{
		"watchID", "verdict", "autoMerge", "mergerBound", "holdReason",
		"blockerID", "wakeUsed", "wakeCap", "rebaseUsed", "rebaseBudget", "retryBudget",
	}
	for _, field := range fields {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("output schema missing %q", field)
		}
	}
}
