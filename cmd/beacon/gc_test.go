package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
)

func mockServeGC(t *testing.T) (*bytes.Buffer, *bytes.Buffer, *[]int, *int) {
	t.Helper()
	oldList, oldPID, oldCfg := gcServeList, gcServicePID, gcServiceCfg
	oldState, oldClear, oldKill := gcServeState, gcServeClear, gcServeKill
	oldAlive, oldRead, oldWrite := gcProcessAlive, gcReadState, gcWriteState
	oldObligations := gcObligationsForGC
	oldSessions, oldDelete, oldClose := gcSessionsForGC, gcSessionDelete, gcSessionClose
	oldStale, oldTree := gcStaleSessions, gcOpenCodeSessions
	t.Cleanup(func() {
		gcServeList, gcServicePID, gcServiceCfg = oldList, oldPID, oldCfg
		gcServeState, gcServeClear, gcServeKill = oldState, oldClear, oldKill
		gcProcessAlive, gcReadState, gcWriteState = oldAlive, oldRead, oldWrite
		gcObligationsForGC = oldObligations
		gcSessionsForGC, gcSessionDelete, gcSessionClose = oldSessions, oldDelete, oldClose
		gcStaleSessions, gcOpenCodeSessions = oldStale, oldTree
	})

	// Obligations and session candidates need the state store; the serve
	// collector tests do not, so keep both seams quiet here and cover them in
	// their own tests below.
	gcObligationsForGC = func(*CLI, bool) ([]store.ObligationGCItem, error) {
		return nil, nil
	}
	gcSessionsForGC = func(*CLI) ([]gcSessionCandidate, error) { return nil, nil }
	gcSessionDelete = func(context.Context, string) error { return nil }

	killed := make([]int, 0)
	cleared := 0
	gcServeKill = func(pid int) error {
		killed = append(killed, pid)
		return nil
	}
	gcServeClear = func() error {
		cleared++
		return nil
	}
	gcProcessAlive = func(pid int) bool { return pid > 0 && pid < 500 }
	gcReadState = func() (gcState, error) { return gcState{}, nil }
	gcWriteState = func([]gcItem) ([]gcItem, error) { return []gcItem{}, nil }
	gcServiceCfg = func() (opencode.Config, error) {
		return opencode.Config{URL: "http://127.0.0.1:4096"}, nil
	}

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return out, errOut, &killed, &cleared
}

func TestQA2ServePlanProtectsMainAndOwnServer(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	_ = out
	_ = errOut
	_ = killed
	_ = cleared

	serves := []opencode.ServeProcess{
		{PID: 100, Port: "4096"}, // main session from service.json
		{PID: 200, Port: "5000"}, // Beacon's own recorded server
		{PID: 300, Port: "6000"}, // orphan
		{PID: 400, Port: ""},     // orphan without a port
	}
	recorded := opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 200}
	plan := gcServePlan(serves, 100, "4096", recorded, true)
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want 2 orphans", plan)
	}
	for _, item := range plan {
		if item.ID == "100" || item.ID == "200" {
			t.Fatalf("защищённый процесс попал в план: %+v", item)
		}
		if item.Kind != gcKindServe || item.Reason != gcReasonOrphan {
			t.Fatalf("неверный item: %+v", item)
		}
	}
}

func TestQA2ServePlanProtectsMainByPort(t *testing.T) {
	mockServeGC(t)
	serves := []opencode.ServeProcess{
		{PID: 700, Port: "4096"}, // no pid in service.json, port still protects
		{PID: 800, Port: "6000"},
	}
	plan := gcServePlan(serves, 0, "4096", opencode.ServeState{}, false)
	if len(plan) != 1 || plan[0].ID != "800" {
		t.Fatalf("plan = %+v, want only pid 800", plan)
	}
}

func TestQA2ServePlanMarksDeadRecord(t *testing.T) {
	mockServeGC(t)
	gcProcessAlive = func(pid int) bool { return false }
	recorded := opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 999}
	plan := gcServePlan(nil, 0, "", recorded, true)
	if len(plan) != 1 || plan[0].ID != "state:999" || plan[0].Reason != gcReasonDead {
		t.Fatalf("plan = %+v, want stale state:999", plan)
	}
}

