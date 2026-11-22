package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/domain"
)

type orchFakeStore struct {
	watches     []orchestrator.Watch
	merged      map[string]bool
	closed      []string
	fixAttempts []string
}

func newOrchFakeStore(watches ...orchestrator.Watch) *orchFakeStore {
	return &orchFakeStore{watches: watches, merged: make(map[string]bool)}
}

func (s *orchFakeStore) key(id string, revision uint64) string {
	return fmt.Sprintf("%s@%d", id, revision)
}

func (s *orchFakeStore) ActiveWatches(context.Context) ([]orchestrator.Watch, error) {
	return s.watches, nil
}

func (s *orchFakeStore) WasMerged(_ context.Context, watchID string, revision uint64) (bool, error) {
	return s.merged[s.key(watchID, revision)], nil
}

func (s *orchFakeStore) RecordMerge(_ context.Context, watchID string, revision uint64) error {
	s.merged[s.key(watchID, revision)] = true
	return nil
}

func (s *orchFakeStore) CloseWatch(_ context.Context, watchID string) error {
	s.closed = append(s.closed, watchID)
	return nil
}

func (s *orchFakeStore) RecordFixAttempt(_ context.Context, watchID string) error {
	s.fixAttempts = append(s.fixAttempts, watchID)
	return nil
}

type orchFakeMerger struct{ calls []int }

func (m *orchFakeMerger) Merge(_ context.Context, _ string, pr int) error {
	m.calls = append(m.calls, pr)
	return nil
}

type orchFakeResolver map[string]bool

func (r orchFakeResolver) Satisfied(_ context.Context, ref string) (bool, error) {
	return r[ref], nil
}

func withOrchResolverDeps(t *testing.T, st orchestrator.Store, merger orchestrator.Merger, resolver orchestrator.DependencyResolver) {
	t.Helper()
	previous := makeOrchestratorDeps
	t.Cleanup(func() { SetOrchestratorDeps(previous) })
	SetOrchestratorDeps(func(*CLI) (orchestratorDeps, func() error, error) {
		return orchestratorDeps{Store: st, Merger: merger, Resolver: resolver}, func() error { return nil }, nil
	})
}

func withOrchDeps(t *testing.T, st orchestrator.Store, merger orchestrator.Merger, waker orchestrator.Waker) {
	t.Helper()
	previous := makeOrchestratorDeps
	t.Cleanup(func() { SetOrchestratorDeps(previous) })
	SetOrchestratorDeps(func(*CLI) (orchestratorDeps, func() error, error) {
		return orchestratorDeps{Store: st, Merger: merger, Waker: waker}, func() error { return nil }, nil
	})
}

// TestProjectWatchCopiesBlocked proves the composition-root projection carries
// the block details, including FIXAttempts, into the orchestrator's decision
// shape. FailedChecks is copied so later mutation of the domain Watch cannot
// change a decision already made.
func TestProjectWatchCopiesBlocked(t *testing.T) {
	checks := []string{"ci-build", "gates"}
	w := domain.Watch{
		ID: "w1", Repo: "refusedguy/beacon", PR: 42, HeadSHA: "abc123",
		Verdict: domain.VerdictBlocked, Revision: 7,
		Blocked: &domain.BlockedInfo{
			Reason:       domain.BlockReasonNotMine,
			FailedChecks: checks,
			Since:        time.Unix(1, 0),
			BlockedBy:    "executor",
			FixAttempts:  2,
		},
	}

	got := projectWatch(w)

	if got.Blocked == nil {
		t.Fatal("projection dropped Watch.Blocked")
	}
	if got.Blocked.Reason != string(domain.BlockReasonNotMine) || got.Blocked.BlockedBy != "executor" {
		t.Fatalf("block context = %+v", got.Blocked)
	}
	if got.Blocked.FixAttempts != 2 {
		t.Fatalf("FixAttempts = %d, want 2", got.Blocked.FixAttempts)
	}
	if !reflect.DeepEqual(got.Blocked.FailedChecks, []string{"ci-build", "gates"}) {
		t.Fatalf("FailedChecks = %v", got.Blocked.FailedChecks)
	}
	if got.ID != w.ID || got.PR != w.PR || got.HeadSHA != w.HeadSHA ||
		got.Revision != w.Revision || got.Verdict != string(w.Verdict) {
		t.Fatalf("identity fields = %+v", got)
	}

	w.Blocked.FailedChecks[0] = "mutated"
	w.Blocked.FixAttempts = 99
	if got.Blocked.FailedChecks[0] != "ci-build" || got.Blocked.FixAttempts != 2 {
		t.Fatalf("projection shares block state with the domain Watch: %+v", got.Blocked)
	}

	if nilBlocked := projectWatch(domain.Watch{ID: "w2"}).Blocked; nilBlocked != nil {
		t.Fatalf("unblocked Watch projected with block info: %+v", nilBlocked)
	}
}

