package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func openACPRunStore(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return d, path
}

func TestACPRunEmptyStoreDoesNotCreateTable(t *testing.T) {
	d, _ := openACPRunStore(t)
	got, found, err := d.GetACPRun("T3.278")
	if err != nil || found {
		t.Fatalf("GetACPRun on empty store: found=%v err=%v", found, err)
	}
	ok, err := tableExists(context.Background(), d.readDB, "acp_runs")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if ok {
		t.Fatal("read created the acp_runs table")
	}
}

func TestACPRunClaimReturnsExistingRun(t *testing.T) {
	d, _ := openACPRunStore(t)
	first := ACPRun{
		TaskID: "T3.278", Runner: "opencode", Title: "ACP exec connector",
		State: ACPRunRequested,
	}
	stored, created, err := d.ClaimACPRun(first)
	if err != nil {
		t.Fatalf("first ClaimACPRun: %v", err)
	}
	if !created {
		t.Fatal("first ClaimACPRun did not create the run")
	}
	if stored.TaskID != first.TaskID || stored.State != ACPRunRequested {
		t.Fatalf("stored run = %+v", stored)
	}
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("stored times are zero: %+v", stored)
	}

	second := first
	second.Runner = "claude"
	second.Title = "a different title"
	second.State = ACPRunStarted
	again, created, err := d.ClaimACPRun(second)
	if err != nil {
		t.Fatalf("second ClaimACPRun: %v", err)
	}
	if created {
		t.Fatal("second ClaimACPRun created another run for the same task")
	}
	if again.Runner != first.Runner || again.Title != first.Title || again.State != ACPRunRequested {
		t.Fatalf("second claim changed the first run: %+v", again)
	}
	if !again.CreatedAt.Equal(stored.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", again.CreatedAt, stored.CreatedAt)
	}

	all, err := d.ListACPRuns()
	if err != nil {
		t.Fatalf("ListACPRuns: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("runs = %d, want 1", len(all))
	}
}

func TestACPRunStateTransitions(t *testing.T) {
	d, _ := openACPRunStore(t)
	if !CanTransitionACPRun(ACPRunRequested, ACPRunStarted) ||
		!CanTransitionACPRun(ACPRunStarted, ACPRunFinished) ||
		!CanTransitionACPRun(ACPRunStarted, ACPRunInterrupted) ||
		!CanTransitionACPRun(ACPRunInterrupted, ACPRunStarted) {
		t.Fatal("state machine rejected a legal transition")
	}
	for _, illegal := range [][2]ACPRunState{
		{ACPRunRequested, ACPRunFinished},
		{ACPRunFinished, ACPRunStarted},
		{ACPRunInterrupted, ACPRunInterrupted},
		{ACPRunStarted, ACPRunStarted},
	} {
		if CanTransitionACPRun(illegal[0], illegal[1]) {
			t.Errorf("state machine accepted %s -> %s", illegal[0], illegal[1])
		}
	}

	if _, _, err := d.ClaimACPRun(ACPRun{TaskID: "T3.278"}); err != nil {
		t.Fatalf("ClaimACPRun: %v", err)
	}

	changed, err := d.TransitionACPRun("T3.278", ACPRunFinished, "ses_1", nil)
	if err != nil || changed {
		t.Fatalf("requested -> finished: changed=%v err=%v", changed, err)
	}

	changed, err = d.TransitionACPRun("T3.278", ACPRunStarted, "ses_1", nil)
	if err != nil || !changed {
		t.Fatalf("requested -> started: changed=%v err=%v", changed, err)
	}
	run, found, err := d.GetACPRun("T3.278")
	if err != nil || !found {
		t.Fatalf("GetACPRun: found=%v err=%v", found, err)
	}
	if run.State != ACPRunStarted || run.SessionID != "ses_1" {
		t.Fatalf("run after start = %+v", run)
	}

	changed, err = d.TransitionACPRun("T3.278", ACPRunInterrupted, "", json.RawMessage(`{"updates":1}`))
	if err != nil || !changed {
		t.Fatalf("started -> interrupted: changed=%v err=%v", changed, err)
	}
	run, _, err = d.GetACPRun("T3.278")
	if err != nil {
		t.Fatalf("GetACPRun: %v", err)
	}
	if run.State != ACPRunInterrupted || run.SessionID != "ses_1" {
		t.Fatalf("interrupt replaced the session: %+v", run)
	}
	if string(run.Result) != `{"updates":1}` {
		t.Fatalf("result = %s, want the interrupted snapshot", run.Result)
	}

	changed, err = d.TransitionACPRun("T3.278", ACPRunStarted, "", nil)
	if err != nil || !changed {
		t.Fatalf("interrupted -> started: changed=%v err=%v", changed, err)
	}
	changed, err = d.TransitionACPRun("T3.278", ACPRunFinished, "", json.RawMessage(`{"updates":2}`))
	if err != nil || !changed {
		t.Fatalf("started -> finished: changed=%v err=%v", changed, err)
	}
	run, _, err = d.GetACPRun("T3.278")
	if err != nil {
		t.Fatalf("GetACPRun: %v", err)
	}
	if run.State != ACPRunFinished || string(run.Result) != `{"updates":2}` {
		t.Fatalf("finished run = %+v", run)
	}

	changed, err = d.TransitionACPRun("T3.278", ACPRunStarted, "", nil)
	if err != nil || changed {
		t.Fatalf("finished -> started: changed=%v err=%v", changed, err)
	}
	run, _, err = d.GetACPRun("T3.278")
	if err != nil || run.State != ACPRunFinished {
		t.Fatalf("illegal transition changed the row: %+v err=%v", run, err)
	}

	changed, err = d.TransitionACPRun("T.MISSING", ACPRunInterrupted, "", nil)
	if err != nil || changed {
		t.Fatalf("transition on missing task: changed=%v err=%v", changed, err)
	}
}