func TestQA2GCDryRunDoesNotKill(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) {
		return []opencode.ServeProcess{{PID: 100, Port: "4096"}, {PID: 300, Port: "6000"}}, nil
	}
	gcServicePID = func() int { return 100 }
	gcServeState = func() (opencode.ServeState, bool) {
		return opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 200}, true
	}

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, nil); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if len(*killed) != 0 || *cleared != 0 {
		t.Fatalf("dry-run killed %v cleared %d", *killed, *cleared)
	}
	if !strings.Contains(out.String(), "300") || strings.Contains(out.String(), "100") {
		t.Fatalf("out = %q", out.String())
	}
	if !strings.Contains(out.String(), "НАЙДЕНО: 1") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestQA2GCApplyKillsOnlyOrphans(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) {
		return []opencode.ServeProcess{
			{PID: 100, Port: "4096"},
			{PID: 200, Port: "5000"},
			{PID: 300, Port: "6000"},
			{PID: 400, Port: ""},
		}, nil
	}
	gcServicePID = func() int { return 100 }
	gcServeState = func() (opencode.ServeState, bool) {
		return opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 200}, true
	}

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if len(*killed) != 2 || (*killed)[0] != 300 || (*killed)[1] != 400 {
		t.Fatalf("killed = %v, want [300 400]", *killed)
	}
	if *cleared != 0 {
		t.Fatalf("живая запись очищена: %d", *cleared)
	}
	if !strings.Contains(out.String(), "УДАЛЕНО: 2") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestQA2GCApplyClearsDeadRecord(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) { return nil, nil }
	gcServeState = func() (opencode.ServeState, bool) {
		return opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 999}, true
	}
	gcProcessAlive = func(pid int) bool { return false }

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply", "--format", "json"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if *cleared != 1 {
		t.Fatalf("cleared = %d, want 1", *cleared)
	}
	if len(*killed) != 0 {
		t.Fatalf("killed = %v", *killed)
	}
	if !strings.Contains(out.String(), `"mode":"applied"`) || !strings.Contains(out.String(), "state:999") {
		t.Fatalf("out = %q", out.String())
	}
}

// TestGCObligationsCollectsClosedWatch verifies the command wiring: a dry run
// lists the store's obligation items, and --apply reports them as removed. The
// eligibility rule itself is tested in the store package.
func TestGCObligationsCollectsClosedWatch(t *testing.T) {
	out, errOut, _, _ := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) { return nil, nil }
	gcServeState = func() (opencode.ServeState, bool) { return opencode.ServeState{}, false }

	applies := make([]bool, 0, 2)
	gcObligationsForGC = func(_ *CLI, apply bool) ([]store.ObligationGCItem, error) {
		applies = append(applies, apply)
		return []store.ObligationGCItem{
			{ID: "old:1:s1", WatchID: "old", Reason: "closed"},
			{ID: "arch:2:s1", WatchID: "arch", Reason: "archived"},
		}, nil
	}

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("dry run code = %d, stderr = %s", code, errOut.String())
	}
	dry := out.String()
	if !strings.Contains(dry, `"mode":"dry-run"`) || !strings.Contains(dry, `"removed":2`) ||
		!strings.Contains(dry, `"kind":"obligation"`) ||
		!strings.Contains(dry, "old:1:s1") || !strings.Contains(dry, "arch:2:s1") {
		t.Fatalf("dry run out = %q", dry)
	}

	out.Reset()
	if code := runGCWithServe(c, []string{"--format", "json", "--apply"}); code != 0 {
		t.Fatalf("apply code = %d, stderr = %s", code, errOut.String())
	}
	applied := out.String()
	if !strings.Contains(applied, `"mode":"applied"`) || !strings.Contains(applied, `"removed":2`) ||
		!strings.Contains(applied, "old:1:s1") {
		t.Fatalf("apply out = %q", applied)
	}

	if len(applies) != 2 || applies[0] || !applies[1] {
		t.Fatalf("seam apply flags = %v, want [false true]", applies)
	}
}

func TestQA2GCListFailure(t *testing.T) {
	out, errOut, _, _ := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) {
		return nil, errors.New("/proc недоступен")
	}
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func gcWatchWithSubscribers(id string, ids ...string) domain.Watch {
	subs := make([]domain.Subscriber, 0, len(ids))
	for _, sid := range ids {
		subs = append(subs, domain.Subscriber{ID: sid})
	}
	return domain.Watch{ID: id, Subscribers: subs}
}

