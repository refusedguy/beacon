package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // register the "sqlite" driver for the store adapter

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/checksource"
	"github.com/refusedguy/beacon/internal/infra/policy"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// fakeExplainSCM reports one observation and records every PR it was asked to
// observe. It has no write method.
type fakeExplainSCM struct {
	obs core.Observation
	err error
	prs []int
}

func (f *fakeExplainSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	f.prs = append(f.prs, pr)
	if f.err != nil {
		return core.Observation{}, f.err
	}
	return f.obs, nil
}

func (f *fakeExplainSCM) UpdateBranch(context.Context, int, string) (string, error) {
	return "", nil
}

func (f *fakeExplainSCM) Merge(context.Context, int, string, string) error { return nil }

func (f *fakeExplainSCM) BaseBranchStatus(context.Context, string) (core.BaseStatus, error) {
	return core.BaseStatus{}, nil
}

func (f *fakeExplainSCM) FindOpenPRByBranch(context.Context, string) (int, error) {
	return 0, nil
}

func (f *fakeExplainSCM) OpenPR(context.Context, string, string, string) (int, error) {
	return 0, nil
}

type approvalCall struct {
	id  core.RunID
	sha string
}

// fakeExplainStore exposes only the read surface an explain call needs.
type fakeExplainStore struct {
	run       *core.Run
	approved  bool
	err       error
	findCalls int
	calls     []approvalCall
}

func (f *fakeExplainStore) FindByPR(context.Context, int) (*core.Run, error) {
	f.findCalls++
	return f.run, f.err
}

func (f *fakeExplainStore) Approved(_ context.Context, id core.RunID, sha string) (bool, error) {
	f.calls = append(f.calls, approvalCall{id: id, sha: sha})
	return f.approved, nil
}

func explainService(store *fakeExplainStore, scm *fakeExplainSCM, p *policy.Policy) app.ExplainService {
	return app.ExplainService{Store: store, SCM: scm, Policy: p}
}

func stepVerdicts(steps []app.ExplainStep) map[string]core.Verdict {
	out := make(map[string]core.Verdict, len(steps))
	for _, step := range steps {
		out[step.Name] = step.Verdict
	}
	return out
}

func TestExplainTracesEveryVerdict(t *testing.T) {
	p := policy.New(policy.Config{
		RequiredCheckSource: checksource.Config{Explicit: []string{"build"}},
		RequiredApprovals:   1,
		Labels:              []string{"ready"},
	})
	scm := &fakeExplainSCM{obs: core.Observation{
		PRState:           "open",
		HeadSHA:           "head",
		Incomplete:        true,
		IncompleteReason:  "checks API failed",
		Conflict:          true,
		UnresolvedThreads: []string{"thread-1"},
		Checks:            []core.CheckResult{{Name: "build", State: core.CheckFailure}},
		FailureExcerpt:    "build broke",
		Behind:            true,
	}}
	store := &fakeExplainStore{run: &core.Run{ID: "r1"}}

	res, err := explainService(store, scm, p).Explain(context.Background(), 42)
	if err != nil {
		t.Fatalf("Explain(ctx, 42): %v", err)
	}
	want := map[string]core.Verdict{
		"invariant/incomplete":         core.VerdictWait,
		"invariant/conflict":           core.VerdictNeedsAgentFix,
		"invariant/unresolved_threads": core.VerdictNeedsAgentFix,
		"invariant/failed_check":       core.VerdictNeedsAgentFix,
		"invariant/behind":             core.VerdictNeedsRebase,
		"required_checks":              core.VerdictWait,
		"approvals":                    core.VerdictWait,
		"labels":                       core.VerdictWait,
	}
	got := stepVerdicts(res.Steps)
	if len(got) != len(want) {
		t.Fatalf("steps = %+v, want %d entries", got, len(want))
	}
	for name, verdict := range want {
		if got[name] != verdict {
			t.Errorf("step %s verdict = %q, want %q", name, got[name], verdict)
		}
	}
	if res.Verdict != core.VerdictWait || res.Reason != "observation is incomplete: checks API failed" {
		t.Fatalf("summary = %q %q", res.Verdict, res.Reason)
	}
	if !strings.Contains(res.Steps[0].Reason, "checks API failed") {
		t.Fatalf("first step reason = %q", res.Steps[0].Reason)
	}
	if scm.prs[0] != 42 {
		t.Fatalf("observed PRs = %v, want [42]", scm.prs)
	}
}

func TestExplainMarksSideEffectRuleWithoutRunningIt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gate-ran")
	p := policy.New(policy.Config{CommandGate: "printf ran > " + marker})
	scm := &fakeExplainSCM{obs: core.Observation{PRState: "open", HeadSHA: "head"}}
	store := &fakeExplainStore{run: &core.Run{ID: "r1"}}

	res, err := explainService(store, scm, p).Explain(context.Background(), 9)
	if err != nil {
		t.Fatalf("Explain(ctx, 9): %v", err)
	}
	var gate *app.ExplainStep
	for i := range res.Steps {
		if res.Steps[i].Name == "command_gate" {
			gate = &res.Steps[i]
		}
	}
	if gate == nil {
		t.Fatalf("command_gate is missing from %+v", res.Steps)
	}
	if !gate.SideEffect || gate.Verdict != core.VerdictNoop {
		t.Fatalf("gate = %+v, want a skipped noop step", gate)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gate marker exists or stat failed: %v", err)
	}
	if res.Verdict != core.VerdictMerge || res.Reason != "all criteria passed" {
		t.Fatalf("summary = %q %q, want a clean dry run", res.Verdict, res.Reason)
	}
}