func TestACPRunResultSurvivesReopen(t *testing.T) {
	d, path := openACPRunStore(t)
	if _, _, err := d.ClaimACPRun(ACPRun{TaskID: "T3.278"}); err != nil {
		t.Fatalf("ClaimACPRun: %v", err)
	}
	if _, err := d.TransitionACPRun("T3.278", ACPRunStarted, "ses_1", nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	want := json.RawMessage(`{"taskId":"T3.278","sessionId":"ses_1","updates":[1,2]}`)
	if _, err := d.TransitionACPRun("T3.278", ACPRunFinished, "", want); err != nil {
		t.Fatalf("finish: %v", err)
	}
	before, _, err := d.GetACPRun("T3.278")
	if err != nil {
		t.Fatalf("GetACPRun: %v", err)
	}
	createdAt := before.CreatedAt

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	after, found, err := reopened.GetACPRun("T3.278")
	if err != nil || !found {
		t.Fatalf("GetACPRun after restart: found=%v err=%v", found, err)
	}
	if after.State != ACPRunFinished || after.SessionID != "ses_1" {
		t.Fatalf("run after restart = %+v", after)
	}
	if string(after.Result) != string(want) {
		t.Fatalf("result after restart = %s, want %s", after.Result, want)
	}
	if !after.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at after restart = %v, want %v", after.CreatedAt, createdAt)
	}
	again, created, err := reopened.ClaimACPRun(ACPRun{TaskID: "T3.278", State: ACPRunRequested})
	if err != nil || created {
		t.Fatalf("repeat run after restart: created=%v err=%v", created, err)
	}
	if again.State != ACPRunFinished {
		t.Fatalf("repeat run state = %s, want finished", again.State)
	}
}

func TestFindSessionByTask(t *testing.T) {
	d := openSessionStore(t)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	want := Session{
		ID:        "ses_t278",
		Status:    SessionStatusActive,
		CreatedAt: now,
		LastSeen:  now,
		Metadata:  map[string]string{SessionMetaTaskID: "T3.278", "kind": "executor"},
	}
	if err := d.SaveSession(want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, found, err := d.FindSessionByTask(" T3.278 ")
	if err != nil || !found {
		t.Fatalf("FindSessionByTask: found=%v err=%v", found, err)
	}
	if got.ID != want.ID {
		t.Fatalf("session = %+v, want %+v", got, want)
	}
	_, found, err = d.FindSessionByTask("T3.279")
	if err != nil || found {
		t.Fatalf("missing task: found=%v err=%v", found, err)
	}
}