// TestGCSessionPlanProtectsLiveAndCurrentSession covers the protection rule:
// a subscriber of a live Watch and the process's own session stay alive, and a
// session shared by two closed Watches is planned once.
func TestGCSessionPlanProtectsLiveAndCurrentSession(t *testing.T) {
	closed := []domain.Watch{
		gcWatchWithSubscribers("w1", "ses_closed", "ses_live", "ses_current"),
		gcWatchWithSubscribers("w2", "ses_closed", "ses_second"),
	}
	live := []domain.Watch{gcWatchWithSubscribers("w3", "ses_live")}
	plan := gcSessionPlan(closed, live, "ses_current")
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want 2 candidates", plan)
	}
	if plan[0].SessionID != "ses_closed" || plan[0].WatchID != "w1" ||
		plan[1].SessionID != "ses_second" || plan[1].WatchID != "w2" {
		t.Fatalf("plan = %+v, want ses_closed/w1 and ses_second/w2", plan)
	}
}

// fakeGCSessionStore is the inspect store slice the session collector reads.
type fakeGCSessionStore struct {
	closed []domain.Watch
	live   []domain.Watch
	since  time.Time
	limit  int
}

func (f *fakeGCSessionStore) RecentClosedWatches(_ context.Context, since time.Time, limit int) ([]domain.Watch, error) {
	f.since, f.limit = since, limit
	return f.closed, nil
}

func (f *fakeGCSessionStore) LiveWatches() ([]domain.Watch, error) { return f.live, nil }

func (f *fakeGCSessionStore) ActiveBlockers() ([]domain.BlockerIncident, error) { return nil, nil }

func (f *fakeGCSessionStore) BoundOrchestrator(string) (domain.OrchestratorBinding, bool, error) {
	return domain.OrchestratorBinding{}, false, nil
}

func (f *fakeGCSessionStore) LoadRuntimeStats() ([]byte, bool, error) { return nil, false, nil }

// TestGCSessionsCollectorUsesRetentionWindowAndEnv verifies the store call:
// the window comes from gcNow, and the current session id comes from the
// environment the daemon uses for its own session.
func TestGCSessionsCollectorUsesRetentionWindowAndEnv(t *testing.T) {
	oldNow, oldFactory := gcNow, makeInspectStore
	fixed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	gcNow = func() time.Time { return fixed }
	t.Cleanup(func() { gcNow, makeInspectStore = oldNow, oldFactory })
	t.Setenv(sessionIDEnv, "ses_current")

	f := &fakeGCSessionStore{
		closed: []domain.Watch{gcWatchWithSubscribers("w1", "ses_closed", "ses_current")},
		live:   []domain.Watch{gcWatchWithSubscribers("w2", "ses_live")},
	}
	makeInspectStore = func(*CLI) (inspectStore, func() error, error) {
		return f, func() error { return nil }, nil
	}
	gcStaleSessions = func(int) ([]session.Session, error) { return nil, nil }
	gcOpenCodeSessions = func(context.Context) ([]opencode.Session, error) { return nil, nil }

	got, err := gcSessionsForGC(&CLI{})
	if err != nil {
		t.Fatalf("gcSessionsForGC: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "ses_closed" || got[0].WatchID != "w1" {
		t.Fatalf("candidates = %+v, want only ses_closed/w1", got)
	}
	if !f.since.Equal(fixed.Add(-gcSessionRetention)) || f.limit != gcSessionLimit {
		t.Fatalf("store call since = %v limit = %d", f.since, f.limit)
	}
}

func mockSessionGC(t *testing.T, candidates ...gcSessionCandidate) (*bytes.Buffer, *bytes.Buffer, *[]string) {
	t.Helper()
	out, errOut, _, _ := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) { return nil, nil }
	gcServeState = func() (opencode.ServeState, bool) { return opencode.ServeState{}, false }
	gcSessionsForGC = func(*CLI) ([]gcSessionCandidate, error) { return candidates, nil }
	deletes := make([]string, 0, len(candidates))
	gcSessionDelete = func(_ context.Context, id string) error {
		deletes = append(deletes, id)
		return nil
	}
	return out, errOut, &deletes
}

