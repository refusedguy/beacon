package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	oldSessions, oldDelete := gcSessionsForGC, gcSessionDelete
	t.Cleanup(func() {
		gcServeList, gcServicePID, gcServiceCfg = oldList, oldPID, oldCfg
		gcServeState, gcServeClear, gcServeKill = oldState, oldClear, oldKill
		gcProcessAlive, gcReadState, gcWriteState = oldAlive, oldRead, oldWrite
		gcObligationsForGC = oldObligations
		gcSessionsForGC, gcSessionDelete = oldSessions, oldDelete
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
