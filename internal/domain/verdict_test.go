package domain

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

const (
	testSHA = "abc123def"
	testNew = "new456sha"
)

var (
	testAt       = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	testDeadline = testAt.Add(-time.Minute)
)

// readySnapshot is a snapshot that satisfies every condition of table row 7.
// Individual tests mutate one field at a time to isolate a rule.
func readySnapshot() Snapshot {
	return Snapshot{
		State:            "OPEN",
		IsDraft:          false,
		HeadSHA:          testSHA,
		WatchHeadSHA:     testSHA,
		Rollup:           "SUCCESS",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		Checks: map[string]Check{
			"ci-build": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true},
		},
		SuitesValid: true,
	}
}

func targets(kind WakeKind, roles ...Role) []WakeTarget {
	out := make([]WakeTarget, 0, len(roles))
	for _, role := range roles {
		out = append(out, WakeTarget{Role: role, Kind: kind})
	}
	return out
}

func allTargets(kind WakeKind) []WakeTarget {
	return targets(kind, RoleExecutor, RoleOrchestrator)
}

// TestEvaluateDecisionTable covers exactly the ten rows of WORKER-PROMPT §4.6
// in priority order. Each case names its row so a failure points at the rule.
func TestEvaluateDecisionTable(t *testing.T) {
	tests := []struct {
		row  int
		name string
		s    Snapshot
		p    Policy
		want Verdict
		wake []WakeTarget
	}{
		{
			row: 1, name: "MERGED is terminal",
			s:    func() Snapshot { s := readySnapshot(); s.State = "MERGED"; return s }(),
			want: VerdictMerged, wake: allTargets(WakePointer),
		},
		{
			row: 2, name: "head changed creates a successor watch",
			s:    func() Snapshot { s := readySnapshot(); s.HeadSHA = testNew; return s }(),
			want: VerdictWatching, wake: allTargets(WakeHeadChanged),
		},
		{
			row: 3, name: "DIRTY forces rebase",
			s:    func() Snapshot { s := readySnapshot(); s.MergeStateStatus = "DIRTY"; return s }(),
			want: VerdictRebase, wake: targets(WakePointer, RoleExecutor),
		},
		{
			row: 4, name: "BEHIND with requireUpToDate forces rebase",
			s:    func() Snapshot { s := readySnapshot(); s.MergeStateStatus = "BEHIND"; return s }(),
			p:    Policy{RequireUpToDate: true},
			want: VerdictRebase, wake: targets(WakePointer, RoleExecutor),
		},
		{
			row: 5, name: "flaky failure under budget retries",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-flaky"}
				s.Flaky = FlakyConfig{
					Names:      []string{"ci-*"},
					MaxRetries: 2,
					Used:       map[string]int{"ci-flaky": 0},
				}
				return s
			}(),
			want: VerdictRetry,
			wake: []WakeTarget{
				{Role: RoleExecutor, RerunChecks: []string{"ci-flaky"}},
				{Role: RoleOrchestrator},
			},
		},
		{
			row: 6, name: "non-flaky failure keeps working",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "ERROR"
				s.Failures = []string{"ci-stable"}
				s.Flaky = FlakyConfig{
					// "ci-stable" is not flaky, so row 5 cannot fire even
					// though the budget is unused. A glob of "ci-*" here
					// would match it and turn this into a retry.
					Names:      []string{"ci-flaky"},
					MaxRetries: 2,
					Used:       map[string]int{},
				}
				return s
			}(),
			want: VerdictKeepWorking, wake: targets(WakePointer, RoleExecutor),
		},
		{
			row: 7, name: "clean success is ready",
			s:    readySnapshot(),
			want: VerdictReady, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			row: 8, name: "UNKNOWN mergeable pings REST even past deadline",
			s: func() Snapshot {
				s := readySnapshot()
				s.Mergeable = "UNKNOWN"
				s.At = testAt
				s.DeadlineAt = testDeadline
				return s
			}(),
			want: VerdictWatching, wake: []WakeTarget{{Kind: WakeRestPin}},
		},
		{
			row: 9, name: "deadline times out through orchestrator",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "PENDING"
				s.At = testAt
				s.DeadlineAt = testDeadline
				return s
			}(),
			want: VerdictTimeout, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			row: 10, name: "anything else watches silently",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "PENDING"
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
	}

	if len(tests) != 10 {
		t.Fatalf("decision table must have exactly 10 rows, got %d", len(tests))
	}
	for i, tt := range tests {
		if tt.row != i+1 {
			t.Fatalf("case %d labels row %d, want %d", i, tt.row, i+1)
		}
		t.Run(tt.name, func(t *testing.T) {
			got, wake := Evaluate(tt.s, tt.p)
			if got != tt.want {
				t.Errorf("verdict = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(wake, tt.wake) {
				t.Errorf("wake targets = %#v, want %#v", wake, tt.wake)
			}
		})
	}
}

