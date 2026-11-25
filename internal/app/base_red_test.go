package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func redBase() core.BaseStatus {
	return core.BaseStatus{
		BaseBranch: "main", HeadSHA: "base1",
		FailedChecks:   []core.CheckResult{{Name: "ci", State: core.CheckFailure, Output: "test failed"}},
		FailureExcerpt: "test failed",
	}
}

func redReconciler(t *testing.T) (*Reconciler, *fakeStore, *fakeSCM, *fakeHarness, chan int) {
	t.Helper()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf(`store.Save(context.Background(), run): %v`, err)
	}
	changes := make(chan int, 4)
	scm := &fakeSCM{
		obs: core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open",
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckFailure}}, FailureExcerpt: "test failed"},
		base: redBase(),
	}
	h := &fakeHarness{}
	return &Reconciler{
		Store: store, SCM: scm, Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "ci failed"}},
		Harness: h, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, Changes: changes,
		Repo: "o/r", BaseBranch: "main", BaseRedMode: "spawn_fixer", MaxFixTries: 2,
	}, store, scm, h, changes
}

func TestRedBaseSpawnsOneFixerAndRecovers(t *testing.T) {
	r, store, scm, h, _ := redReconciler(t)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	if store.runs["r1"].State != core.RunBlocked {
		t.Fatalf("run not blocked: %s", store.runs["r1"].State)
	}
	fixers := 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("spawned %d fixers", fixers)
	}
	if len(h.wakes) != 0 {
		t.Fatalf("workers should not be woken on a red base: %d", len(h.wakes))
	}
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	fixers = 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("second reconcile spawned extra fixer: %d", fixers)
	}

	scm.base = core.BaseStatus{BaseBranch: "main", HeadSHA: "base2"}
	if err := r.checkBaseRecovery(ctx); err != nil {
		t.Fatalf(`r.checkBaseRecovery(ctx): %v`, err)
	}
	if store.runs["r1"].State != core.RunWaitingCI {
		t.Fatalf("run not recovered: %s", store.runs["r1"].State)
	}
	if scm.rebases != 1 {
		t.Fatalf("expected one update-branch, got %d", scm.rebases)
	}
	incidents, _ := store.ListOpenIncidents(ctx)
	if len(incidents) != 0 {
		t.Fatalf("incident not resolved: %+v", incidents)
	}
}

func TestRedBaseDefaultSpawnsFixer(t *testing.T) {
	r, store, _, _, _ := redReconciler(t)
	r.BaseRedMode = "" // unset config resolves to spawn_fixer
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	if store.runs["r1"].State != core.RunBlocked {
		t.Fatalf("run not blocked: %s", store.runs["r1"].State)
	}
	fixers := 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("spawned %d fixers, want 1", fixers)
	}
	incidents, err := store.ListOpenIncidents(ctx)
	if err != nil {
		t.Fatalf(`store.ListOpenIncidents(ctx): %v`, err)
	}
	if len(incidents) != 1 || incidents[0].FixerRunID == "" {
		t.Fatalf("incident missing fixer run ID: %+v", incidents)
	}
}

func assertBaseRedParentNotified(t *testing.T, h *fakeHarness) {
	t.Helper()
	if len(h.notifications) != 1 {
		t.Fatalf("parent notifications = %d, want 1", len(h.notifications))
	}
	got := h.notifications[0]
	if got.sessionID != "ses_parent" {
		t.Fatalf("notification session = %q, want ses_parent", got.sessionID)
	}
	if !strings.Contains(got.text, "base_red") {
		t.Fatalf("notification text %q missing base_red", got.text)
	}
}

func TestRedBaseNotifyOrchestratorNotifiesAndDoesNotSpawn(t *testing.T) {
	r, store, _, h, _ := redReconciler(t)
	r.BaseRedMode = "notify_orchestrator"
	n := &fakeNotifier{}
	r.Notifier = n
	store.runs["r1"].ParentSessionID = "ses_parent"
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	if n.count != 1 {
		t.Fatalf("notifier calls = %d, want 1", n.count)
	}
	if store.runs["r1"].State != core.RunBlocked {
		t.Fatalf("run not blocked: %s", store.runs["r1"].State)
	}
	fixers := 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 0 {
		t.Fatalf("spawned %d fixers, want 0", fixers)
	}
	assertBaseRedParentNotified(t, h)
}

func TestRedBaseNoNotifierStillNotifiesParent(t *testing.T) {
	r, store, _, h, _ := redReconciler(t)
	r.BaseRedMode = "notify_orchestrator"
	store.runs["r1"].ParentSessionID = "ses_parent"
	if r.Notifier != nil {
		t.Fatal("expected no notifier")
	}
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	if store.runs["r1"].State != core.RunBlocked {
		t.Fatalf("run not blocked: %s", store.runs["r1"].State)
	}
	assertBaseRedParentNotified(t, h)
}