// TestGCSessionsDryRunListsWithoutDeleting checks that a dry run reports the
// candidate as a session item and never calls the deleter.
func TestGCSessionsDryRunListsWithoutDeleting(t *testing.T) {
	out, errOut, deletes := mockSessionGC(t, gcSessionCandidate{SessionID: "ses_closed", WatchID: "w1"})
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("dry run code = %d, stderr = %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, `"mode":"dry-run"`) || !strings.Contains(got, `"removed":1`) ||
		!strings.Contains(got, `"kind":"session"`) || !strings.Contains(got, "ses_closed") {
		t.Fatalf("dry run out = %q", got)
	}
	if len(*deletes) != 0 {
		t.Fatalf("dry run deleted %v", *deletes)
	}
}

// TestGCSessionsApplyDeletesClosedOnce checks the --apply contract: each
// candidate is deleted exactly once and reported as removed.
func TestGCSessionsApplyDeletesClosedOnce(t *testing.T) {
	out, errOut, deletes := mockSessionGC(t,
		gcSessionCandidate{SessionID: "ses_closed", WatchID: "w1"},
		gcSessionCandidate{SessionID: "ses_second", WatchID: "w2"},
	)
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--format", "json", "--apply"}); code != 0 {
		t.Fatalf("apply code = %d, stderr = %s", code, errOut.String())
	}
	if len(*deletes) != 2 || (*deletes)[0] != "ses_closed" || (*deletes)[1] != "ses_second" {
		t.Fatalf("deletes = %v, want each session once", *deletes)
	}
	got := out.String()
	if !strings.Contains(got, `"mode":"applied"`) || !strings.Contains(got, `"removed":2`) ||
		!strings.Contains(got, "ses_closed") || !strings.Contains(got, "ses_second") {
		t.Fatalf("apply out = %q", got)
	}
}

func TestGCSessionsDeleteFailureFailsSweep(t *testing.T) {
	out, errOut, _ := mockSessionGC(t, gcSessionCandidate{SessionID: "ses_closed", WatchID: "w1"})
	gcSessionDelete = func(context.Context, string) error { return errors.New("403") }
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply"}); code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "удаление сессии ses_closed") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// mockStaleSessionGC records both the OpenCode delete and the registry close
// so the tests can assert their order and that a failed delete leaves the row
// open.
func mockStaleSessionGC(t *testing.T, candidates ...gcSessionCandidate) (*bytes.Buffer, *bytes.Buffer, *[]string, *[]string) {
	t.Helper()
	out, errOut, _, _ := mockServeGC(t)
	oldSessions, oldDelete, oldClose := gcSessionsForGC, gcSessionDelete, gcSessionClose
	t.Cleanup(func() { gcSessionsForGC, gcSessionDelete, gcSessionClose = oldSessions, oldDelete, oldClose })
	gcSessionsForGC = func(*CLI) ([]gcSessionCandidate, error) { return candidates, nil }
	deletes := make([]string, 0, len(candidates))
	closes := make([]string, 0, len(candidates))
	gcSessionDelete = func(_ context.Context, id string) error {
		deletes = append(deletes, id)
		return nil
	}
	gcSessionClose = func(id string) error {
		closes = append(closes, id)
		return nil
	}
	return out, errOut, &deletes, &closes
}

// TestGCSessionsStaleDryRunReportsReason checks that a stale row is planned
// with the stale_session reason and that a dry run never deletes or closes.
func TestGCSessionsStaleDryRunReportsReason(t *testing.T) {
	out, errOut, deletes, closes := mockStaleSessionGC(t, gcSessionCandidate{SessionID: "ses_stale", Stale: true})
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("dry run code = %d, stderr = %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "stale_session") || !strings.Contains(got, "ses_stale") {
		t.Fatalf("dry run out = %q", got)
	}
	if len(*deletes) != 0 || len(*closes) != 0 {
		t.Fatalf("dry run deletes = %v closes = %v", *deletes, *closes)
	}
}

// TestGCStalePlanSkipsLiveSubscriber checks that a stale row belonging to a
// live subscriber is never a candidate.
func TestGCStalePlanSkipsLiveSubscriber(t *testing.T) {
	live := []domain.Watch{gcWatchWithSubscribers("w1", "ses_live")}
	stale := []session.Session{{ID: "ses_live"}, {ID: "ses_dead"}}
	plan := gcStaleSessionPlan(stale, live, "", nil)
	if len(plan) != 1 || plan[0].SessionID != "ses_dead" || !plan[0].Stale {
		t.Fatalf("plan = %+v, want only ses_dead as stale", plan)
	}
}

// TestGCStalePlanSkipsAncestorOfLiveSubscriber checks the session tree rule:
// OpenCode DELETE removes children, so an ancestor of a live subscriber must
// stay out of the plan even when its own registry row went stale.
func TestGCStalePlanSkipsAncestorOfLiveSubscriber(t *testing.T) {
	live := []domain.Watch{gcWatchWithSubscribers("w1", "ses_child")}
	tree := []opencode.Session{
		{ID: "ses_grand"},
		{ID: "ses_parent", ParentID: "ses_grand"},
		{ID: "ses_child", ParentID: "ses_parent"},
	}
	stale := []session.Session{{ID: "ses_grand"}, {ID: "ses_parent"}, {ID: "ses_other"}}
	plan := gcStaleSessionPlan(stale, live, "", tree)
	if len(plan) != 1 || plan[0].SessionID != "ses_other" {
		t.Fatalf("plan = %+v, want only ses_other", plan)
	}
}

// TestGCSessionsStaleDeleteFailureLeavesRowOpen checks that a rejected DELETE
// never reaches the registry close.
func TestGCSessionsStaleDeleteFailureLeavesRowOpen(t *testing.T) {
	out, errOut, deletes, closes := mockStaleSessionGC(t, gcSessionCandidate{SessionID: "ses_stale", Stale: true})
	gcSessionDelete = func(_ context.Context, id string) error {
		*deletes = append(*deletes, id)
		return errors.New("403")
	}
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply"}); code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if len(*deletes) != 1 || len(*closes) != 0 {
		t.Fatalf("deletes = %v closes = %v, want delete only and no close", *deletes, *closes)
	}
}

