package store

import (
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func backfillWatch(id string, status domain.Status, subscribers ...domain.Subscriber) domain.Watch {
	return domain.Watch{
		ID:          id,
		Repo:        "refusedguy/beacon",
		PR:          1,
		Status:      status,
		Revision:    3,
		Subscribers: subscribers,
	}
}

type backfillExecutorView struct {
	AgentID string
	Role    string
	Session string
	Adapter string
	Target  string
	Status  string
}

func backfillViews(records []ExecutorRecord) []backfillExecutorView {
	out := make([]backfillExecutorView, 0, len(records))
	for _, e := range records {
		out = append(out, backfillExecutorView{
			AgentID: e.AgentID,
			Role:    e.Role,
			Session: e.SessionID,
			Adapter: e.NotifyAdapter,
			Target:  e.NotifyTarget,
			Status:  e.Status,
		})
	}
	return out
}

func TestBackfillRegistryFillsActiveSubscribers(t *testing.T) {
	d := openTestDB(t)
	watches := []domain.Watch{
		backfillWatch("watch-1", domain.StatusWatching,
			domain.Subscriber{
				ID: "ses-1", Role: domain.RoleExecutor,
				Adapter: "opencode", Target: "T5.26",
			},
			domain.Subscriber{
				ID: "ses-2", Role: domain.RoleExecutor,
				Adapter: "acp", Target: "check",
			},
		),
		backfillWatch("watch-2", domain.StatusNotifying,
			domain.Subscriber{
				ID: "ses-3", Role: domain.RoleOrchestrator,
				Adapter: "opencode", Target: "T5.26",
			},
		),
		backfillWatch("watch-closed", domain.StatusClosed,
			domain.Subscriber{
				ID: "ses-closed", Role: domain.RoleExecutor,
				Adapter: "opencode", Target: "T5.26",
			},
		),
	}
	for _, w := range watches {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}

	res, err := d.BackfillRegistry("project-a", []string{"ses-1", "ses-3"})
	if err != nil {
		t.Fatalf("BackfillRegistry: %v", err)
	}
	if res.WatchesScanned != 2 {
		t.Errorf("WatchesScanned = %d, want 2 active watches", res.WatchesScanned)
	}
	if res.ExecutorsCreated != 3 {
		t.Errorf("ExecutorsCreated = %d, want 3", res.ExecutorsCreated)
	}
	if res.ExecutorsStale != 1 {
		t.Errorf("ExecutorsStale = %d, want 1", res.ExecutorsStale)
	}

	want := []backfillExecutorView{
		{AgentID: "ses-1", Role: "executor", Session: "ses-1", Adapter: "opencode", Target: "T5.26", Status: RegistryStatusActive},
		{AgentID: "ses-2", Role: "executor", Session: "ses-2", Adapter: "acp", Target: "check", Status: RegistryStatusStale},
		{AgentID: "ses-3", Role: "orchestrator", Session: "ses-3", Adapter: "opencode", Target: "T5.26", Status: RegistryStatusActive},
	}
	if got := backfillViews(res.Executors); !reflect.DeepEqual(got, want) {
		t.Errorf("registry after backfill = %+v, want %+v", got, want)
	}
}

func TestBackfillRegistryDoesNotRewriteExistingRows(t *testing.T) {
	d := openTestDB(t)
	w := backfillWatch("watch-1", domain.StatusWatching,
		domain.Subscriber{
			ID: "ses-1", Role: domain.RoleExecutor,
			Adapter: "opencode", Target: "T5.26",
		},
	)
	if err := d.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	first, err := d.BackfillRegistry("project-a", []string{"ses-1"})
	if err != nil {
		t.Fatalf("first BackfillRegistry: %v", err)
	}
	if first.ExecutorsCreated != 1 || first.ExecutorsStale != 0 {
		t.Fatalf("first pass = %+v, want 1 created and 0 stale", first)
	}

	second, err := d.BackfillRegistry("project-a", nil)
	if err != nil {
		t.Fatalf("second BackfillRegistry: %v", err)
	}
	if second.ExecutorsCreated != 0 || second.ExecutorsKept != 1 {
		t.Errorf("second pass counts = %+v, want 0 created and 1 kept", second)
	}
	want := []backfillExecutorView{
		{AgentID: "ses-1", Role: "executor", Session: "ses-1", Adapter: "opencode", Target: "T5.26", Status: RegistryStatusActive},
	}
	if got := backfillViews(second.Executors); !reflect.DeepEqual(got, want) {
		t.Errorf("repeat backfill changed the row: %+v, want %+v", got, want)
	}
}

func TestBackfillRegistrySkipsAddresslessSubscriber(t *testing.T) {
	d := openTestDB(t)
	w := backfillWatch("watch-1", domain.StatusWatching,
		domain.Subscriber{ID: "", Role: domain.RoleExecutor, Adapter: "opencode"},
		domain.Subscriber{ID: "ses-1", Role: domain.RoleExecutor, Adapter: "opencode"},
	)
	if err := d.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	res, err := d.BackfillRegistry("project-a", []string{"ses-1"})
	if err != nil {
		t.Fatalf("BackfillRegistry: %v", err)
	}
	if res.ExecutorsCreated != 1 {
		t.Fatalf("ExecutorsCreated = %d, want 1", res.ExecutorsCreated)
	}
	if len(res.Executors) != 1 || res.Executors[0].AgentID != "ses-1" {
		t.Fatalf("registry = %+v, want only ses-1", res.Executors)
	}
}

func TestBackfillRegistryRejectsEmptyProject(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.BackfillRegistry("  ", []string{"ses-1"}); err == nil {
		t.Fatal("BackfillRegistry with empty project must fail")
	}
}
