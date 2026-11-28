package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestRunExplainReadsOneRunWithEventsAndLastDecision(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	wantRun := core.NewRun("run-a", "T-A", now.Add(-time.Hour))
	wantRun.State = core.RunWaitingCI
	wantRun.UpdatedAt = now.Add(-time.Minute)
	if err := store.Save(ctx, wantRun); err != nil {
		t.Fatalf("Save(want): %v", err)
	}
	other := core.NewRun("run-b", "T-B", now.Add(-time.Hour))
	other.State = core.RunBlocked
	if err := store.Save(ctx, other); err != nil {
		t.Fatalf("Save(other): %v", err)
	}
	for _, d := range []app.Decision{
		{At: now.Add(-2 * time.Minute), RunID: "run-a", Verdict: string(core.VerdictWait), Reason: "first"},
		{At: now.Add(-time.Minute), RunID: "run-a", Verdict: string(core.VerdictWait), Reason: "newest", Fields: map[string]any{"criterion": "approvals"}},
		{At: now, RunID: "run-b", Verdict: string(core.VerdictBlocked), Reason: "other run"},
	} {
		if err := store.RecordDecision(ctx, d); err != nil {
			t.Fatalf("RecordDecision: %v", err)
		}
	}

	data, err := store.RunExplain(ctx, wantRun.ID)
	if err != nil {
		t.Fatalf("RunExplain: %v", err)
	}
	if data == nil || data.Run == nil || data.Run.ID != wantRun.ID {
		t.Fatalf("data = %+v, want run-a", data)
	}
	if len(data.Events) == 0 {
		t.Fatal("events are empty, want the new-run transition")
	}
	for _, event := range data.Events {
		if event.RunID != "run-a" {
			t.Fatalf("event run = %q, want run-a", event.RunID)
		}
	}
	if data.Decision == nil || data.Decision.Reason != "newest" {
		t.Fatalf("decision = %+v, want newest run-a decision", data.Decision)
	}
	if name, _ := data.Decision.Fields["criterion"].(string); name != "approvals" {
		t.Fatalf("criterion = %q, want approvals", name)
	}
}

func TestRunExplainUnknownRunReturnsNil(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	data, err := store.RunExplain(context.Background(), "run-absent")
	if err != nil || data != nil {
		t.Fatalf("data = %+v, err = %v, want nil, nil", data, err)
	}
}