func TestOrchestrateDispatch(t *testing.T) {
	previous := orchestratorRunner
	t.Cleanup(func() { orchestratorRunner = previous })
	var gotArgs []string
	RegisterOrchestrator(func(_ *CLI, args []string) int {
		gotArgs = append([]string(nil), args...)
		return 7
	})
	c, _, _, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--dry-run"}); code != 7 {
		t.Fatalf("exit = %d, want 7", code)
	}
	if !reflect.DeepEqual(gotArgs, []string{"--dry-run"}) {
		t.Fatalf("args = %q, want [--dry-run]", gotArgs)
	}
}

func TestOrchestrateDryRunPrintsActionsWithoutMerge(t *testing.T) {
	st := newOrchFakeStore(orchestrator.Watch{
		ID: "w1", Repo: "refusedguy/beacon", PR: 42, Verdict: "ready", Revision: 3,
	})
	merger := &orchFakeMerger{}
	withOrchDeps(t, st, merger, nil)
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--dry-run"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if len(merger.calls) != 0 {
		t.Fatalf("dry run must not merge: %v", merger.calls)
	}
	if len(st.closed) != 0 {
		t.Fatalf("dry run must not close watches: %q", st.closed)
	}
	if !strings.Contains(out.String(), "gh pr merge 42 --squash --delete-branch") {
		t.Fatalf("stdout = %q, want the planned gh command", out.String())
	}
}

