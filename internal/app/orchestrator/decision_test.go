package orchestrator

import (
	"context"
	"errors"
	"fmt"
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

// TestDecideFixAttemptCap proves the loop breaker: the first three fix attempts
// still ask for a FIX agent, the next request escalates to a human.
func TestDecideFixAttemptCap(t *testing.T) {
	for _, attempts := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprintf("attempts=%d", attempts), func(t *testing.T) {
			w := decisionWatch(nil)
			w.Blocked = &BlockedInfo{
				Reason:       "чужой тест",
				FailedChecks: []string{"ci-build"},
				BlockedBy:    "executor",
				FixAttempts:  attempts,
			}

			got := Decide(w, nil, CIState{})

			if got.WatchID != w.ID || got.PR != w.PR || got.Revision != w.Revision {
				t.Fatalf("identity = %q/%d/%d", got.WatchID, got.PR, got.Revision)
			}
			if attempts < MaxFixAttempts {
				if got.Kind != DecisionBlockedFixAgent {
					t.Fatalf("attempts=%d: kind = %q, want %q", attempts, got.Kind, DecisionBlockedFixAgent)
				}
				if got.Reason != "чужой тест" {
					t.Fatalf("attempts=%d: reason = %q, want block reason", attempts, got.Reason)
				}
				return
			}
			if got.Kind != DecisionNotifyOrchestrator {
				t.Fatalf("attempts=%d: kind = %q, want %q", attempts, got.Kind, DecisionNotifyOrchestrator)
			}
			if got.Reason != ReasonEscalateHuman {
				t.Fatalf("attempts=%d: reason = %q, want %q", attempts, got.Reason, ReasonEscalateHuman)
			}
			if got.BlockedBy != "executor" || !reflect.DeepEqual(got.FailedChecks, []string{"ci-build"}) {
				t.Fatalf("escalation lost block context: %+v", got)
			}
		})
	}
}

// TestDecideFixAttemptsArePerWatch proves the cap is not shared state: an
// escalated Watch never changes the decision for a different Watch.
func TestDecideFixAttemptsArePerWatch(t *testing.T) {
	exhausted := decisionWatch(nil)
	exhausted.ID = "watch:exhausted"
	exhausted.Blocked = &BlockedInfo{Reason: "чужой тест", FixAttempts: MaxFixAttempts}
	fresh := decisionWatch(nil)
	fresh.ID = "watch:fresh"
	fresh.Blocked = &BlockedInfo{Reason: "чужой тест"}

	first := Decide(exhausted, nil, CIState{})
	second := Decide(fresh, nil, CIState{})
	again := Decide(exhausted, nil, CIState{})

	if first.Kind != DecisionNotifyOrchestrator || first.Reason != ReasonEscalateHuman {
		t.Fatalf("exhausted watch = %+v", first)
	}
	if second.Kind != DecisionBlockedFixAgent {
		t.Fatalf("fresh watch = %+v", second)
	}
	if again.Kind != DecisionNotifyOrchestrator || again.Reason != ReasonEscalateHuman {
		t.Fatalf("repeated decision changed: %+v", again)
	}
}

// TestDecideUnblockedWatchNeverEscalates models the post-unblock Watch: the
// block record is cleared, so no fix-attempt budget is consulted and the Watch
// follows the normal ready path again.
func TestDecideUnblockedWatchNeverEscalates(t *testing.T) {
	w := decisionWatch(nil)
	w.Blocked = nil

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionMerge {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionMerge)
	}
	if got.Reason == ReasonEscalateHuman {
		t.Fatalf("unblocked Watch escalated: %+v", got)
	}
}