// TestGCSessionsStaleApplyClosesAfterDelete checks the apply order: the
// registry row closes only after a successful OpenCode delete.
func TestGCSessionsStaleApplyClosesAfterDelete(t *testing.T) {
	out, errOut, deletes, closes := mockStaleSessionGC(t, gcSessionCandidate{SessionID: "ses_stale", Stale: true})
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--format", "json", "--apply"}); code != 0 {
		t.Fatalf("apply code = %d, stderr = %s", code, errOut.String())
	}
	if len(*deletes) != 1 || (*deletes)[0] != "ses_stale" || len(*closes) != 1 || (*closes)[0] != "ses_stale" {
		t.Fatalf("deletes = %v closes = %v, want ses_stale in both", *deletes, *closes)
	}
	if !strings.Contains(out.String(), "stale_session") {
		t.Fatalf("apply out = %q", out.String())
	}
}

// TestGCSessionsForGCDedupesSources checks that an id found both in a closed
// Watch and in the stale registry is deleted once, and keeps the Watch id so
// the dry-run report stays accurate.
func TestGCSessionsForGCDedupesSources(t *testing.T) {
	oldNow, oldFactory := gcNow, makeInspectStore
	oldStale, oldTree := gcStaleSessions, gcOpenCodeSessions
	t.Cleanup(func() {
		gcNow, makeInspectStore, gcStaleSessions, gcOpenCodeSessions = oldNow, oldFactory, oldStale, oldTree
	})
	fixed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	gcNow = func() time.Time { return fixed }
	t.Setenv(sessionIDEnv, "ses_current")
	f := &fakeGCSessionStore{
		closed: []domain.Watch{gcWatchWithSubscribers("w1", "ses_dup")},
	}
	makeInspectStore = func(*CLI) (inspectStore, func() error, error) {
		return f, func() error { return nil }, nil
	}
	gcStaleSessions = func(int) ([]session.Session, error) {
		return []session.Session{{ID: "ses_dup"}, {ID: "ses_only_stale"}}, nil
	}
	gcOpenCodeSessions = func(context.Context) ([]opencode.Session, error) { return nil, nil }

	got, err := gcSessionsForGC(&CLI{})
	if err != nil {
		t.Fatalf("gcSessionsForGC: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want 2 unique ids", got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.SessionID] {
			t.Fatalf("candidates = %+v, duplicate id %s", got, c.SessionID)
		}
		seen[c.SessionID] = true
	}
	if !seen["ses_dup"] || !seen["ses_only_stale"] {
		t.Fatalf("candidates = %+v, want ses_dup and ses_only_stale", got)
	}
	for _, c := range got {
		if c.SessionID == "ses_dup" && (!c.Stale || c.WatchID != "w1") {
			t.Fatalf("candidate %+v, want merged stale row with WatchID w1", c)
		}
	}
}