func TestOrchestrateMergeAndRebaseReport(t *testing.T) {
	st := newOrchFakeStore(
		orchestrator.Watch{ID: "w1", Repo: "refusedguy/beacon", PR: 42, Verdict: "ready", Revision: 3},
		orchestrator.Watch{ID: "w2", Repo: "refusedguy/beacon", PR: 7, Verdict: "retry", Revision: 1},
	)
	merger := &orchFakeMerger{}
	withOrchDeps(t, st, merger, nil)
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--format", "json"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !reflect.DeepEqual(merger.calls, []int{42}) {
		t.Fatalf("merger calls = %v, want [42]", merger.calls)
	}
	if !reflect.DeepEqual(st.closed, []string{"w1"}) {
		t.Fatalf("closed = %q, want [w1]", st.closed)
	}
	var result struct {
		Merged      []int `json:"merged"`
		NeedsRebase []int `json:"needsRebase"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Merged, []int{42}) || !reflect.DeepEqual(result.NeedsRebase, []int{7}) {
		t.Fatalf("result = %+v, want merged [42] needsRebase [7]", result)
	}
}

// TestOrchestrateUsesInjectedResolver proves the CLI passes the dependency
// resolver into orchestrator.Run: a ready Watch with a satisfied waitFor merges,
// the same Watch with an unsatisfied ref stays open and reports the reason.
func TestOrchestrateUsesInjectedResolver(t *testing.T) {
	policy := &orchestrator.AutoMergePolicy{WaitFor: []string{"T3.100"}}
	watch := orchestrator.Watch{
		ID: "w1", Repo: "refusedguy/beacon", PR: 42, Verdict: "ready", Revision: 3, Policy: policy,
	}
	for _, tc := range []struct {
		name      string
		satisfied bool
		wantMerge bool
		wantWait  bool
	}{
		{name: "satisfied", satisfied: true, wantMerge: true},
		{name: "unsatisfied", satisfied: false, wantWait: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newOrchFakeStore(watch)
			merger := &orchFakeMerger{}
			resolver := orchFakeResolver{"T3.100": tc.satisfied}
			withOrchResolverDeps(t, st, merger, resolver)
			c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

			if code := c.Run([]string{"orchestrate", "--format", "json"}); code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
			}
			if got := len(merger.calls) == 1; got != tc.wantMerge {
				t.Fatalf("merged = %v, calls = %v", got, merger.calls)
			}
			if tc.wantWait && !strings.Contains(out.String(), "wait_for:T3.100") {
				t.Fatalf("stdout = %q, want the wait reason", out.String())
			}
		})
	}
}

func TestOrchestrateBadFormat(t *testing.T) {
	st := newOrchFakeStore()
	withOrchDeps(t, st, &orchFakeMerger{}, nil)
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))

	if code := c.Run([]string{"orchestrate", "--format", "yaml"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "неизвестный формат") {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

// recordingDomainMerger records the explicit repo argument and returns a
// scripted outcome, so the adapter can be checked without a network or gh.
type recordingDomainMerger struct {
	repo   string
	pr     int
	merged bool
	held   bool
	reason string
	err    error
}

func (m *recordingDomainMerger) Merge(_ context.Context, repo string, pr int) (bool, bool, string, error) {
	m.repo, m.pr = repo, pr
	return m.merged, m.held, m.reason, m.err
}

func TestDomainMergeAdapterPassesRepoAndReportsHold(t *testing.T) {
	fake := &recordingDomainMerger{held: true, reason: "conflict"}
	err := (domainMergeAdapter{merger: fake}).Merge(context.Background(), "owner/name", 42)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("error = %v, want a hold with the provider reason", err)
	}
	if fake.repo != "owner/name" || fake.pr != 42 {
		t.Fatalf("merge called with %s#%d, want owner/name#42", fake.repo, fake.pr)
	}
}

// TestVerifyIssueEvidence drives the pure verifier with in-memory mock code.
// It covers the three HD-009 outcomes: a matching file:line is current, a
// stale reference is marked outdated before work, and a claim with no
// reference is reported rather than accepted blindly.
func TestVerifyIssueEvidence(t *testing.T) {
	code := currentCode{
		"cmd/beacon/orchestrate.go": {
			"package main",
			"func runOrchestrate(c *CLI, args []string) int {",
			"	return 0",
		},
	}
	tests := []struct {
		name       string
		ev         issueEvidence
		wantStatus evidenceStatus
		wantNote   string
		wantClear  bool
	}{
		{
			name: "matching link is current",
			ev: issueEvidence{
				Claim:    "runOrchestrate lives in orchestrate.go",
				File:     "cmd/beacon/orchestrate.go",
				Line:     2,
				Expected: "runOrchestrate",
			},
			wantStatus: EvidenceCurrent,
			wantClear:  true,
		},
		{
			name: "drifted content is marked outdated",
			ev: issueEvidence{
				Claim:    "runDaemon lives at orchestrate.go:2",
				File:     "cmd/beacon/orchestrate.go",
				Line:     2,
				Expected: "runDaemon",
			},
			wantStatus: EvidenceOutdated,
			wantNote:   "устаревшим",
		},
		{
			name: "missing line is marked outdated",
			ev: issueEvidence{
				Claim:    "helper is at orchestrate.go:99",
				File:     "cmd/beacon/orchestrate.go",
				Line:     99,
				Expected: "helper",
			},
			wantStatus: EvidenceOutdated,
			wantNote:   "устаревшим",
		},
		{
			name: "missing file is marked outdated",
			ev: issueEvidence{
				Claim: "implementation moved out of gone.go",
				File:  "cmd/beacon/gone.go",
				Line:  1,
			},
			wantStatus: EvidenceOutdated,
			wantNote:   "устаревшим",
		},
		{
			name: "absent link is reported, not accepted blindly",
			ev: issueEvidence{
				Claim: "some claim with no file:line proof",
			},
			wantStatus: EvidenceNoRef,
			wantNote:   "вслепую",
		},
		{
			name: "non-positive line is treated as no link",
			ev: issueEvidence{
				Claim: "claim with a file but no line",
				File:  "cmd/beacon/orchestrate.go",
				Line:  0,
			},
			wantStatus: EvidenceNoRef,
			wantNote:   "вслепую",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyIssueEvidence(tc.ev, code)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (note: %s)", got.Status, tc.wantStatus, got.Note)
			}
			if got.Evidence != tc.ev {
				t.Fatalf("evidence not carried through: got %+v, want %+v", got.Evidence, tc.ev)
			}
			if tc.wantNote != "" && !strings.Contains(got.Note, tc.wantNote) {
				t.Fatalf("note = %q, want it to contain %q", got.Note, tc.wantNote)
			}
			if cleared := clearsForWork(got); cleared != tc.wantClear {
				t.Fatalf("clearsForWork = %v, want %v for status %q", cleared, tc.wantClear, got.Status)
			}
		})
	}
}

// TestVerifyIssueEvidenceReportsActual proves the verdict shows both the
// expected fragment and the line that is actually in the current code, so an
// agent can see why a claim was marked outdated before it starts work.
func TestVerifyIssueEvidenceReportsActual(t *testing.T) {
	code := currentCode{"f.go": {"line one", "func realHandler() {}"}}
	got := verifyIssueEvidence(issueEvidence{
		Claim:    "oldHandler is at f.go:2",
		File:     "f.go",
		Line:     2,
		Expected: "oldHandler",
	}, code)
	if got.Status != EvidenceOutdated {
		t.Fatalf("status = %q, want outdated", got.Status)
	}
	if got.Actual != "func realHandler() {}" {
		t.Fatalf("actual = %q, want the current line", got.Actual)
	}
}

// TestGenerateFindingIDDeterministic proves HD-041: the same defect keeps the
// same id on every call, so an id derived now still names the finding on a
// later CI pass. A random or time-based id would fail this test.
func TestGenerateFindingIDDeterministic(t *testing.T) {
	const file, rule = "cmd/beacon/orchestrate.go", "LAYER-FORBIDDEN-EDGE"
	const line = 41
	want := generateFindingID(file, line, rule)
	for i := 0; i < 128; i++ {
		if got := generateFindingID(file, line, rule); got != want {
			t.Fatalf("call %d: id = %q, want stable %q", i, got, want)
		}
	}
}

// TestGenerateFindingIDDistinctFindings proves each identity field matters and
// that no two findings in the set share an id. Length-prefixing the fields is
// what keeps, for example, "a.go"/1/"RULE-B" apart from "a.go"/11/"RULE-A" and
// keeps different rules at the same location distinct.
func TestGenerateFindingIDDistinctFindings(t *testing.T) {
	base := generateFindingID("a.go", 10, "RULE-A")
	other := []struct {
		name string
		file string
		line int
		rule string
	}{
		{name: "different file", file: "b.go", line: 10, rule: "RULE-A"},
		{name: "different line", file: "a.go", line: 11, rule: "RULE-A"},
		{name: "different rule", file: "a.go", line: 10, rule: "RULE-B"},
		{name: "adjacent line different rule", file: "a.go", line: 11, rule: "RULE-B"},
	}
	seen := map[string]bool{base: true}
	for _, tc := range other {
		t.Run(tc.name, func(t *testing.T) {
			got := generateFindingID(tc.file, tc.line, tc.rule)
			if got == base {
				t.Fatalf("distinct finding got the base id %q", got)
			}
			if seen[got] {
				t.Fatalf("duplicate id %q across distinct findings", got)
			}
			seen[got] = true
		})
	}
	if len(seen) != len(other)+1 {
		t.Fatalf("got %d unique ids, want %d", len(seen), len(other)+1)
	}
}

// TestGenerateFindingIDEmptyFields proves a finding with no file, line or rule
// still gets a non-empty, well-formed and stable id instead of an empty or
// random one. It must also differ from any populated finding.
func TestGenerateFindingIDEmptyFields(t *testing.T) {
	id := generateFindingID("", 0, "")
	if id == "" {
		t.Fatal("empty finding produced an empty id")
	}
	if !strings.HasPrefix(id, "finding-") {
		t.Fatalf("id = %q, want the finding- prefix", id)
	}
	if len(id) != len("finding-")+16 {
		t.Fatalf("id = %q, want a 16-hex-character suffix", id)
	}
	for _, c := range id[len("finding-"):] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("id = %q, want lowercase hex suffix", id)
		}
	}
	if again := generateFindingID("", 0, ""); again != id {
		t.Fatalf("empty finding id is unstable: %q then %q", id, again)
	}
	if populated := generateFindingID("a.go", 1, "RULE"); populated == id {
		t.Fatalf("empty finding collided with populated id %q", id)
	}
	// Whitespace-only fields are still just fields, and still deterministic.
	ws := generateFindingID("  ", 0, "  ")
	if ws == id || ws == "" {
		t.Fatalf("whitespace finding id = %q, want a distinct non-empty id", ws)
	}
}

// TestGenerateFindingIDStableAcrossCallOrder proves the id depends only on the
// finding, not on when or in what order it is computed. A per-process counter
// or map iteration order would break this.
func TestGenerateFindingIDStableAcrossCallOrder(t *testing.T) {
	type finding struct {
		file string
		line int
		rule string
	}
	items := []finding{
		{"cmd/beacon/daemon.go", 90, "LAYER-FORBIDDEN-EDGE"},
		{"internal/infra/observe/trace.go", 12, "OBSERVE"},
		{"", 0, ""},
		{"cmd/beacon/orchestrate.go", 41, "LAYER-FORBIDDEN-EDGE"},
	}
	first := make(map[finding]string, len(items))
	for _, f := range items {
		first[f] = generateFindingID(f.file, f.line, f.rule)
	}
	for i := len(items) - 1; i >= 0; i-- {
		f := items[i]
		if got := generateFindingID(f.file, f.line, f.rule); got != first[f] {
			t.Fatalf("recomputing %+v: id = %q, want %q", f, got, first[f])
		}
	}
}

// TestCLIRunnerKeepsFullGHDiagnostic guards the merge path. gh puts the real
// refusal reason after its header, so a merge error cut at the first line
// hides why the merge failed from the merge adapter.
func TestCLIRunnerKeepsFullGHDiagnostic(t *testing.T) {
	c, _, _, _ := t06NewCLI(t, func(call t06Call) runResult {
		return runResult{
			code:   1,
			stderr: []byte("gh: merging PR 42 failed\nauthorization denied for token\ncheck the token scopes"),
		}
	})
	r := cliRunner{c: c}
	_, err := r.Run(context.Background(), "gh", "pr", "merge", "42", "--repo", "refusedguy/beacon")
	if err == nil {
		t.Fatal("want an error from a failed gh merge")
	}
	msg := err.Error()
	for _, want := range []string{"authorization denied for token", "check the token scopes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to contain %q", msg, want)
		}
	}
}

// TestGHErrorStaysOneLineForDaemonLog guards the other policy in the same
// package. The daemon logs ghError once per tick, so its text must stay on a
// single line even when stderr has several.
func TestGHErrorStaysOneLineForDaemonLog(t *testing.T) {
	res := runResult{code: 1, stderr: []byte("gh: merging PR 42 failed\nauthorization denied\ncheck the token scopes")}
	msg := ghError("gh", res).Error()
	if strings.ContainsAny(msg, "\r\n") {
		t.Fatalf("daemon error must stay on one line, got %q", msg)
	}
	if !strings.Contains(msg, "gh: merging PR 42 failed") {
		t.Fatalf("error = %q, want the first diagnostic line", msg)
	}
}
