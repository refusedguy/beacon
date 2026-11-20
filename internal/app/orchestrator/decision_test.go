package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type decisionDeps struct {
	inMain map[string]bool
	err    map[string]error
	calls  []string
}

func (d *decisionDeps) Satisfied(_ context.Context, ref string) (bool, error) {
	d.calls = append(d.calls, ref)
	if err := d.err[ref]; err != nil {
		return false, err
	}
	return d.inMain[ref], nil
}

func decisionBoolPtr(v bool) *bool { return &v }

func decisionWatch(policy *AutoMergePolicy) Watch {
	return Watch{ID: "watch:D1", Repo: "refusedguy/beacon", PR: 42, Verdict: VerdictReady, Revision: 7, Policy: policy}
}

func TestDecideBlockedWinsOverReadyAndDependencies(t *testing.T) {
	deps := &decisionDeps{inMain: map[string]bool{}}
	w := decisionWatch(&AutoMergePolicy{WaitFor: []string{"T3.100"}})
	w.Blocked = &BlockedInfo{
		Reason:       "чужой тест",
		FailedChecks: []string{"ci-build", "ci-audit"},
		BlockedBy:    "executor",
	}

	got := Decide(w, deps, CIState{})

	if got.Kind != DecisionBlockedFixAgent {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionBlockedFixAgent)
	}
	if got.Reason != "чужой тест" || got.BlockedBy != "executor" {
		t.Fatalf("reason/blockedBy = %q/%q", got.Reason, got.BlockedBy)
	}
	if !reflect.DeepEqual(got.FailedChecks, []string{"ci-build", "ci-audit"}) {
		t.Fatalf("failedChecks = %q", got.FailedChecks)
	}
	if len(deps.calls) != 0 {
		t.Fatalf("blocked Watch must short-circuit dependency resolution: %v", deps.calls)
	}
	if got.WatchID != w.ID || got.PR != w.PR || got.Revision != w.Revision {
		t.Fatalf("identity = %q/%d/%d", got.WatchID, got.PR, got.Revision)
	}
}

func TestDecideBlockedCopiesFailedChecks(t *testing.T) {
	checks := []string{"ci-build"}
	w := decisionWatch(nil)
	w.Blocked = &BlockedInfo{Reason: "не моя вина", FailedChecks: checks}

	got := Decide(w, nil, CIState{})
	checks[0] = "mutated"

	if !reflect.DeepEqual(got.FailedChecks, []string{"ci-build"}) {
		t.Fatalf("failedChecks aliases the Watch: %q", got.FailedChecks)
	}
}

func TestDecideWaitDependencyListsOnlyMissingRefs(t *testing.T) {
	deps := &decisionDeps{inMain: map[string]bool{"dep:a": true}}
	w := decisionWatch(&AutoMergePolicy{
		WaitFor:     []string{"dep:a", "dep:b"},
		WaitForMain: []string{"dep:c"},
	})

	got := Decide(w, deps, CIState{})

	if got.Kind != DecisionWaitDependency {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionWaitDependency)
	}
	if !reflect.DeepEqual(got.Refs, []string{"dep:b", "dep:c"}) {
		t.Fatalf("refs = %q, want [dep:b dep:c]", got.Refs)
	}
	if !reflect.DeepEqual(deps.calls, []string{"dep:a", "dep:b", "dep:c"}) {
		t.Fatalf("resolver calls = %q", deps.calls)
	}
}

func TestDecideWaitDependencyDedupesAndTrimsRefs(t *testing.T) {
	deps := &decisionDeps{inMain: map[string]bool{}}
	w := decisionWatch(&AutoMergePolicy{
		WaitFor:     []string{" dep:b ", "dep:b", "dep:a"},
		WaitForMain: []string{"dep:a"},
	})

	got := Decide(w, deps, CIState{})

	if !reflect.DeepEqual(got.Refs, []string{"dep:b", "dep:a"}) {
		t.Fatalf("refs = %q, want [dep:b dep:a]", got.Refs)
	}
}

func TestDecideWaitDependencyFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		deps DependencyResolver
	}{
		{name: "nil resolver", deps: nil},
		{name: "nil function", deps: DependencyResolverFunc(nil)},
		{
			name: "resolver error",
			deps: &decisionDeps{err: map[string]error{"dep:a": errors.New("store down")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := decisionWatch(&AutoMergePolicy{WaitFor: []string{"dep:a"}})
			got := Decide(w, tt.deps, CIState{})
			if got.Kind != DecisionWaitDependency || !reflect.DeepEqual(got.Refs, []string{"dep:a"}) {
				t.Fatalf("decision = %+v, want WAIT_DEPENDENCY [dep:a]", got)
			}
		})
	}
}