// gcCompactFixture is five commits in commit order with monotone dates. The
// first two sit before the cutoff, the last three after it.
func gcCompactFixture() []gcCompactCommit {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	commits := make([]gcCompactCommit, 0, 5)
	for i := 0; i < 5; i++ {
		commits = append(commits, gcCompactCommit{
			Hash:  fmt.Sprintf("hash-%d", i),
			Date:  base.Add(time.Duration(i) * 24 * time.Hour),
			Order: i,
		})
	}
	return commits
}

func gcCompactSeams(t *testing.T, commits []gcCompactCommit, head string, applied *int) {
	t.Helper()
	oldRead, oldApply := gcCompactRead, gcCompactApply
	t.Cleanup(func() { gcCompactRead, gcCompactApply = oldRead, oldApply })
	gcCompactRead = func(string) ([]gcCompactCommit, string, string, error) { return commits, head, head, nil }
	gcCompactApply = func(gcCompactPlan) (string, error) {
		if applied != nil {
			*applied = *applied + 1
		}
		return head, nil
	}
}

// TestGCCompactRefusesOutOfOrderDates is the first measurable check: five
// commits whose dates cross the cutoff against history order produce a refusal
// with a hint, zero created commits and no apply call.
func TestGCCompactRefusesOutOfOrderDates(t *testing.T) {
	commits := gcCompactFixture()
	commits[2].Date = commits[2].Date.Add(-30 * 24 * time.Hour)
	// Commit 2 gets an old date but keeps its late position in history. The
	// cutoff must place commit 1 on the recent side for the dates to cross.
	cutoff := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	plan := gcCompactBuild(commits, cutoff, "hash-4", "hash-4")
	if plan.Reason != gcCompactReasonOutOfOrder {
		t.Fatalf("reason = %q, want %q", plan.Reason, gcCompactReasonOutOfOrder)
	}
	if plan.gcCompactCreated() != 0 || len(plan.gcCompactReplays()) != 0 {
		t.Fatalf("created = %d replays = %v, want 0 and empty", plan.gcCompactCreated(), plan.gcCompactReplays())
	}
	if plan.Hint == "" {
		t.Fatal("refusal has no hint")
	}

	applied := 0
	gcCompactSeams(t, commits, "hash-4", &applied)
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runGCCompact(c, []string{"--cutoff", "2026-11-01T12:00:00Z", "--format", "json"}); code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if applied != 0 {
		t.Fatalf("apply called %d times on refusal", applied)
	}
	got := out.String()
	if !strings.Contains(got, `"mode":"refusal"`) || !strings.Contains(got, `"created":0`) ||
		!strings.Contains(got, "не сжимать") {
		t.Fatalf("out = %q, want refusal, zero created and hint", got)
	}
}

// TestGCCompactMonotonicBuildsBaseAndReplays is the second measurable check:
// one base commit, three replays in commit order, and a result tree that matches
// the captured headHash byte for byte.
func TestGCCompactMonotonicBuildsBaseAndReplays(t *testing.T) {
	commits := gcCompactFixture()
	cutoff := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)

	plan := gcCompactBuild(commits, cutoff, "hash-4", "hash-4")
	if plan.Reason != "" {
		t.Fatalf("plan refused: %s / %s", plan.Reason, plan.Hint)
	}
	if len(plan.Old) != 2 || len(plan.Recent) != 3 {
		t.Fatalf("old = %d recent = %d, want 2 and 3", len(plan.Old), len(plan.Recent))
	}
	if plan.gcCompactCreated() != 4 {
		t.Fatalf("created = %d, want 4 (one base + three replays)", plan.gcCompactCreated())
	}
	replays := plan.gcCompactReplays()
	want := []string{"hash-2", "hash-3", "hash-4"}
	if len(replays) != len(want) {
		t.Fatalf("replays = %v, want %v", replays, want)
	}
	for i := range want {
		if replays[i] != want[i] {
			t.Fatalf("replays = %v, want %v (commit order)", replays, want)
		}
	}

	applied := 0
	gcCompactSeams(t, commits, "hash-4", &applied)
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runGCCompact(c, []string{
		"--cutoff", "2026-11-03T00:00:00Z", "--format", "json", "--apply", "--dry-run=false",
	}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %s", code, errOut.String())
	}
	if applied != 1 {
		t.Fatalf("apply called %d times, want 1", applied)
	}
	got := out.String()
	if !strings.Contains(got, `"mode":"applied"`) || !strings.Contains(got, `"base":1`) ||
		!strings.Contains(got, `"created":4`) || !strings.Contains(got, `"verified":true`) {
		t.Fatalf("out = %q, want applied base 1 created 4 verified true", got)
	}
}

