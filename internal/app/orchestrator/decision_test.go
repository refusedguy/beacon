package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

// TestDecideChecksRulesAgainstGitRef proves the check reads the ref source, not
// the working copy: ChangedFiles is empty and only the ref supplies the file
// that triggers the rejection. The decision carries the ref for reproduction.
func TestDecideChecksRulesAgainstGitRef(t *testing.T) {
	var calledWith []string
	src := RefSource{
		RefName: "refs/heads/fix/hd057",
		Resolve: func(_ context.Context, ref string) ([]string, error) {
			calledWith = append(calledWith, ref)
			return []string{"evals/reports/hd057.md"}, nil
		},
	}
	w := decisionWatch(nil)
	w.Source = src

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionRejectGeneratedEdit {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionRejectGeneratedEdit)
	}
	if !got.ByRef || got.SourceRef != "refs/heads/fix/hd057" {
		t.Fatalf("byRef/sourceRef = %v/%q", got.ByRef, got.SourceRef)
	}
	if !reflect.DeepEqual(got.GeneratedFiles, []string{"evals/reports/hd057.md"}) {
		t.Fatalf("generatedFiles = %q", got.GeneratedFiles)
	}
	if !reflect.DeepEqual(calledWith, []string{"refs/heads/fix/hd057"}) {
		t.Fatalf("ref resolver calls = %q", calledWith)
	}
}

// TestDecideChecksRulesAgainstChangedFiles keeps the diff-backed path: with no
// Source the Watch falls back to ChangedFiles and the decision is not marked
// as ref-backed.
func TestDecideChecksRulesAgainstChangedFiles(t *testing.T) {
	w := decisionWatch(nil)
	w.ChangedFiles = []string{"evals/reports/hd057.md"}

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionRejectGeneratedEdit {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionRejectGeneratedEdit)
	}
	if got.ByRef || got.SourceRef != "" {
		t.Fatalf("diff-backed decision marked by ref: byRef=%v sourceRef=%q", got.ByRef, got.SourceRef)
	}
	if !reflect.DeepEqual(got.GeneratedFiles, []string{"evals/reports/hd057.md"}) {
		t.Fatalf("generatedFiles = %q", got.GeneratedFiles)
	}
}

// TestDecideEmptyRefDoesNotMarkByRef proves an empty or whitespace-only ref is
// treated as no ref: no resolver call, no rejection and no ref flag.
func TestDecideEmptyRefDoesNotMarkByRef(t *testing.T) {
	called := false
	w := decisionWatch(nil)
	w.Source = RefSource{
		RefName: "  ",
		Resolve: func(context.Context, string) ([]string, error) {
			called = true
			return nil, errors.New("resolver must not run for an empty ref")
		},
	}

	got := Decide(w, nil, CIState{})

	if got.Kind != DecisionMerge {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionMerge)
	}
	if got.ByRef || got.SourceRef != "" {
		t.Fatalf("empty ref marked the decision by ref: byRef=%v sourceRef=%q", got.ByRef, got.SourceRef)
	}
	if called {
		t.Fatal("empty ref must not call the resolver")
	}
}

// TestDecideSourceErrorFailsClosed proves a failed ref resolution cannot fall
// through to a merge or to dependency resolution.
func TestDecideSourceErrorFailsClosed(t *testing.T) {
	w := decisionWatch(&AutoMergePolicy{WaitFor: []string{"dep:a"}})
	w.Source = RefSource{
		RefName: "refs/heads/fix/hd057",
		Resolve: func(context.Context, string) ([]string, error) {
			return nil, errors.New("ref not found")
		},
	}
	deps := &decisionDeps{inMain: map[string]bool{"dep:a": true}}

	got := Decide(w, deps, CIState{})

	if got.Kind != DecisionSourceError {
		t.Fatalf("kind = %q, want %q", got.Kind, DecisionSourceError)
	}
	if got.Reason != ReasonSourceUnavailable {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonSourceUnavailable)
	}
	if !got.ByRef || got.SourceRef != "refs/heads/fix/hd057" {
		t.Fatalf("byRef/sourceRef = %v/%q", got.ByRef, got.SourceRef)
	}
	if !strings.Contains(got.SourceError, "ref not found") {
		t.Fatalf("sourceError = %q", got.SourceError)
	}
	if len(deps.calls) != 0 {
		t.Fatalf("source error must short-circuit dependency resolution: %v", deps.calls)
	}
}

