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