func TestRedBaseParentNoticeCarriesReason(t *testing.T) {
	r, store, _, h, _ := redReconciler(t)
	r.BaseRedMode = "notify_orchestrator"
	r.Notifier = &fakeNotifier{}
	store.runs["r1"].ParentSessionID = "ses_parent"
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	assertBaseRedParentNotified(t, h)
	if got := h.notifications[0].text; !strings.Contains(got, "ci failed") {
		t.Fatalf("notification %q missing reason", got)
	}
}

func TestFixerInheritsDispatcherMaxAttempts(t *testing.T) {
	r, store, _, _, _ := redReconciler(t)
	r.Dispatcher = &Dispatcher{MaxAttempts: 17}
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	for _, run := range store.runs {
		if run.IsFixer && run.MaxAttempts != 17 {
			t.Fatalf("fixer max attempts = %d, want 17", run.MaxAttempts)
		}
	}
}

func TestRedEpicBranchBlocksChildrenAndSpawnsFixer(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	child := makeRun("child-1", 42, core.RunWaitingCI)
	child.TaskID = "T-EPIC-01"
	child.Branch = "beacon/T-EPIC-01"
	if err := store.Save(ctx, child); err != nil {
		t.Fatalf(`store.Save(ctx, child): %v`, err)
	}
	scm := &fakeSCM{
		obs: core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open",
			Checks:         []core.CheckResult{{Name: "ci", State: core.CheckFailure}},
			FailureExcerpt: "test failed"},
		base: core.BaseStatus{BaseBranch: "beacon/T-EPIC", HeadSHA: "base1",
			FailedChecks: []core.CheckResult{{Name: "ci", State: core.CheckFailure, Output: "test failed"}}},
	}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "ci failed"}},
		Harness: h, Source: epicSource(core.ReviewModeLLM),
		Workspace: &fakeWorkspace{}, Repo: "o/r", BaseBranch: "main",
		BaseRedMode: "spawn_fixer", MaxFixTries: 2,
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	incidents, err := store.ListOpenIncidents(ctx)
	if err != nil {
		t.Fatalf(`store.ListOpenIncidents(ctx): %v`, err)
	}
	if len(incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(incidents))
	}
	if incidents[0].BaseBranch != "beacon/T-EPIC" {
		t.Fatalf("incident base = %q, want beacon/T-EPIC", incidents[0].BaseBranch)
	}
	if got := store.runs["child-1"]; got.State != core.RunBlocked {
		t.Fatalf("child state = %s, want blocked", got.State)
	}
	fixers := 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("fixers = %d, want 1", fixers)
	}
	found := false
	for _, prompt := range h.prompts {
		if strings.Contains(prompt, "beacon/T-EPIC") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixer prompt missing epic branch: %v", h.prompts)
	}
}

func TestSqliteChecksRoundTripsUTF8AndControlCharacters(t *testing.T) {
	names := []string{"ci\vbuild", "日本語", "quote \" and \\ slash"}
	raw := sqliteChecks(names)
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("sqliteChecks produced invalid JSON %q: %v", raw, err)
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("round trip = %q, want %q", got, names)
	}
}

func TestFixerPromptCarriesValidFailedCheckJSON(t *testing.T) {
	names := []string{"ci\vbuild", "日本語"}
	raw := sqliteChecks(names)
	base := core.BaseStatus{BaseBranch: "main", FailureExcerpt: "boom"}
	in := &core.BaseIncident{FailedChecksJSON: raw}
	prompt := fixerPrompt(base, in, core.Task{}, "")

	const marker = "Failed checks: "
	i := strings.Index(prompt, marker)
	if i < 0 {
		t.Fatalf("prompt missing failed checks: %q", prompt)
	}
	line := prompt[i+len(marker):]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	var got []string
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("fixer prompt JSON %q is invalid: %v", line, err)
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("fixer prompt checks = %q, want %q", got, names)
	}
}

func TestIdleAutoAttachesPRByBranch(t *testing.T) {
	store := newFakeStore()
	run := makeRun("r1", 0, core.RunRunning)
	run.Branch = "beacon/T-1"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf(`store.Save(context.Background(), run): %v`, err)
	}
	changes := make(chan int, 4)
	r := &Reconciler{
		Store: store, SCM: &fakeSCM{foundPR: 42}, Harness: &fakeHarness{},
		Workspace: &fakeWorkspace{}, Changes: changes, BaseBranch: "main",
	}
	r.HandleHarnessEvent(context.Background(), core.HarnessEvent{RunID: "r1", Kind: "idle"})
	got := store.runs["r1"]
	if got.PRNumber != 42 || got.State != core.RunWaitingCI {
		t.Fatalf("auto attach failed: %+v", got)
	}
	select {
	case pr := <-changes:
		if pr != 42 {
			t.Fatalf("wrong pr signal: %d", pr)
		}
	default:
		t.Fatal("no pr change signal")
	}
}