// A cache keyed by SHA alone is not enough: a re-run keeps the SHA but resets
// suites. With an unvalidated suite composition ready must never be returned.
func TestEvaluateNoReadyWithoutSuiteValidation(t *testing.T) {
	s := readySnapshot()
	s.SuitesValid = false

	got, wake := Evaluate(s, Policy{})
	if got == VerdictReady {
		t.Fatal("ready returned for an unvalidated suite composition")
	}
	if got != VerdictWatching || wake != nil {
		t.Fatalf("got (%q, %#v), want (watching, nil)", got, wake)
	}
}

// A repeated watching verdict must not move the revision or stage an
// obligation. Evaluate yields no targets and Transition is a no-op.
func TestEvaluateWatchingDoesNotWakeOrChangeRevision(t *testing.T) {
	s := readySnapshot()
	s.Rollup = "PENDING"

	got, wake := Evaluate(s, Policy{})
	if got != VerdictWatching || wake != nil {
		t.Fatalf("got (%q, %#v), want (watching, nil)", got, wake)
	}

	w := Watch{
		ID: "w1", Status: StatusWatching, Verdict: VerdictWatching,
		HeadSHA: testSHA, Revision: 7,
	}
	next, effects := Transition(w, VerdictEvent(got, testAt))
	if next.Revision != w.Revision {
		t.Errorf("revision = %d, want %d", next.Revision, w.Revision)
	}
	if next.Status != StatusWatching {
		t.Errorf("status = %q, want watching", next.Status)
	}
	if len(effects) != 0 {
		t.Errorf("effects = %#v, want none", effects)
	}
}

// The flaky retry is bounded per check per SHA. Simulating the caller
// consuming the budget shows the verdict switches to keep_working and can
// never return to retry, so an infinite re-run loop is impossible.
func TestEvaluateFlakyRetryBudgetIsFinite(t *testing.T) {
	s := readySnapshot()
	s.Rollup = "FAILURE"
	s.Failures = []string{"ci-flaky"}
	s.Flaky = FlakyConfig{
		Names:      []string{"ci-*"},
		MaxRetries: 2,
		Used:       map[string]int{"ci-flaky": 0},
	}

	for i := 0; i < 5; i++ {
		got, wake := Evaluate(s, Policy{})
		if i < s.Flaky.MaxRetries {
			if got != VerdictRetry {
				t.Fatalf("iteration %d: verdict = %q, want retry", i, got)
			}
			if !reflect.DeepEqual(wake[0].RerunChecks, []string{"ci-flaky"}) {
				t.Fatalf("iteration %d: rerun checks = %#v", i, wake[0].RerunChecks)
			}
			s.Flaky.Used["ci-flaky"]++
			continue
		}
		if got != VerdictKeepWorking {
			t.Fatalf("iteration %d: verdict = %q, want keep_working", i, got)
		}
		if !reflect.DeepEqual(wake, targets(WakePointer, RoleExecutor)) {
			t.Fatalf("iteration %d: wake = %#v, want executor only", i, wake)
		}
	}
}