func TestDecideMergeWhenReadyAndDependenciesSatisfied(t *testing.T) {
	tests := []struct {
		name   string
		policy *AutoMergePolicy
	}{
		{name: "default policy", policy: nil},
		{name: "explicit auto merge", policy: &AutoMergePolicy{AutoMerge: decisionBoolPtr(true)}},
		{
			name: "satisfied waitFor",
			policy: &AutoMergePolicy{
				AutoMerge: decisionBoolPtr(true),
				WaitFor:   []string{"dep:a"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &decisionDeps{inMain: map[string]bool{"dep:a": true}}
			got := Decide(decisionWatch(tt.policy), deps, CIState{})
			if got.Kind != DecisionMerge {
				t.Fatalf("kind = %q, want %q", got.Kind, DecisionMerge)
			}
			if got.Reason != VerdictReady {
				t.Fatalf("reason = %q, want ready", got.Reason)
			}
		})
	}
}

func TestDecideNeverMergesWhenAutoMergeIsHeldBack(t *testing.T) {
	tests := []struct {
		name   string
		policy *AutoMergePolicy
		reason string
	}{
		{
			name:   "auto merge off",
			policy: &AutoMergePolicy{AutoMerge: decisionBoolPtr(false)},
			reason: "auto_merge_disabled",
		},
		{
			name:   "approval required",
			policy: &AutoMergePolicy{RequireApproval: true},
			reason: "require_approval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(decisionWatch(tt.policy), nil, CIState{})
			if got.Kind != DecisionNotifyOrchestrator {
				t.Fatalf("kind = %q, want %q", got.Kind, DecisionNotifyOrchestrator)
			}
			if got.Reason != tt.reason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestDecideRetryGoesToExecutor(t *testing.T) {
	w := decisionWatch(nil)
	w.Verdict = VerdictRetry
	got := Decide(w, nil, CIState{})
	if got.Kind != DecisionRetryExecutor {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionRetryExecutor)
	}
	if got.Reason != VerdictRetry {
		t.Fatalf("reason = %q, want retry", got.Reason)
	}
}

func TestDecideNotifiesForOtherVerdicts(t *testing.T) {
	for _, verdict := range []string{"watching", "rebase", VerdictFail, "keep_working", ""} {
		t.Run(verdict, func(t *testing.T) {
			w := decisionWatch(nil)
			w.Verdict = verdict
			got := Decide(w, nil, CIState{})
			if got.Kind != DecisionNotifyOrchestrator {
				t.Fatalf("verdict %q: kind = %q, want %q", verdict, got.Kind, DecisionNotifyOrchestrator)
			}
		})
	}
}

func TestDecideUsesCIVerdictOnlyWhenWatchIsSilent(t *testing.T) {
	w := decisionWatch(nil)
	w.Verdict = ""
	if got := Decide(w, nil, CIState{Verdict: VerdictReady}); got.Kind != DecisionMerge {
		t.Fatalf("empty Watch verdict with CI ready = %q, want MERGE", got.Kind)
	}

	w.Verdict = VerdictRetry
	if got := Decide(w, nil, CIState{Verdict: VerdictReady}); got.Kind != DecisionRetryExecutor {
		t.Fatalf("explicit Watch retry must win over CI ready: %q", got.Kind)
	}
}

func TestDecideCarriesIdentityForEveryOutcome(t *testing.T) {
	blocked := decisionWatch(nil)
	blocked.Blocked = &BlockedInfo{Reason: "чужой тест"}
	retry := decisionWatch(nil)
	retry.Verdict = VerdictRetry

	tests := []struct {
		name string
		w    Watch
		deps DependencyResolver
		kind DecisionKind
	}{
		{name: "merge", w: decisionWatch(nil), kind: DecisionMerge},
		{
			name: "wait",
			w:    decisionWatch(&AutoMergePolicy{WaitFor: []string{"dep:a"}}),
			deps: &decisionDeps{},
			kind: DecisionWaitDependency,
		},
		{name: "blocked", w: blocked, kind: DecisionBlockedFixAgent},
		{name: "retry", w: retry, kind: DecisionRetryExecutor},
		{
			name: "notify",
			w:    decisionWatch(&AutoMergePolicy{AutoMerge: decisionBoolPtr(false)}),
			kind: DecisionNotifyOrchestrator,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.w, tt.deps, CIState{})
			if got.Kind != tt.kind {
				t.Fatalf("kind = %q, want %q", got.Kind, tt.kind)
			}
			if got.WatchID != tt.w.ID || got.PR != tt.w.PR || got.Revision != tt.w.Revision {
				t.Fatalf("identity = %q/%d/%d, want %q/%d/%d",
					got.WatchID, got.PR, got.Revision, tt.w.ID, tt.w.PR, tt.w.Revision)
			}
		})
	}
}

func TestDecideDoesNotMutateInputs(t *testing.T) {
	policy := &AutoMergePolicy{WaitFor: []string{"dep:a"}}
	w := decisionWatch(policy)
	w.Blocked = &BlockedInfo{Reason: "чужой тест", FailedChecks: []string{"ci-build"}}
	before := *w.Blocked
	deps := &decisionDeps{inMain: map[string]bool{"dep:a": true}}

	_ = Decide(w, deps, CIState{Verdict: VerdictReady})

	if !reflect.DeepEqual(*w.Blocked, before) {
		t.Fatalf("block info mutated: %+v", w.Blocked)
	}
	if !reflect.DeepEqual(policy.WaitFor, []string{"dep:a"}) {
		t.Fatalf("policy mutated: %+v", policy)
	}
}