// TestGCCompactDoubleDryRunDoesNotChangeFiles is the third measurable check:
// two dry runs produce the same plan and never call apply.
func TestGCCompactDoubleDryRunDoesNotChangeFiles(t *testing.T) {
	commits := gcCompactFixture()
	applied := 0
	gcCompactSeams(t, commits, "hash-4", &applied)

	outs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		c := &CLI{out: out, errOut: errOut}
		if code := runGCCompact(c, []string{"--cutoff", "2026-11-03T00:00:00Z", "--format", "json"}); code != 0 {
			t.Fatalf("dry run %d code = %d, stderr = %s", i, code, errOut.String())
		}
		outs = append(outs, out.String())
	}
	if applied != 0 {
		t.Fatalf("dry run called apply %d times", applied)
	}
	if outs[0] != outs[1] {
		t.Fatalf("dry runs differ: %q vs %q", outs[0], outs[1])
	}
	if !strings.Contains(outs[0], `"mode":"dry-run"`) || !strings.Contains(outs[0], `"created":0`) {
		t.Fatalf("out = %q, want dry-run with zero created", outs[0])
	}
}

func TestGCCompactParseLogKeepsCommitOrder(t *testing.T) {
	commits, err := gcCompactParseLog("a\t1761936000\nb\t1762022400\n")
	if err != nil {
		t.Fatalf("gcCompactParseLog: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %+v, want 2", commits)
	}
	if commits[0].Order != 0 || commits[1].Order != 1 || commits[0].Hash != "a" || commits[1].Hash != "b" {
		t.Fatalf("commits = %+v, want a/0 then b/1", commits)
	}
	if !commits[0].Date.Before(commits[1].Date) {
		t.Fatalf("dates = %v, %v, want a before b", commits[0].Date, commits[1].Date)
	}
	if _, err := gcCompactParseLog("malformed\n"); err == nil {
		t.Fatal("malformed log line must fail the plan")
	}
}

func TestGCCompactVerifyIsByteExact(t *testing.T) {
	if !gcCompactVerify("hash-4", "hash-4") {
		t.Fatal("same tree must verify")
	}
	if gcCompactVerify("hash-4", "hash-4\n") {
		t.Fatal("tree with extra byte must not verify")
	}
	if gcCompactVerify("hash-4", "other") {
		t.Fatal("changed tree must not verify")
	}
}

// TestGCCompactRejectsChangedTree checks that a replay which changed data
// fails the command even though apply itself reported success.
func TestGCCompactRejectsChangedTree(t *testing.T) {
	commits := gcCompactFixture()
	oldRead, oldApply := gcCompactRead, gcCompactApply
	t.Cleanup(func() { gcCompactRead, gcCompactApply = oldRead, oldApply })
	gcCompactRead = func(string) ([]gcCompactCommit, string, string, error) {
		return commits, "hash-4", "hash-4", nil
	}
	gcCompactApply = func(gcCompactPlan) (string, error) { return "changed", nil }

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := runGCCompact(c, []string{"--cutoff", "2026-11-03T00:00:00Z", "--apply", "--dry-run=false"}); code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "не совпало") {
		t.Fatalf("stderr = %q, want tree mismatch", errOut.String())
	}
}

// TestGCCompactSubcommandReachesPlanner checks that `beacon gc compact` is
// dispatched through the gc runner without a new top-level command.
func TestGCCompactSubcommandReachesPlanner(t *testing.T) {
	commits := gcCompactFixture()
	applied := 0
	gcCompactSeams(t, commits, "hash-4", &applied)

	oldRunner := gcRunner
	t.Cleanup(func() { gcRunner = oldRunner })
	gcRunner = runGCWithServe

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut}
	if code := gcRunner(c, []string{"compact", "--cutoff", "2026-11-03T00:00:00Z", "--format", "json"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"mode":"dry-run"`) {
		t.Fatalf("out = %q, want dry-run plan", out.String())
	}
}