// taskClosureFake is the port fake for AuditCompletion. It records every task
// it is asked about so the tests can prove the audit queried the whole plan.
type taskClosureFake struct {
	closed map[string]bool
	err    map[string]error
	calls  []string
}

func (f *taskClosureFake) Closed(_ context.Context, id string) (bool, error) {
	f.calls = append(f.calls, id)
	if err := f.err[id]; err != nil {
		return false, err
	}
	return f.closed[id], nil
}

func TestAuditCompletionAllTasksClosed(t *testing.T) {
	plan := []string{"HD-013", "HD-014", "HD-015"}
	closure := &taskClosureFake{closed: map[string]bool{
		"HD-013": true, "HD-014": true, "HD-015": true,
	}}

	got, err := AuditCompletion(context.Background(), plan, closure)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !got.Complete {
		t.Fatalf("complete = false, want true")
	}
	if len(got.Missing) != 0 {
		t.Fatalf("missing = %q, want empty", got.Missing)
	}
	if !reflect.DeepEqual(closure.calls, plan) {
		t.Fatalf("closure calls = %q, want %q", closure.calls, plan)
	}
}

func TestAuditCompletionOpenTaskBlocks(t *testing.T) {
	closure := &taskClosureFake{closed: map[string]bool{"HD-013": true}}

	got, err := AuditCompletion(context.Background(), []string{"HD-013", "HD-014", "HD-015"}, closure)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.Complete {
		t.Fatal("partial completion must not be complete")
	}
	if !reflect.DeepEqual(got.Missing, []string{"HD-014", "HD-015"}) {
		t.Fatalf("missing = %q, want [HD-014 HD-015]", got.Missing)
	}
}

func TestAuditCompletionEmptyPlan(t *testing.T) {
	closure := &taskClosureFake{}

	got, err := AuditCompletion(context.Background(), nil, closure)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !got.Complete {
		t.Fatal("empty plan must be complete")
	}
	if len(got.Missing) != 0 {
		t.Fatalf("missing = %q, want empty", got.Missing)
	}
	if len(closure.calls) != 0 {
		t.Fatalf("empty plan must not query the port: %v", closure.calls)
	}
}

func TestAuditCompletionPortErrorFailsClosed(t *testing.T) {
	closure := &taskClosureFake{
		closed: map[string]bool{"HD-013": true},
		err:    map[string]error{"HD-014": errors.New("task store down")},
	}

	got, err := AuditCompletion(context.Background(), []string{"HD-013", "HD-014", "HD-015"}, closure)

	if err == nil {
		t.Fatal("err = nil, want the port error")
	}
	if !strings.Contains(err.Error(), "HD-014") || !strings.Contains(err.Error(), "task store down") {
		t.Fatalf("err = %q, want task id and port cause", err)
	}
	if got.Complete {
		t.Fatal("port error must leave the audit incomplete")
	}
	if !reflect.DeepEqual(got.Missing, []string{"HD-014"}) {
		t.Fatalf("missing = %q, want [HD-014]", got.Missing)
	}
	if !reflect.DeepEqual(closure.calls, []string{"HD-013", "HD-014"}) {
		t.Fatalf("closure calls = %q, want [HD-013 HD-014]", closure.calls)
	}
}

func TestAuditCompletionTrimsAndDedupesPlan(t *testing.T) {
	closure := &taskClosureFake{closed: map[string]bool{"HD-013": true, "HD-014": true}}

	got, err := AuditCompletion(context.Background(), []string{" HD-013 ", "", "HD-013", "HD-014"}, closure)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !got.Complete {
		t.Fatal("deduplicated closed plan must be complete")
	}
	if !reflect.DeepEqual(closure.calls, []string{"HD-013", "HD-014"}) {
		t.Fatalf("closure calls = %q, want [HD-013 HD-014]", closure.calls)
	}
}

func TestAuditCompletionNilClosureFailsClosed(t *testing.T) {
	got, err := AuditCompletion(context.Background(), []string{"HD-013", "HD-014"}, nil)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.Complete {
		t.Fatal("nil closure must not complete a non-empty plan")
	}
	if !reflect.DeepEqual(got.Missing, []string{"HD-013", "HD-014"}) {
		t.Fatalf("missing = %q, want the whole plan", got.Missing)
	}
}