// The remaining §4.6 fixtures: fork, missing required, harmless conclusions,
// pending conclusions, draft, BEHIND under both policies, terminal states and
// a suite reset after rerequest.
func TestEvaluateFixtures(t *testing.T) {
	tests := []struct {
		name string
		s    Snapshot
		p    Policy
		want Verdict
		wake []WakeTarget
	}{
		{
			name: "fork PR keyed by head SHA",
			s:    readySnapshot(),
			want: VerdictReady, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			name: "missing required check times out",
			s: func() Snapshot {
				s := readySnapshot()
				s.MissingRequired = true
				s.At = testAt
				s.DeadlineAt = testDeadline
				return s
			}(),
			want: VerdictTimeout, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			name: "SKIPPED and NEUTRAL conclusions are allowed",
			s: func() Snapshot {
				s := readySnapshot()
				s.Checks["ci-optional"] = Check{State: "COMPLETED", Conclusion: "SKIPPED"}
				s.Checks["ci-legacy"] = Check{State: "COMPLETED", Conclusion: "NEUTRAL"}
				return s
			}(),
			want: VerdictReady, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			name: "ACTION_REQUIRED is pending",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "ACTION_REQUIRED"
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
		{
			name: "WAITING is pending",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "WAITING"
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
		{
			name: "QUEUED is pending",
			s: func() Snapshot {
				s := readySnapshot()
				s.Rollup = "QUEUED"
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
		{
			name: "draft is never ready",
			s: func() Snapshot {
				s := readySnapshot()
				s.IsDraft = true
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
		{
			name: "BEHIND with requireUpToDate rebase",
			s: func() Snapshot {
				s := readySnapshot()
				s.MergeStateStatus = "BEHIND"
				return s
			}(),
			p:    Policy{RequireUpToDate: true},
			want: VerdictRebase, wake: targets(WakePointer, RoleExecutor),
		},
		{
			name: "BEHIND without requireUpToDate is ready",
			s: func() Snapshot {
				s := readySnapshot()
				s.MergeStateStatus = "BEHIND"
				return s
			}(),
			want: VerdictReady, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			name: "CLOSED is terminal",
			s: func() Snapshot {
				s := readySnapshot()
				s.State = "CLOSED"
				return s
			}(),
			want: VerdictClosed, wake: allTargets(WakePointer),
		},
		{
			name: "rerequested suites reset to watching",
			s: func() Snapshot {
				s := readySnapshot()
				s.SuitesValid = false // stale SUCCESS from the cache
				s.Rollup = "QUEUED"
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
		{
			name: "ignoreReviews overrides CHANGES_REQUESTED",
			s: func() Snapshot {
				s := readySnapshot()
				s.ReviewDecision = "CHANGES_REQUESTED"
				return s
			}(),
			p:    Policy{IgnoreReviews: true},
			want: VerdictReady, wake: targets(WakePointer, RoleOrchestrator),
		},
		{
			name: "CHANGES_REQUESTED blocks ready",
			s: func() Snapshot {
				s := readySnapshot()
				s.ReviewDecision = "CHANGES_REQUESTED"
				return s
			}(),
			want: VerdictWatching, wake: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, wake := Evaluate(tt.s, tt.p)
			if got != tt.want {
				t.Errorf("verdict = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(wake, tt.wake) {
				t.Errorf("wake targets = %#v, want %#v", wake, tt.wake)
			}
		})
	}
}

// The engine must not mutate its input: Flaky.Used is owned by the caller.
func TestEvaluateDoesNotMutateSnapshot(t *testing.T) {
	s := readySnapshot()
	s.Rollup = "FAILURE"
	s.Failures = []string{"ci-flaky"}
	s.Flaky = FlakyConfig{
		Names:      []string{"ci-*"},
		MaxRetries: 2,
		Used:       map[string]int{"ci-flaky": 0},
	}
	before := s.Flaky.Used["ci-flaky"]

	if got, _ := Evaluate(s, Policy{}); got != VerdictRetry {
		t.Fatalf("verdict = %q, want retry", got)
	}
	if s.Flaky.Used["ci-flaky"] != before {
		t.Errorf("used = %d, want unchanged %d", s.Flaky.Used["ci-flaky"], before)
	}
}

// A conflict result carries a machine type and a clean list of file paths.
// The constructor normalizes the paths so callers can branch on them.
func TestNewConflictResult(t *testing.T) {
	tests := []struct {
		name    string
		typ     ConflictType
		paths   []string
		message string
		want    ConflictResult
	}{
		{
			name:    "merge conflict trims and dedups paths",
			typ:     ConflictTypeMerge,
			paths:   []string{" src/a.go ", "", "src/a.go", "docs/b.md"},
			message: "merge conflict",
			want: ConflictResult{
				Type:    ConflictTypeMerge,
				Paths:   []string{"src/a.go", "docs/b.md"},
				Message: "merge conflict",
			},
		},
		{
			name:  "no paths still yields an empty slice",
			typ:   ConflictTypeFileOwnership,
			paths: nil,
			want: ConflictResult{
				Type:  ConflictTypeFileOwnership,
				Paths: []string{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewConflictResult(tt.typ, tt.paths, tt.message)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NewConflictResult = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// The JSON shape is part of the MCP response contract: type and paths are
// always present and message is omitted when empty.
func TestConflictResultJSON(t *testing.T) {
	cases := []struct {
		name string
		in   ConflictResult
		want string
	}{
		{
			name: "merge with paths",
			in:   NewConflictResult(ConflictTypeMerge, []string{"src/a.go", "docs/b.md"}, ""),
			want: `{"type":"merge","paths":["src/a.go","docs/b.md"]}`,
		},
		{
			name: "file ownership with message",
			in:   NewConflictResult(ConflictTypeFileOwnership, []string{"shared.go"}, "two agents"),
			want: `{"type":"file_ownership","paths":["shared.go"],"message":"two agents"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Fatalf("json = %s, want %s", b, tc.want)
			}
		})
	}
}

func TestConflictResultEmpty(t *testing.T) {
	if !(ConflictResult{}).Empty() {
		t.Fatal("zero ConflictResult must be empty")
	}
	if NewConflictResult(ConflictTypeMerge, nil, "").Empty() {
		t.Fatal("a result with a type is not empty")
	}
}