func TestGeneratedFileEdits(t *testing.T) {
	tests := []struct {
		name         string
		changedFiles []string
		patterns     []string
		want         []string
	}{
		{
			name:         "generated roadmap status matches",
			changedFiles: []string{"docs/ROADMAP_STATUS.md", "internal/app/orchestrator/decision.go"},
			patterns:     DefaultGeneratedFilePatterns,
			want:         []string{"docs/ROADMAP_STATUS.md"},
		},
		{
			name:         "batch-apply task card matches",
			changedFiles: []string{"docs/tasks/HD-007.md"},
			patterns:     DefaultGeneratedFilePatterns,
			want:         []string{"docs/tasks/HD-007.md"},
		},
		{
			name:         "eval report matches",
			changedFiles: []string{"evals/reports/hd06-generated-files.md"},
			patterns:     DefaultGeneratedFilePatterns,
			want:         []string{"evals/reports/hd06-generated-files.md"},
		},
		{
			name:         "nested eval report matches directory pattern",
			changedFiles: []string{"evals/reports/2026/run.md"},
			patterns:     DefaultGeneratedFilePatterns,
			want:         []string{"evals/reports/2026/run.md"},
		},
		{
			name:         "ordinary file is allowed",
			changedFiles: []string{"internal/app/orchestrator/decision.go", "go.mod"},
			patterns:     DefaultGeneratedFilePatterns,
		},
		{
			name:     "empty changed files list is allowed",
			patterns: DefaultGeneratedFilePatterns,
		},
		{
			name:         "paths are normalised and deduped",
			changedFiles: []string{" ./docs/tasks/HD-007.md ", "docs/tasks/HD-007.md", "docs\\ROADMAP_STATUS.md"},
			patterns:     DefaultGeneratedFilePatterns,
			want:         []string{"docs/tasks/HD-007.md", "docs/ROADMAP_STATUS.md"},
		},
		{
			name:         "custom patterns replace the defaults",
			changedFiles: []string{"generated/x.go", "docs/ROADMAP_STATUS.md"},
			patterns:     []string{"generated/*.go"},
			want:         []string{"generated/x.go"},
		},
		{
			name:         "empty patterns allow everything",
			changedFiles: []string{"docs/ROADMAP_STATUS.md"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filesBefore := append([]string(nil), tt.changedFiles...)
			patternsBefore := append([]string(nil), tt.patterns...)

			got := GeneratedFileEdits(tt.changedFiles, tt.patterns)

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("edits = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(tt.changedFiles, filesBefore) {
				t.Fatalf("changedFiles mutated: %q", tt.changedFiles)
			}
			if !reflect.DeepEqual(tt.patterns, patternsBefore) {
				t.Fatalf("patterns mutated: %q", tt.patterns)
			}
		})
	}
}

// TestDecideRejectsManualEditOfGeneratedFile proves the rejection wins over a
// ready verdict and over an outstanding dependency: a hand-edited artifact
// must never reach the merge path.
func TestDecideRejectsManualEditOfGeneratedFile(t *testing.T) {
	w := decisionWatch(&AutoMergePolicy{WaitFor: []string{"dep:a"}})
	w.ChangedFiles = []string{"internal/app/orchestrator/decision.go", "evals/reports/hd06-generated-files.md"}
	deps := &decisionDeps{inMain: map[string]bool{}}

	got := Decide(w, deps, CIState{})

	if got.Kind != DecisionRejectGeneratedEdit {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionRejectGeneratedEdit)
	}
	if got.Reason != ReasonGeneratedFileEdited {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonGeneratedFileEdited)
	}
	if !reflect.DeepEqual(got.GeneratedFiles, []string{"evals/reports/hd06-generated-files.md"}) {
		t.Fatalf("generatedFiles = %q", got.GeneratedFiles)
	}
	if len(deps.calls) != 0 {
		t.Fatalf("rejection must short-circuit dependency resolution: %v", deps.calls)
	}
	if got.WatchID != w.ID || got.PR != w.PR || got.Revision != w.Revision {
		t.Fatalf("identity = %q/%d/%d", got.WatchID, got.PR, got.Revision)
	}
}

func TestDecideAcceptsOrdinaryChangedFile(t *testing.T) {
	w := decisionWatch(nil)
	w.ChangedFiles = []string{"internal/app/orchestrator/decision.go", "README.md", "docs/tasks/HD-007.md.tmpl"}

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionMerge {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionMerge)
	}
	if len(got.GeneratedFiles) != 0 {
		t.Fatalf("generatedFiles on merge = %q", got.GeneratedFiles)
	}
}

func TestDecideAllowsEmptyChangedFiles(t *testing.T) {
	w := decisionWatch(nil)

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionMerge {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionMerge)
	}
}

// TestDecideBlockedWinsOverGeneratedEdit documents the priority: a blocked
// Watch keeps its FIX/escalation path, which never merges either.
func TestDecideBlockedWinsOverGeneratedEdit(t *testing.T) {
	w := decisionWatch(nil)
	w.Blocked = &BlockedInfo{Reason: "чужой тест"}
	w.ChangedFiles = []string{"docs/ROADMAP_STATUS.md"}

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionBlockedFixAgent {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionBlockedFixAgent)
	}
}