// slotVerifier is the artifact port fake. ready gates the answer, so a test can
// hold the artifact back and then let it land. calls records every check.
type slotVerifier struct {
	ready bool
	err   error
	calls []SlotToken
}

func (v *slotVerifier) Verified(_ context.Context, t SlotToken, _ SlotArtifact) (bool, error) {
	v.calls = append(v.calls, t)
	if v.err != nil {
		return false, v.err
	}
	return v.ready, nil
}

func TestSlotLedgerOneSignalOneSpawn(t *testing.T) {
	l := NewSlotLedger()
	v := &slotVerifier{ready: true}

	first, inc := l.Issue(SignalReportToOrchestrator, "watch:D1", 7, 42)
	if inc != nil || first.WatchID != "watch:D1" || first.Revision != 7 || first.PR != 42 {
		t.Fatalf("first issue = %+v, incident = %+v", first, inc)
	}
	if !l.IsLive("watch:D1", 7) {
		t.Fatal("first signal did not open a slot")
	}

	second, inc := l.Issue(SignalReportToOrchestrator, "watch:D1", 7, 42)
	if inc == nil || inc.Kind != SlotDuplicateSignal {
		t.Fatalf("second signal incident = %+v, want %s", inc, SlotDuplicateSignal)
	}
	if second != (SlotToken{}) {
		t.Fatalf("second signal opened a slot: %+v", second)
	}
	if len(l.live) != 1 {
		t.Fatalf("two signals opened %d slots, want 1", len(l.live))
	}

	burned, inc := l.Burn(context.Background(), first, SlotArtifact{PR: 42}, v)
	if inc != nil || !burned {
		t.Fatalf("burn = %v, incident = %+v, want true/nil", burned, inc)
	}
	if l.IsLive("watch:D1", 7) {
		t.Fatal("burned token still holds a slot")
	}

	third, inc := l.Issue(SignalReportToOrchestrator, "watch:D1", 7, 42)
	if inc != nil || third == (SlotToken{}) {
		t.Fatalf("signal after burn = %+v, incident = %+v, want a fresh token", third, inc)
	}
}

// TestSlotLedgerDelayedArtifact holds the artifact back after the first signal.
// A duplicate signal during the delay must not open a second slot. The slot
// opens again only after the artifact lands and the token burns.
func TestSlotLedgerDelayedArtifact(t *testing.T) {
	l := NewSlotLedger()
	v := &slotVerifier{ready: false}
	signal := SignalReportToOrchestrator

	token, inc := l.Issue(signal, "watch:D1", 7, 42)
	if inc != nil || token == (SlotToken{}) {
		t.Fatalf("first issue = %+v, incident = %+v", token, inc)
	}

	burned, inc := l.Burn(context.Background(), token, SlotArtifact{Plan: "PLAN.md"}, v)
	if inc != nil || burned {
		t.Fatalf("burn with delayed artifact = %v, incident = %+v, want false/nil", burned, inc)
	}
	if !l.IsLive("watch:D1", 7) {
		t.Fatal("delayed artifact freed the slot")
	}

	duplicate, inc := l.Issue(signal, "watch:D1", 7, 42)
	if inc == nil || inc.Kind != SlotDuplicateSignal || duplicate != (SlotToken{}) {
		t.Fatalf("signal during delay = %+v, incident = %+v, want duplicate and no token", duplicate, inc)
	}
	if len(l.live) != 1 {
		t.Fatalf("signal during delay opened %d slots, want 1", len(l.live))
	}

	v.ready = true
	burned, inc = l.Burn(context.Background(), token, SlotArtifact{Plan: "PLAN.md"}, v)
	if inc != nil || !burned {
		t.Fatalf("burn after artifact landed = %v, incident = %+v, want true/nil", burned, inc)
	}
	if len(v.calls) != 2 {
		t.Fatalf("verifier calls = %d, want 2 (delayed, then landed)", len(v.calls))
	}

	if _, inc := l.Issue(signal, "watch:D1", 7, 42); inc != nil {
		t.Fatalf("fresh signal after burn returned an incident: %+v", inc)
	}
	if len(l.live) != 1 {
		t.Fatalf("slot count after burn and new signal = %d, want 1", len(l.live))
	}
}

