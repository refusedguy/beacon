package domain

import (
	"encoding/json"
	"math"
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
		Ship:        ShipFacts{Build: true, Audit: true, TDD: true},
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

// TestReviewVerdictConstants pins the canonical journal spellings from ADR
// 0038 §4. Changing one would break the recorded verdict contract.
func TestReviewVerdictConstants(t *testing.T) {
	cases := []struct {
		v    ReviewVerdict
		want string
	}{
		{ReviewShip, "SHIP"},
		{ReviewFixFirst, "FIX-FIRST"},
		{ReviewBlocked, "BLOCKED"},
	}
	for _, tc := range cases {
		if string(tc.v) != tc.want {
			t.Errorf("verdict = %q, want %q", tc.v, tc.want)
		}
	}
}

func TestReviewVerdictValid(t *testing.T) {
	cases := []struct {
		name string
		v    ReviewVerdict
		want bool
	}{
		{"ship", ReviewShip, true},
		{"fix-first", ReviewFixFirst, true},
		{"blocked", ReviewBlocked, true},
		{"zero value", ReviewVerdict(""), false},
		{"unknown", ReviewVerdict("MAYBE"), false},
		{"lowercase is not canonical", ReviewVerdict("ship"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Valid(); got != tc.want {
				t.Fatalf("Valid(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestParseReviewVerdict(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   ReviewVerdict
		wantOK bool
	}{
		{"ship", "SHIP", ReviewShip, true},
		{"fix-first", "FIX-FIRST", ReviewFixFirst, true},
		{"blocked", "BLOCKED", ReviewBlocked, true},
		{"surrounding whitespace", "  FIX-FIRST\n", ReviewFixFirst, true},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
		{"unknown", "APPROVE", "", false},
		{"lowercase is not canonical", "ship", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseReviewVerdict(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ParseReviewVerdict(%q) = (%q, %v), want (%q, %v)",
					tc.in, got, ok, tc.want, tc.wantOK)
			}
			if ok && !got.Valid() {
				t.Fatalf("accepted verdict %q must be valid", got)
			}
		})
	}
}

// TestReviewVerdictJSON records that the verdict serializes to its canonical
// string, which is the form written to the journal.
func TestReviewVerdictJSON(t *testing.T) {
	b, err := json.Marshal(ReviewBlocked)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"BLOCKED"` {
		t.Fatalf("json = %s, want %q", b, `"BLOCKED"`)
	}
}

// TestShipAllowedTruthTable pins all sixteen combinations of the compiled-in
// formula ship ⇒ build ∧ audit ∧ (tdd ∨ trivial). The want column is written
// from the formula by hand so a change to ShipAllowed cannot silently pass.
func TestShipAllowedTruthTable(t *testing.T) {
	cases := []struct {
		name  string
		facts ShipFacts
		want  bool
	}{
		{"no facts", ShipFacts{}, false},
		{"trivial alone", ShipFacts{Trivial: true}, false},
		{"tdd alone", ShipFacts{TDD: true}, false},
		{"tdd and trivial without gates", ShipFacts{TDD: true, Trivial: true}, false},
		{"build only", ShipFacts{Build: true}, false},
		{"build and trivial without audit", ShipFacts{Build: true, Trivial: true}, false},
		{"build and tdd without audit", ShipFacts{Build: true, TDD: true}, false},
		{"build tdd trivial without audit", ShipFacts{Build: true, TDD: true, Trivial: true}, false},
		{"audit only", ShipFacts{Audit: true}, false},
		{"audit and trivial without build", ShipFacts{Audit: true, Trivial: true}, false},
		{"audit and tdd without build", ShipFacts{Audit: true, TDD: true}, false},
		{"audit tdd trivial without build", ShipFacts{Audit: true, TDD: true, Trivial: true}, false},
		{"build and audit without alt", ShipFacts{Build: true, Audit: true}, false},
		{"build audit trivial", ShipFacts{Build: true, Audit: true, Trivial: true}, true},
		{"build audit tdd", ShipFacts{Build: true, Audit: true, TDD: true}, true},
		{"all facts", ShipFacts{Build: true, Audit: true, TDD: true, Trivial: true}, true},
	}
	if len(cases) != 16 {
		t.Fatalf("truth table must have 16 rows, got %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShipAllowed(tc.facts); got != tc.want {
				t.Fatalf("ShipAllowed(%+v) = %v, want %v", tc.facts, got, tc.want)
			}
		})
	}
}

// The rule is a pure function: the same facts give the same verdict on every
// call, with no external state that could be tuned between calls.
func TestShipAllowedDeterministic(t *testing.T) {
	facts := ShipFacts{Build: true, Audit: true, TDD: true}
	want := ShipAllowed(facts)
	for i := 0; i < 10; i++ {
		if got := ShipAllowed(facts); got != want {
			t.Fatalf("call %d = %v, want %v", i, got, want)
		}
	}
	if !want {
		t.Fatal("a change with build, audit and tdd must ship")
	}
}

// Trivial is an explicit condition in code, not a free opinion that can
// override a failed gate. It substitutes for TDD only, never for build or
// audit.
func TestShipAllowedTrivialDoesNotOverrideGates(t *testing.T) {
	cases := []struct {
		name  string
		facts ShipFacts
		want  bool
	}{
		{"trivial cannot replace build", ShipFacts{Audit: true, Trivial: true}, false},
		{"trivial cannot replace audit", ShipFacts{Build: true, Trivial: true}, false},
		{"trivial replaces tdd only", ShipFacts{Build: true, Audit: true, Trivial: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShipAllowed(tc.facts); got != tc.want {
				t.Fatalf("ShipAllowed(%+v) = %v, want %v", tc.facts, got, tc.want)
			}
		})
	}
}

// The formula cannot be configured: its only input is ShipFacts of four bool
// facts and it returns a single bool. There is no policy, config or prompt
// parameter to dispute the rule with, and ShipFacts carries no config field.
func TestShipAllowedHasNoConfigurationInput(t *testing.T) {
	fn := reflect.TypeOf(ShipAllowed)
	if fn.NumIn() != 1 || fn.In(0) != reflect.TypeOf(ShipFacts{}) {
		t.Fatalf("ShipAllowed input = %v, want exactly one ShipFacts", fn)
	}
	if fn.NumOut() != 1 || fn.Out(0) != reflect.TypeOf(true) {
		t.Fatalf("ShipAllowed output = %v, want exactly one bool", fn)
	}

	facts := reflect.TypeOf(ShipFacts{})
	if facts.NumField() != 4 {
		t.Fatalf("ShipFacts has %d fields, want 4", facts.NumField())
	}
	boolType := reflect.TypeOf(true)
	for i := 0; i < facts.NumField(); i++ {
		if facts.Field(i).Type != boolType {
			t.Errorf("ShipFacts.%s = %v, want bool", facts.Field(i).Name, facts.Field(i).Type)
		}
	}
}

// TestEvaluateRunsShipFormula proves the compiled-in formula is on the live
// path to the ready verdict, not dead code. Every case starts from a snapshot
// that satisfies all other row-7 conditions and changes only the ship facts.
// If the ShipAllowed call is removed from Evaluate, every "not allowed" case
// below turns ready and this test fails.
func TestEvaluateRunsShipFormula(t *testing.T) {
	cases := []struct {
		name string
		ship ShipFacts
		want Verdict
	}{
		{"build audit tdd is ready", ShipFacts{Build: true, Audit: true, TDD: true}, VerdictReady},
		{"build audit trivial is ready", ShipFacts{Build: true, Audit: true, Trivial: true}, VerdictReady},
		{"all facts are ready", ShipFacts{Build: true, Audit: true, TDD: true, Trivial: true}, VerdictReady},
		{"missing build blocks ready", ShipFacts{Audit: true, TDD: true}, VerdictWatching},
		{"missing audit blocks ready", ShipFacts{Build: true, TDD: true}, VerdictWatching},
		{"no alternative blocks ready", ShipFacts{Build: true, Audit: true}, VerdictWatching},
		{"trivial cannot replace build", ShipFacts{Audit: true, Trivial: true}, VerdictWatching},
		{"trivial cannot replace audit", ShipFacts{Build: true, Trivial: true}, VerdictWatching},
		{"no facts block ready", ShipFacts{}, VerdictWatching},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := readySnapshot()
			s.Ship = tc.ship
			got, wake := Evaluate(s, Policy{})
			if got != tc.want {
				t.Fatalf("Evaluate with ship facts %+v = %q, want %q", tc.ship, got, tc.want)
			}
			if tc.want == VerdictWatching && wake != nil {
				t.Fatalf("a blocked ship must watch silently, got wake %#v", wake)
			}
		})
	}
}

// Evaluate stays pure with ship facts on the snapshot: repeated calls over the
// same input give the same verdict and wake targets, and never mutate them.
func TestEvaluateShipFactsDeterministic(t *testing.T) {
	s := readySnapshot()
	s.Ship = ShipFacts{Build: true, Audit: true, TDD: true}
	want, wantWake := Evaluate(s, Policy{})
	if want != VerdictReady {
		t.Fatalf("verdict = %q, want ready", want)
	}
	for i := 0; i < 10; i++ {
		got, wake := Evaluate(s, Policy{})
		if got != want || !reflect.DeepEqual(wake, wantWake) {
			t.Fatalf("call %d = (%q, %#v), want (%q, %#v)", i, got, wake, want, wantWake)
		}
	}
}

// TestGateMetricFlagTruthTable pins the two-condition rule of ADR 0049 §5. The
// flag is set only when the median ratio is not below the threshold AND the
// p-value is below alpha. Either condition alone leaves it unset.
func TestGateMetricFlagTruthTable(t *testing.T) {
	const (
		threshold = 2.0
		alpha     = 0.05
	)
	cases := []struct {
		name  string
		facts GateMetricFacts
		want  bool
	}{
		{
			name:  "both conditions hold sets the flag",
			facts: GateMetricFacts{MedianRatio: 2.07, Threshold: threshold, PValue: 0.0001, Alpha: alpha},
			want:  true,
		},
		{
			name:  "magnitude only leaves the flag unset",
			facts: GateMetricFacts{MedianRatio: 2.07, Threshold: threshold, PValue: 0.2, Alpha: alpha},
			want:  false,
		},
		{
			name:  "significance only leaves the flag unset",
			facts: GateMetricFacts{MedianRatio: 1.2, Threshold: threshold, PValue: 0.0001, Alpha: alpha},
			want:  false,
		},
		{
			name:  "neither condition holds leaves the flag unset",
			facts: GateMetricFacts{MedianRatio: 1.2, Threshold: threshold, PValue: 0.2, Alpha: alpha},
			want:  false,
		},
		{
			name:  "ratio exactly at threshold counts as magnitude",
			facts: GateMetricFacts{MedianRatio: threshold, Threshold: threshold, PValue: 0.01, Alpha: alpha},
			want:  true,
		},
		{
			name:  "ratio just below threshold fails magnitude",
			facts: GateMetricFacts{MedianRatio: 1.999, Threshold: threshold, PValue: 0.01, Alpha: alpha},
			want:  false,
		},
		{
			name:  "p-value exactly at alpha fails significance",
			facts: GateMetricFacts{MedianRatio: 3.0, Threshold: threshold, PValue: alpha, Alpha: alpha},
			want:  false,
		},
		{
			name:  "p-value just below alpha counts as significance",
			facts: GateMetricFacts{MedianRatio: 3.0, Threshold: threshold, PValue: 0.049, Alpha: alpha},
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GateMetricFlag(tc.facts); got != tc.want {
				t.Fatalf("GateMetricFlag(%+v) = %v, want %v", tc.facts, got, tc.want)
			}
		})
	}
}

// An unknown or unconfigured quantity must not set the flag. Comparisons with
// NaN are always false, and a zero-value facts struct cannot satisfy both
// conditions, so a missing observation fails closed.
func TestGateMetricFlagFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		facts GateMetricFacts
	}{
		{"zero facts", GateMetricFacts{}},
		{"NaN ratio", GateMetricFacts{MedianRatio: math.NaN(), Threshold: 2.0, PValue: 0.01, Alpha: 0.05}},
		{"NaN p-value", GateMetricFacts{MedianRatio: 3.0, Threshold: 2.0, PValue: math.NaN(), Alpha: 0.05}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if GateMetricFlag(tc.facts) {
				t.Fatalf("GateMetricFlag(%+v) = true, want false", tc.facts)
			}
		})
	}
}

// The rule is fixed: one facts struct in, one bool out, no policy parameter to
// dispute it with, and the facts carry only measured quantities. It stays pure
// across repeated calls.
func TestGateMetricFlagHasNoConfigurationInput(t *testing.T) {
	fn := reflect.TypeOf(GateMetricFlag)
	if fn.NumIn() != 1 || fn.In(0) != reflect.TypeOf(GateMetricFacts{}) {
		t.Fatalf("GateMetricFlag input = %v, want exactly one GateMetricFacts", fn)
	}
	if fn.NumOut() != 1 || fn.Out(0) != reflect.TypeOf(true) {
		t.Fatalf("GateMetricFlag output = %v, want exactly one bool", fn)
	}

	facts := reflect.TypeOf(GateMetricFacts{})
	if facts.NumField() != 4 {
		t.Fatalf("GateMetricFacts has %d fields, want 4", facts.NumField())
	}
	for i := 0; i < facts.NumField(); i++ {
		if facts.Field(i).Type != reflect.TypeOf(float64(0)) {
			t.Errorf("GateMetricFacts.%s = %v, want float64", facts.Field(i).Name, facts.Field(i).Type)
		}
	}

	in := GateMetricFacts{MedianRatio: 2.07, Threshold: 2.0, PValue: 0.0001, Alpha: 0.05}
	want := GateMetricFlag(in)
	for i := 0; i < 10; i++ {
		if got := GateMetricFlag(in); got != want {
			t.Fatalf("call %d = %v, want %v", i, got, want)
		}
	}
	if !want {
		t.Fatal("a ratio above threshold with a p-value below alpha must set the flag")
	}
}

// The gate-metric rule is a separate gate from the ship formula of T3.67. The
// two must not be conflated: each can be true while the other is false, and
// adding gate-metric facts to a snapshot does not move the CI verdict.
func TestGateMetricFlagIsDistinctFromShipFormula(t *testing.T) {
	passingMetrics := GateMetricFacts{MedianRatio: 2.07, Threshold: 2.0, PValue: 0.0001, Alpha: 0.05}
	failingMetrics := GateMetricFacts{MedianRatio: 1.2, Threshold: 2.0, PValue: 0.2, Alpha: 0.05}

	if !GateMetricFlag(passingMetrics) || ShipAllowed(ShipFacts{}) {
		t.Fatal("a passing gate metric must not imply a passing ship formula")
	}
	if GateMetricFlag(failingMetrics) || !ShipAllowed(ShipFacts{Build: true, Audit: true, TDD: true}) {
		t.Fatal("a passing ship formula must not imply a passing gate metric")
	}

	// The gate-metric facts are not part of Snapshot, so Evaluate cannot read
	// them and the §4.6 CI decision table stays untouched.
	snapshotType := reflect.TypeOf(Snapshot{})
	for i := 0; i < snapshotType.NumField(); i++ {
		if snapshotType.Field(i).Type == reflect.TypeOf(GateMetricFacts{}) {
			t.Fatalf("Snapshot.%s carries gate-metric facts; the CI rule must stay separate",
				snapshotType.Field(i).Name)
		}
	}
}

// completionPlan is a two-phase plan with approved, verifiable proof for each
// phase. Individual cases break one condition at a time.
func completionPlan() ([]Phase, []Proof) {
	phases := []Phase{{ID: "implement", Done: true}, {ID: "verify", Done: true}}
	proofs := []Proof{
		{PhaseID: "implement", Artifact: "abc123def", Approved: true},
		{PhaseID: "verify", Artifact: "ci/run/42", Approved: true},
	}
	return phases, proofs
}

// TestCompletionGate covers both closing conditions of ADR 0042 §4. Closing
// requires every phase done and an approved, verifiable proof for each one;
// any failure returns the task to work with a machine-readable reason.
func TestCompletionGate(t *testing.T) {
	tests := []struct {
		name   string
		phases []Phase
		proofs []Proof
		want   CompletionResult
	}{
		{
			name:   "no plan cannot close",
			phases: nil,
			proofs: nil,
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonNoPlan},
		},
		{
			name:   "open phase returns to work",
			phases: []Phase{{ID: "implement", Done: true}, {ID: "verify", Done: false}},
			proofs: []Proof{{PhaseID: "implement", Artifact: "abc123def", Approved: true}},
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonOpenPhase, PhaseID: "verify"},
		},
		{
			name:   "done phase without proof returns to work",
			phases: []Phase{{ID: "implement", Done: true}},
			proofs: nil,
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonMissingProof, PhaseID: "implement"},
		},
		{
			name:   "empty proof trace returns to work",
			phases: []Phase{{ID: "implement", Done: true}},
			proofs: []Proof{{PhaseID: "implement", Artifact: "   ", Approved: true}},
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonProofNoTrace, PhaseID: "implement"},
		},
		{
			name:   "unapproved proof returns to work",
			phases: []Phase{{ID: "implement", Done: true}},
			proofs: []Proof{{PhaseID: "implement", Artifact: "abc123def", Approved: false}},
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonProofUnapproved, PhaseID: "implement"},
		},
		{
			name:   "proof for another phase does not count",
			phases: []Phase{{ID: "verify", Done: true}},
			proofs: []Proof{{PhaseID: "implement", Artifact: "abc123def", Approved: true}},
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonMissingProof, PhaseID: "verify"},
		},
		{
			name:   "all phases done with approved proof closes",
			phases: []Phase{{ID: "implement", Done: true}, {ID: "verify", Done: true}},
			proofs: []Proof{
				{PhaseID: "implement", Artifact: "abc123def", Approved: true},
				{PhaseID: "verify", Artifact: "ci/run/42", Approved: true},
			},
			want: CompletionResult{Outcome: CompletionAllow},
		},
		{
			name:   "one phase missing proof blocks the whole plan",
			phases: []Phase{{ID: "implement", Done: true}, {ID: "verify", Done: true}},
			proofs: []Proof{{PhaseID: "implement", Artifact: "abc123def", Approved: true}},
			want:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonMissingProof, PhaseID: "verify"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompletionGate(tt.phases, tt.proofs); got != tt.want {
				t.Fatalf("CompletionGate = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Both conditions are independent: done phases alone and proof alone each
// fail. Only the conjunction closes the task.
func TestCompletionGateRequiresConditionsTogether(t *testing.T) {
	phases, proofs := completionPlan()

	// All phases done, but no proof left.
	if got := CompletionGate(phases, nil); got.Outcome != CompletionRework ||
		got.Reason != CompletionReasonMissingProof {
		t.Fatalf("done phases without proof = %+v, want rework/missing_proof", got)
	}

	// Proof for every phase, but one phase still open.
	open := []Phase{{ID: "implement", Done: true}, {ID: "verify", Done: false}}
	if got := CompletionGate(open, proofs); got.Outcome != CompletionRework ||
		got.Reason != CompletionReasonOpenPhase {
		t.Fatalf("proof with an open phase = %+v, want rework/open_phase", got)
	}

	if got := CompletionGate(phases, proofs); got.Outcome != CompletionAllow {
		t.Fatalf("both conditions met = %+v, want allow", got)
	}
}

// The gate is a closing condition, not the review gate: its result carries no
// Verdict or ReviewVerdict and its output is not one of those types. A REWORK
// must not be mistaken for a downgraded review decision.
func TestCompletionGateDoesNotChangeVerdict(t *testing.T) {
	resultType := reflect.TypeOf(CompletionResult{})
	verdictType := reflect.TypeOf(VerdictWatching)
	reviewType := reflect.TypeOf(ReviewShip)
	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i).Type
		if field == verdictType || field == reviewType {
			t.Errorf("CompletionResult.%s = %v, must not carry a verdict", resultType.Field(i).Name, field)
		}
	}

	fn := reflect.TypeOf(CompletionGate)
	if fn.NumOut() != 1 || fn.Out(0) != resultType {
		t.Fatalf("CompletionGate output = %v, want exactly one CompletionResult", fn)
	}

	// Even with a SHIP review verdict on the table, a missing proof is REWORK.
	phases, proofs := completionPlan()
	proofs[1].Approved = false
	got := CompletionGate(phases, proofs)
	if got.Outcome != CompletionRework || got.Reason != CompletionReasonProofUnapproved {
		t.Fatalf("gate with unapproved proof = %+v, want rework/proof_unapproved", got)
	}
}

// The gate is pure: repeated calls give the same result and leave the plan and
// its proofs untouched, so a caller can retry it without side effects.
func TestCompletionGateDeterministicAndPure(t *testing.T) {
	phases, proofs := completionPlan()
	phases[1].Done = false
	want := CompletionGate(phases, proofs)
	phasesCopy := append([]Phase(nil), phases...)
	proofsCopy := append([]Proof(nil), proofs...)

	for i := 0; i < 10; i++ {
		if got := CompletionGate(phases, proofs); got != want {
			t.Fatalf("call %d = %+v, want %+v", i, got, want)
		}
	}
	if !reflect.DeepEqual(phases, phasesCopy) || !reflect.DeepEqual(proofs, proofsCopy) {
		t.Fatalf("gate mutated its inputs: phases=%+v proofs=%+v", phases, proofs)
	}
}

// The canonical strings are the journal form. Changing one breaks recorded
// completion results, the same way the review verdict strings are pinned.
func TestCompletionCanonicalStrings(t *testing.T) {
	if string(CompletionAllow) != "ALLOW" || string(CompletionRework) != "REWORK" {
		t.Fatalf("outcome strings = %q, %q", CompletionAllow, CompletionRework)
	}
	cases := map[CompletionReason]string{
		CompletionReasonNoPlan:          "no_plan",
		CompletionReasonOpenPhase:       "open_phase",
		CompletionReasonMissingProof:    "missing_proof",
		CompletionReasonProofNoTrace:    "proof_no_trace",
		CompletionReasonProofUnapproved: "proof_unapproved",
	}
	for reason, want := range cases {
		if string(reason) != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
	}
}

// The JSON shape is part of the recorded result contract: outcome is always
// present, reason and phase are omitted on a clean allow.
func TestCompletionResultJSON(t *testing.T) {
	cases := []struct {
		name string
		in   CompletionResult
		want string
	}{
		{
			name: "allow omits reason and phase",
			in:   CompletionResult{Outcome: CompletionAllow},
			want: `{"outcome":"ALLOW"}`,
		},
		{
			name: "rework names the reason and phase",
			in:   CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonOpenPhase, PhaseID: "verify"},
			want: `{"outcome":"REWORK","reason":"open_phase","phase":"verify"}`,
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

// TestEvaluateExternalDecisionTable covers ADR 0044 §3: the verdict is made
// only from declared checks and their observed exit codes, and the gate fails
// closed on an empty or incomplete panel.
func TestEvaluateExternalDecisionTable(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
		observed []ExternalCheck
		want     ExternalVerdict
		checks   []CheckOutcome
	}{
		{
			name:     "empty declaration fails closed",
			declared: nil,
			observed: nil,
			want:     ExternalFail,
			checks:   []CheckOutcome{},
		},
		{
			name:     "single declared check passes on exit 0",
			declared: []string{"build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}},
			want:     ExternalPass,
			checks:   []CheckOutcome{{Name: "build", ExitCode: 0, Observed: true, Passed: true}},
		},
		{
			name:     "non-zero exit fails the gate",
			declared: []string{"build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 1}},
			want:     ExternalFail,
			checks:   []CheckOutcome{{Name: "build", ExitCode: 1, Observed: true, Passed: false}},
		},
		{
			name:     "negative exit fails the gate",
			declared: []string{"build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: -1}},
			want:     ExternalFail,
			checks:   []CheckOutcome{{Name: "build", ExitCode: -1, Observed: true, Passed: false}},
		},
		{
			name:     "all declared checks pass",
			declared: []string{"build", "audit"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}, {Name: "audit", ExitCode: 0}},
			want:     ExternalPass,
			checks: []CheckOutcome{
				{Name: "build", ExitCode: 0, Observed: true, Passed: true},
				{Name: "audit", ExitCode: 0, Observed: true, Passed: true},
			},
		},
		{
			name:     "one failing exit fails the whole gate",
			declared: []string{"build", "audit"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}, {Name: "audit", ExitCode: 2}},
			want:     ExternalFail,
			checks: []CheckOutcome{
				{Name: "build", ExitCode: 0, Observed: true, Passed: true},
				{Name: "audit", ExitCode: 2, Observed: true, Passed: false},
			},
		},
		{
			name:     "missing observation fails and stays visible",
			declared: []string{"build", "audit"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}},
			want:     ExternalFail,
			checks: []CheckOutcome{
				{Name: "build", ExitCode: 0, Observed: true, Passed: true},
				{Name: "audit", ExitCode: 0, Observed: false, Passed: false},
			},
		},
		{
			name:     "observation for an undeclared check is ignored",
			declared: []string{"build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}, {Name: "rogue", ExitCode: 0}},
			want:     ExternalPass,
			checks:   []CheckOutcome{{Name: "build", ExitCode: 0, Observed: true, Passed: true}},
		},
		{
			name:     "declaration order is kept regardless of observation order",
			declared: []string{"audit", "build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}, {Name: "audit", ExitCode: 0}},
			want:     ExternalPass,
			checks: []CheckOutcome{
				{Name: "audit", ExitCode: 0, Observed: true, Passed: true},
				{Name: "build", ExitCode: 0, Observed: true, Passed: true},
			},
		},
		{
			name:     "duplicate declarations are recorded once",
			declared: []string{"build", "build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}},
			want:     ExternalPass,
			checks:   []CheckOutcome{{Name: "build", ExitCode: 0, Observed: true, Passed: true}},
		},
		{
			name:     "duplicate observations use the first exit code",
			declared: []string{"build"},
			observed: []ExternalCheck{{Name: "build", ExitCode: 1}, {Name: "build", ExitCode: 0}},
			want:     ExternalFail,
			checks:   []CheckOutcome{{Name: "build", ExitCode: 1, Observed: true, Passed: false}},
		},
		{
			name:     "empty declared name fails the gate",
			declared: []string{""},
			observed: []ExternalCheck{{Name: "", ExitCode: 0}},
			want:     ExternalFail,
			checks:   []CheckOutcome{},
		},
		{
			name:     "empty name among valid declarations fails the gate",
			declared: []string{"build", ""},
			observed: []ExternalCheck{{Name: "build", ExitCode: 0}},
			want:     ExternalFail,
			checks:   []CheckOutcome{{Name: "build", ExitCode: 0, Observed: true, Passed: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateExternal(tt.declared, tt.observed)
			if got.Verdict != tt.want {
				t.Errorf("verdict = %q, want %q", got.Verdict, tt.want)
			}
			if !reflect.DeepEqual(got.Checks, tt.checks) {
				t.Errorf("checks = %#v, want %#v", got.Checks, tt.checks)
			}
		})
	}
}

// The evidence is the record an adapter writes to disk. Its JSON shape pins
// the contract: the verdict plus the exit code of every declared check, with
// a missing run recorded as observed=false so it cannot look like a pass.
func TestExternalEvidenceJSON(t *testing.T) {
	cases := []struct {
		name string
		in   ExternalEvidence
		want string
	}{
		{
			name: "pass carries the exit codes of all declared checks",
			in: EvaluateExternal(
				[]string{"build", "audit"},
				[]ExternalCheck{{Name: "audit", ExitCode: 0}, {Name: "build", ExitCode: 0}},
			),
			want: `{"verdict":"PASS","checks":[{"name":"build","exit_code":0,"observed":true,"passed":true},{"name":"audit","exit_code":0,"observed":true,"passed":true}]}`,
		},
		{
			name: "fail records the failing exit code and the missing check",
			in: EvaluateExternal(
				[]string{"build", "audit"},
				[]ExternalCheck{{Name: "build", ExitCode: 7}},
			),
			want: `{"verdict":"FAIL","checks":[{"name":"build","exit_code":7,"observed":true,"passed":false},{"name":"audit","exit_code":0,"observed":false,"passed":false}]}`,
		},
		{
			name: "empty panel serializes checks as an empty array",
			in:   EvaluateExternal(nil, nil),
			want: `{"verdict":"FAIL","checks":[]}`,
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

// The gate is pure: repeated calls on the same external facts give the same
// evidence and leave the declared names and observed checks untouched.
func TestEvaluateExternalDeterministicAndPure(t *testing.T) {
	declared := []string{"build", "audit"}
	observed := []ExternalCheck{{Name: "build", ExitCode: 0}, {Name: "audit", ExitCode: 3}}
	want := EvaluateExternal(declared, observed)
	if want.Verdict != ExternalFail {
		t.Fatalf("verdict = %q, want fail", want.Verdict)
	}

	declaredCopy := append([]string(nil), declared...)
	observedCopy := append([]ExternalCheck(nil), observed...)
	for i := 0; i < 10; i++ {
		got := EvaluateExternal(declared, observed)
		if got.Verdict != want.Verdict || !reflect.DeepEqual(got.Checks, want.Checks) {
			t.Fatalf("call %d = %+v, want %+v", i, got, want)
		}
	}
	if !reflect.DeepEqual(declared, declaredCopy) || !reflect.DeepEqual(observed, observedCopy) {
		t.Fatalf("inputs mutated: declared=%v observed=%v", declared, observed)
	}
}

// The external verdict is a distinct gate from T3.67 and T3.88. Its inputs
// are only declared names and observed exit codes, and its evidence carries
// no ship facts, proof, internal verdict or review verdict.
func TestEvaluateExternalDependsOnlyOnExternalFacts(t *testing.T) {
	fn := reflect.TypeOf(EvaluateExternal)
	if fn.NumIn() != 2 ||
		fn.In(0) != reflect.TypeOf([]string{}) ||
		fn.In(1) != reflect.TypeOf([]ExternalCheck{}) {
		t.Fatalf("EvaluateExternal input = %v, want ([]string, []ExternalCheck) only", fn)
	}
	if fn.NumOut() != 1 || fn.Out(0) != reflect.TypeOf(ExternalEvidence{}) {
		t.Fatalf("EvaluateExternal output = %v, want exactly one ExternalEvidence", fn)
	}

	if reflect.TypeOf(ExternalPass) == reflect.TypeOf(VerdictWatching) ||
		reflect.TypeOf(ExternalPass) == reflect.TypeOf(ReviewShip) {
		t.Fatal("external verdict type must differ from Verdict and ReviewVerdict")
	}

	internalTypes := map[string]reflect.Type{
		"Verdict":          reflect.TypeOf(VerdictWatching),
		"ReviewVerdict":    reflect.TypeOf(ReviewShip),
		"ShipFacts":        reflect.TypeOf(ShipFacts{}),
		"Proof":            reflect.TypeOf(Proof{}),
		"CompletionResult": reflect.TypeOf(CompletionResult{}),
	}
	evidenceType := reflect.TypeOf(ExternalEvidence{})
	if evidenceType.NumField() != 2 {
		t.Fatalf("ExternalEvidence has %d fields, want 2", evidenceType.NumField())
	}
	for i := 0; i < evidenceType.NumField(); i++ {
		field := evidenceType.Field(i)
		for name, typ := range internalTypes {
			if field.Type == typ {
				t.Errorf("ExternalEvidence.%s = %v, must not carry internal %s", field.Name, field.Type, name)
			}
		}
	}
}

// Nothing the agent reported can move the verdict: two evaluations over the
// same external facts stay equal even when the surrounding internal verdict,
// ship facts, proof and completion result differ.
func TestEvaluateExternalIgnoresAgentReportedState(t *testing.T) {
	declared := []string{"build"}
	observed := []ExternalCheck{{Name: "build", ExitCode: 0}}
	baseline := EvaluateExternal(declared, observed)

	_ = VerdictReady
	_ = ReviewShip
	_ = ShipFacts{Build: true, Audit: true, TDD: true}
	_ = Proof{PhaseID: "implement", Artifact: "x", Approved: true}
	_ = CompletionResult{Outcome: CompletionAllow}

	got := EvaluateExternal(declared, observed)
	if got.Verdict != baseline.Verdict || !reflect.DeepEqual(got.Checks, baseline.Checks) {
		t.Fatalf("verdict changed without new external facts: %+v, want %+v", got, baseline)
	}
}

// The canonical journal strings are pinned. Changing one breaks a recorded
// external verdict on disk.
func TestExternalCanonicalStrings(t *testing.T) {
	if string(ExternalPass) != "PASS" || string(ExternalFail) != "FAIL" {
		t.Fatalf("verdict strings = %q, %q", ExternalPass, ExternalFail)
	}
}

// The three strict codes of ADR 0047 feature 3 are pinned to 0, 3 and 4. A
// shifted number would silently reinterpret every recorded reviewer verdict.
func TestReviewerExitCodeValues(t *testing.T) {
	if ReviewerExitApprove != 0 || ReviewerExitChangesRequested != 3 || ReviewerExitReject != 4 {
		t.Fatalf("codes = %d, %d, %d, want 0, 3, 4",
			ReviewerExitApprove, ReviewerExitChangesRequested, ReviewerExitReject)
	}
}

// TestParseReviewerExitCodeDecisionTable covers each of the four parse rules
// in priority order: block wins, contradiction is unclear, each clear signal,
// and no signal. Case names name the rule so a failure points at the branch.
func TestParseReviewerExitCodeDecisionTable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ReviewerExitCode
	}{
		// rule 1: a block always wins, even next to an approve.
		{"approve", "APPROVE", ReviewerExitApprove},
		{"approved with whitespace", "  approve\n", ReviewerExitApprove},
		{"approved with punctuation", "Approved.", ReviewerExitApprove},
		{"lgtm", "LGTM", ReviewerExitApprove},
		{"ship it", "ship it", ReviewerExitApprove},
		{"looks good to me", "looks good to me", ReviewerExitApprove},
		// rule 3: clear changes requested.
		{"changes requested", "changes requested", ReviewerExitChangesRequested},
		{"request changes", "Please request changes here", ReviewerExitChangesRequested},
		{"fix first", "fix first", ReviewerExitChangesRequested},
		{"reject", "reject", ReviewerExitChangesRequested},
		// rule 2: approve plus changes is contradictory, so unclear.
		{"approve and changes is unclear", "I approve but changes requested", ReviewerExitReject},
		// rule 4: no recognised signal fails closed.
		{"empty answer", "", ReviewerExitReject},
		{"whitespace only", "   \n\t ", ReviewerExitReject},
		{"hedging", "maybe, could be ok, not sure", ReviewerExitReject},
		{"unrelated text", "the weather is nice today", ReviewerExitReject},
		// rule 1 again: every block marker outranks an approve.
		{"approve with blocker", "I approve, but this is a blocker", ReviewerExitReject},
		{"approve with blocked", "APPROVED, one issue blocked", ReviewerExitReject},
		{"approve with veto", "approve / veto", ReviewerExitReject},
		{"block alone", "block", ReviewerExitReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseReviewerExitCode(tt.in); got != tt.want {
				t.Fatalf("ParseReviewerExitCode(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// Negated approve markers must not be read as an approve. "not approved" is
// a rejection, not a pass, and a negated block is no block at all.
func TestParseReviewerExitCodeNegation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ReviewerExitCode
	}{
		{"not approved is reject", "not approved", ReviewerExitReject},
		{"cannot approve is reject", "cannot approve this", ReviewerExitReject},
		{"cant approve is reject", "can't approve", ReviewerExitReject},
		{"do not approve is reject", "do not approve", ReviewerExitReject},
		{"negated block is not a block", "there is no block here, approved", ReviewerExitApprove},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseReviewerExitCode(tt.in); got != tt.want {
				t.Fatalf("ParseReviewerExitCode(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// A block wins over every other signal, including a clear changes request,
// and it cannot be softened by hedging or an approve anywhere in the answer.
func TestParseReviewerExitCodeBlockingAlwaysWins(t *testing.T) {
	bases := []string{
		"approve",
		"changes requested",
		"looks good to me",
		"maybe",
		"",
	}
	for _, base := range bases {
		for _, marker := range []string{"block", "blocked", "blocker", "blocking", "veto"} {
			answer := base + " but " + marker
			if got := ParseReviewerExitCode(answer); got != ReviewerExitReject {
				t.Errorf("ParseReviewerExitCode(%q) = %d, want block reject %d", answer, got, ReviewerExitReject)
			}
		}
	}
}

// An unclear answer is a rejection, never a pass. No input may turn a missing
// or unrecognised answer into code 0.
func TestParseReviewerExitCodeUnclearIsReject(t *testing.T) {
	for _, answer := range []string{"", " ", "?", "hmm", "I have no opinion", "seems fine to me"} {
		if got := ParseReviewerExitCode(answer); got != ReviewerExitReject {
			t.Errorf("unclear answer %q = %d, want reject %d", answer, got, ReviewerExitReject)
		}
	}
}

// This parser is not the indeterminate of T3.64 and not any other verdict
// type. Its signature takes only the reviewer text and returns only a
// ReviewerExitCode, so it cannot read a Watch, a check or a signed verdict.
func TestParseReviewerExitCodeIndependentOfOtherVerdicts(t *testing.T) {
	fn := reflect.TypeOf(ParseReviewerExitCode)
	if fn.NumIn() != 1 || fn.In(0) != reflect.TypeOf("") {
		t.Fatalf("input = %v, want exactly one string", fn)
	}
	if fn.NumOut() != 1 || fn.Out(0) != reflect.TypeOf(ReviewerExitApprove) {
		t.Fatalf("output = %v, want exactly one ReviewerExitCode", fn)
	}

	otherVerdictTypes := []reflect.Type{
		reflect.TypeOf(VerdictWatching),
		reflect.TypeOf(ReviewShip),
		reflect.TypeOf(ExternalPass),
	}
	if reflect.TypeOf(ReviewerExitApprove) == reflect.TypeOf(0) {
		t.Fatal("ReviewerExitCode must be a distinct named type, not a bare int")
	}
	for _, typ := range otherVerdictTypes {
		if fn.Out(0) == typ {
			t.Errorf("parser must not return %v", typ)
		}
	}
}

// The parser is pure: the same answer always gives the same code, and the
// input string is a value so it cannot be mutated through the call.
func TestParseReviewerExitCodeDeterministic(t *testing.T) {
	answer := "changes requested, but approved, blocked"
	want := ParseReviewerExitCode(answer)
	if want != ReviewerExitReject {
		t.Fatalf("block answer = %d, want reject %d", want, ReviewerExitReject)
	}
	for i := 0; i < 10; i++ {
		if got := ParseReviewerExitCode(answer); got != want {
			t.Fatalf("call %d = %d, want %d", i, got, want)
		}
	}
	if answer != "changes requested, but approved, blocked" {
		t.Fatalf("input mutated to %q", answer)
	}
}

// TestEvaluateGreenBlockedDecisionTable covers the two conditions of ADR 0059
// §3. Only a green PR with an open blocker becomes green-blocked and frees its
// slot. Every other combination keeps the slot. Case names name the condition.
func TestEvaluateGreenBlockedDecisionTable(t *testing.T) {
	red := readySnapshot()
	red.Rollup = "FAILURE"
	red.Ship = ShipFacts{}
	noCI := readySnapshot()
	noCI.Ship = ShipFacts{}
	draft := readySnapshot()
	draft.IsDraft = true
	cachedWrong := readySnapshot()
	cachedWrong.SuitesValid = false

	tests := []struct {
		name        string
		s           Snapshot
		blockerOpen bool
		want        GreenBlockedVerdict
		wantFree    bool
	}{
		{"green and blocked frees the slot", readySnapshot(), true, GreenBlocked, true},
		{"green without blocker keeps the slot", readySnapshot(), false, GreenBlockedNone, false},
		{"red with blocker is not green blocked", red, true, GreenBlockedNone, false},
		{"red without blocker", red, false, GreenBlockedNone, false},
		{"no CI facts with blocker keeps the slot", noCI, true, GreenBlockedNone, false},
		{"draft with blocker is not green", draft, true, GreenBlockedNone, false},
		{"invalid suite with blocker is not green", cachedWrong, true, GreenBlockedNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateGreenBlocked(tt.s, Policy{}, tt.blockerOpen)
			if got != tt.want {
				t.Fatalf("EvaluateGreenBlocked = %q, want %q", got, tt.want)
			}
			if free := got.ReleasesSlot(); free != tt.wantFree {
				t.Fatalf("ReleasesSlot() = %v, want %v for verdict %q", free, tt.wantFree, got)
			}
		})
	}
}

// A green PR that becomes blocked frees its slot, and the slot is reclaimed as
// soon as the blocker closes. The two conditions can toggle independently.
func TestGreenBlockedSlotFollowsBlocker(t *testing.T) {
	s := readySnapshot()
	if got := EvaluateGreenBlocked(s, Policy{}, false); got.ReleasesSlot() {
		t.Fatalf("ready PR without blocker already frees the slot: %q", got)
	}
	blocked := EvaluateGreenBlocked(s, Policy{}, true)
	if blocked != GreenBlocked || !blocked.ReleasesSlot() {
		t.Fatalf("ready PR with blocker = %q, want %q freeing the slot", blocked, GreenBlocked)
	}
	unblocked := EvaluateGreenBlocked(s, Policy{}, false)
	if unblocked != GreenBlockedNone || unblocked.ReleasesSlot() {
		t.Fatalf("closed blocker = %q, want %q keeping the slot", unblocked, GreenBlockedNone)
	}
}

// The green-blocked verdict is a distinct scheduling status. It must not
// collide with the CI verdict, the external verdict, a parked Watch, a BLOCKED
// review verdict or a reviewer exit code, and its journal string must not be
// the word BLOCKED that those concepts already use.
func TestGreenBlockedIsNotParkOrBlocked(t *testing.T) {
	if reflect.TypeOf(GreenBlocked) == reflect.TypeOf(VerdictWatching) ||
		reflect.TypeOf(GreenBlocked) == reflect.TypeOf(ExternalPass) ||
		reflect.TypeOf(GreenBlocked) == reflect.TypeOf(ReviewBlocked) ||
		reflect.TypeOf(GreenBlocked) == reflect.TypeOf(ReviewerExitReject) {
		t.Fatal("green-blocked verdict type must differ from other verdict types")
	}
	if string(GreenBlocked) == string(VerdictParked) ||
		string(GreenBlocked) == string(ReviewBlocked) ||
		string(GreenBlocked) == string(VerdictReady) ||
		string(GreenBlocked) == string(ExternalPass) {
		t.Fatalf("green-blocked string %q collides with an existing verdict", GreenBlocked)
	}
}

// ReleasesSlot is true only for the green-blocked verdict. The zero value and
// every other named status keep the slot.
func TestGreenBlockedReleasesSlotOnlyForHeldVerdict(t *testing.T) {
	if GreenBlockedNone.ReleasesSlot() {
		t.Fatal("zero verdict must not free a slot")
	}
	if !GreenBlocked.ReleasesSlot() {
		t.Fatal("green-blocked verdict must free a slot")
	}
	if GreenBlockedVerdict("something_else").ReleasesSlot() {
		t.Fatal("unknown verdict must not free a slot")
	}
}

// Green-blocked is a pure scheduling decision: it does not change the CI
// verdict, does not set the ready verdict and does not let an agent-provided
// flag manufacture a green PR. Green comes from Evaluate over CI facts only.
func TestEvaluateGreenBlockedDoesNotChangeCIVerdict(t *testing.T) {
	s := readySnapshot()
	blocked := EvaluateGreenBlocked(s, Policy{}, true)
	if string(blocked) == string(VerdictReady) {
		t.Fatal("green-blocked must not become the ready verdict")
	}
	if got := Evaluate(s, Policy{}); got != VerdictReady {
		t.Fatalf("Evaluate mutated by green-blocked check: %q, want ready", got)
	}

	before := s
	before.Checks = make(map[string]Check, len(s.Checks))
	for name, check := range s.Checks {
		before.Checks[name] = check
	}
	before.Failures = append([]string(nil), s.Failures...)
	_ = EvaluateGreenBlocked(s, Policy{}, true)
	if !reflect.DeepEqual(s, before) {
		t.Fatalf("EvaluateGreenBlocked mutated the snapshot: %+v, want %+v", s, before)
	}
}

// The decision is deterministic: the same facts always yield the same verdict,
// independent of how many times the engine is asked.
func TestEvaluateGreenBlockedDeterministic(t *testing.T) {
	s := readySnapshot()
	want := EvaluateGreenBlocked(s, Policy{}, true)
	for i := 0; i < 10; i++ {
		if got := EvaluateGreenBlocked(s, Policy{}, true); got != want {
			t.Fatalf("call %d = %q, want %q", i, got, want)
		}
	}
}