func TestExplainWithoutARunStillTraces(t *testing.T) {
	p := policy.New(policy.Config{RequiredApprovals: 1})
	scm := &fakeExplainSCM{obs: core.Observation{PRState: "open", HeadSHA: "head"}}
	store := &fakeExplainStore{}

	res, err := explainService(store, scm, p).Explain(context.Background(), 77)
	if err != nil {
		t.Fatalf("Explain(ctx, 77): %v", err)
	}
	if res.RunID != "" {
		t.Fatalf("run ID = %q, want empty", res.RunID)
	}
	if store.findCalls != 1 || len(store.calls) != 0 {
		t.Fatalf("store calls = %d finds, %d approval lookups", store.findCalls, len(store.calls))
	}
	if got := stepVerdicts(res.Steps)["approvals"]; got != core.VerdictWait {
		t.Fatalf("approvals verdict = %q, want wait", got)
	}
}

func TestExplainCountsStoredApproval(t *testing.T) {
	p := policy.New(policy.Config{RequiredApprovals: 1})
	scm := &fakeExplainSCM{obs: core.Observation{PRState: "open", HeadSHA: "head"}}
	store := &fakeExplainStore{run: &core.Run{ID: "r7"}, approved: true}

	res, err := explainService(store, scm, p).Explain(context.Background(), 7)
	if err != nil {
		t.Fatalf("Explain(ctx, 7): %v", err)
	}
	if len(store.calls) != 1 || store.calls[0].id != "r7" || store.calls[0].sha != "head" {
		t.Fatalf("approval calls = %+v", store.calls)
	}
	if got := stepVerdicts(res.Steps)["approvals"]; got != core.VerdictNoop {
		t.Fatalf("approvals verdict = %q, want noop", got)
	}
	if res.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %q, want merge", res.Verdict)
	}
}

func TestExplainStopsAtClosedPullRequest(t *testing.T) {
	p := policy.New(policy.Config{RequiredApprovals: 1})
	scm := &fakeExplainSCM{obs: core.Observation{PRState: "merged", HeadSHA: "head"}}
	store := &fakeExplainStore{run: &core.Run{ID: "r1"}}

	res, err := explainService(store, scm, p).Explain(context.Background(), 5)
	if err != nil {
		t.Fatalf("Explain(ctx, 5): %v", err)
	}
	if len(res.Steps) != 1 || res.Steps[0].Name != "invariant/closed" {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if res.Verdict != core.VerdictNoop || res.Reason != "pull request is merged" {
		t.Fatalf("summary = %q %q", res.Verdict, res.Reason)
	}
}

func TestExplainOutputShowsChainAndJSONKeepsSideEffects(t *testing.T) {
	p := policy.New(policy.Config{
		RequiredCheckSource: checksource.Config{Explicit: []string{"build", "test"}},
		CommandGate:         "exit 0",
	})
	scm := &fakeExplainSCM{obs: core.Observation{
		PRState: "open",
		Checks: []core.CheckResult{
			{Name: "build", State: core.CheckSuccess},
			{Name: "test", State: core.CheckSuccess},
		},
	}}
	store := &fakeExplainStore{run: &core.Run{ID: "r1"}}
	res, err := explainService(store, scm, p).Explain(context.Background(), 12)
	if err != nil {
		t.Fatalf("Explain(ctx, 12): %v", err)
	}

	text := app.FormatExplain(res)
	for _, want := range []string{"PR 12 dry-run verdict: merge", "reason: all criteria passed", "required_checks: noop", "config: build, test", "command_gate: skipped"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q does not contain %q", text, want)
		}
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal(res): %v", err)
	}
	var decoded struct {
		Steps []struct {
			Name       string `json:"name"`
			SideEffect bool   `json:"side_effect"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(data): %v", err)
	}
	found := false
	for _, step := range decoded.Steps {
		if step.Name == "command_gate" && step.SideEffect {
			found = true
		}
	}
	if !found {
		t.Fatalf("JSON lost the side-effect mark: %s", data)
	}
}

func TestExplainDoesNotWriteState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("sqlite.Open(path): %v", err)
	}
	defer store.Close()
	run := &core.Run{ID: "r1", TaskID: "T1", State: core.RunWaitingCI, PRNumber: 42, HeadSHA: "head"}
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}
	before, err := store.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("store.Get(ctx, \"r1\") before: %v", err)
	}
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path) before: %v", err)
	}

	scm := &fakeExplainSCM{obs: core.Observation{PRState: "open", HeadSHA: "head"}}
	p := policy.New(policy.Config{RequiredApprovals: 1})
	service := app.ExplainService{Store: store, SCM: scm, Policy: p}
	if _, err := service.Explain(ctx, 42); err != nil {
		t.Fatalf("service.Explain(ctx, 42): %v", err)
	}

	after, err := store.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("store.Get(ctx, \"r1\") after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("run changed: before %+v after %+v", before, after)
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path) after: %v", err)
	}
	if sha256.Sum256(beforeBytes) != sha256.Sum256(afterBytes) {
		t.Fatal("state.db bytes changed during explain")
	}
}