func TestSlotLedgerOnlyReportSignalOpensSlot(t *testing.T) {
	l := NewSlotLedger()
	for _, signal := range []string{"", "keep_working", "rebase", "retry", "blocked", "wait", "MERGE"} {
		token, inc := l.Issue(signal, "watch:D1", 7, 42)
		if token != (SlotToken{}) || inc != nil {
			t.Fatalf("signal %q opened a slot: %+v, incident %+v", signal, token, inc)
		}
	}
	if len(l.live) != 0 {
		t.Fatalf("non-completion signals opened %d slots, want 0", len(l.live))
	}
}

func TestSlotLedgerBurnWithoutTokenIsIncident(t *testing.T) {
	l := NewSlotLedger()
	v := &slotVerifier{ready: true}
	token := SlotToken{WatchID: "watch:D1", Revision: 7, PR: 42}

	burned, inc := l.Burn(context.Background(), token, SlotArtifact{PR: 42}, v)

	if burned {
		t.Fatal("burn without a token succeeded")
	}
	if inc == nil || inc.Kind != SlotSpawnWithoutToken {
		t.Fatalf("incident = %+v, want %s", inc, SlotSpawnWithoutToken)
	}
	if len(v.calls) != 0 {
		t.Fatalf("verifier called with no token: %d calls", len(v.calls))
	}
}

func TestSlotLedgerBurnKeepsTokenWhenArtifactInvalid(t *testing.T) {
	l := NewSlotLedger()
	v := &slotVerifier{ready: true}
	token, _ := l.Issue(SignalReportToOrchestrator, "watch:D1", 7, 42)

	burned, inc := l.Burn(context.Background(), token, SlotArtifact{}, v)
	if burned || inc != nil {
		t.Fatalf("burn with empty artifact = %v, incident %+v, want false/nil", burned, inc)
	}
	if !l.IsLive("watch:D1", 7) {
		t.Fatal("empty artifact freed the slot")
	}
	if len(v.calls) != 0 {
		t.Fatalf("verifier called for an invalid artifact: %d calls", len(v.calls))
	}
}

func TestSlotLedgerBurnVerifierErrorKeepsSlot(t *testing.T) {
	l := NewSlotLedger()
	v := &slotVerifier{err: errors.New("artifact store down")}
	token, _ := l.Issue(SignalReportToOrchestrator, "watch:D1", 7, 42)

	burned, inc := l.Burn(context.Background(), token, SlotArtifact{PR: 42}, v)
	if burned || inc != nil {
		t.Fatalf("burn with verifier error = %v, incident %+v, want false/nil", burned, inc)
	}
	if !l.IsLive("watch:D1", 7) {
		t.Fatal("verifier error freed the slot")
	}
}

func TestSlotLedgerKeysByWatchAndRevision(t *testing.T) {
	l := NewSlotLedger()
	_, inc := l.Issue(SignalReportToOrchestrator, "watch:D1", 7, 42)
	if inc != nil {
		t.Fatalf("first issue: %+v", inc)
	}

	for _, tc := range []struct {
		name    string
		watchID string
		rev     uint64
		wantInc bool
	}{
		{name: "same watch new revision", watchID: "watch:D1", rev: 8},
		{name: "new watch same revision", watchID: "watch:D2", rev: 7},
		{name: "same watch same revision", watchID: "watch:D1", rev: 7, wantInc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, inc := l.Issue(SignalReportToOrchestrator, tc.watchID, tc.rev, 43)
			if tc.wantInc {
				if inc == nil || inc.Kind != SlotDuplicateSignal || token != (SlotToken{}) {
					t.Fatalf("issue = %+v, incident = %+v, want duplicate and no token", token, inc)
				}
				return
			}
			if inc != nil || token == (SlotToken{}) {
				t.Fatalf("issue = %+v, incident = %+v, want a fresh token", token, inc)
			}
		})
	}
	if len(l.live) != 3 {
		t.Fatalf("slot count = %d, want 3", len(l.live))
	}
}

func TestSlotArtifactValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		art  SlotArtifact
		want bool
	}{
		{name: "plan", art: SlotArtifact{Plan: "PLAN.md"}, want: true},
		{name: "pr", art: SlotArtifact{PR: 42}, want: true},
		{name: "both", art: SlotArtifact{Plan: "PLAN.md", PR: 42}, want: true},
		{name: "empty", art: SlotArtifact{}},
		{name: "blank plan", art: SlotArtifact{Plan: "  "}},
		{name: "zero pr", art: SlotArtifact{PR: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.art.Valid(); got != tc.want {
				t.Fatalf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}
