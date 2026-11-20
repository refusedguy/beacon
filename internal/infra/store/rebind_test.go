package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func openRebindTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func rebindFixtureWatch(id, orchestratorID string, status domain.Status, withSubscriber bool) domain.Watch {
	w := domain.Watch{
		ID:             id,
		Repo:           "refusedguy/beacon",
		PR:             7,
		HeadSHA:        "sha-" + id,
		Status:         status,
		OrchestratorID: orchestratorID,
		Subscribers: []domain.Subscriber{
			{ID: "ses_executor", Role: domain.RoleExecutor},
		},
	}
	if withSubscriber {
		w.Subscribers = append(w.Subscribers, domain.Subscriber{
			ID:     orchestratorID,
			Role:   domain.RoleOrchestrator,
			Target: orchestratorID,
		})
	}
	return w
}

func TestRebindOrchestratorReplacesMatchingWatches(t *testing.T) {
	d := openRebindTestDB(t)
	// w1 matches by both the field and the routing subscriber; w2 only by the
	// field; w3 belongs to another orchestrator; w4 is closed and inactive.
	fixtures := []domain.Watch{
		rebindFixtureWatch("w1", "ses_old", domain.StatusWatching, true),
		rebindFixtureWatch("w2", "ses_old", domain.StatusNotifying, false),
		rebindFixtureWatch("w3", "ses_other", domain.StatusWatching, true),
		rebindFixtureWatch("w4", "ses_old", domain.StatusClosed, true),
	}
	for _, w := range fixtures {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}

	targets, err := d.RebindOrchestrator("ses_old", "ses_new", false)
	if err != nil {
		t.Fatalf("RebindOrchestrator: %v", err)
	}
	if len(targets) != 2 || targets[0].WatchID != "w1" || targets[1].WatchID != "w2" {
		t.Fatalf("targets = %+v, want w1 and w2", targets)
	}
	if targets[0].PR != 7 || !strings.Contains(targets[0].Reason, rebindReasonSubscriber) {
		t.Errorf("w1 target = %+v, want PR 7 and subscriber reason", targets[0])
	}
	if !strings.Contains(targets[1].Reason, rebindReasonField) {
		t.Errorf("w2 target = %+v, want field reason", targets[1])
	}

	w1, err := d.Watch("w1")
	if err != nil {
		t.Fatalf("Watch(w1): %v", err)
	}
	if w1.OrchestratorID != "ses_new" {
		t.Errorf("w1.OrchestratorID = %q, want ses_new", w1.OrchestratorID)
	}
	var routed bool
	for _, sub := range w1.Subscribers {
		if sub.Role != domain.RoleOrchestrator {
			continue
		}
		routed = true
		if sub.ID != "ses_new" || sub.Target != "ses_new" {
			t.Errorf("orchestrator subscriber = %+v, want id and target ses_new", sub)
		}
	}
	if !routed {
		t.Fatal("w1 lost its orchestrator subscriber")
	}

	w2, err := d.Watch("w2")
	if err != nil {
		t.Fatalf("Watch(w2): %v", err)
	}
	if w2.OrchestratorID != "ses_new" {
		t.Errorf("w2.OrchestratorID = %q, want ses_new", w2.OrchestratorID)
	}

	for _, id := range []string{"w3", "w4"} {
		w, err := d.Watch(id)
		if err != nil {
			t.Fatalf("Watch(%s): %v", id, err)
		}
		if w.OrchestratorID == "ses_new" {
			t.Errorf("unrelated watch %s was rebound", id)
		}
		for _, sub := range w.Subscribers {
			if sub.ID == "ses_new" {
				t.Errorf("unrelated watch %s got the new subscriber", id)
			}
		}
	}
}

func TestRebindOrchestratorDryRunWritesNothing(t *testing.T) {
	d := openRebindTestDB(t)
	original := rebindFixtureWatch("dry", "ses_old", domain.StatusWatching, true)
	if err := d.SaveWatch(original); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	targets, err := d.RebindOrchestrator("ses_old", "ses_new", true)
	if err != nil {
		t.Fatalf("RebindOrchestrator dry run: %v", err)
	}
	if len(targets) != 1 || targets[0].WatchID != "dry" {
		t.Fatalf("dry run targets = %+v, want one target", targets)
	}
	w, err := d.Watch("dry")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if w.OrchestratorID != "ses_old" {
		t.Errorf("dry run changed the field to %q", w.OrchestratorID)
	}
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator && sub.ID != "ses_old" {
			t.Errorf("dry run changed the subscriber to %+v", sub)
		}
	}
}

func TestRebindOrchestratorIncludesBlockedWatches(t *testing.T) {
	d := openRebindTestDB(t)
	blocked := rebindFixtureWatch("blocked", "ses_old", domain.StatusBlocked, true)
	if err := d.SaveWatch(blocked); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	targets, err := d.RebindOrchestrator("ses_old", "ses_new", false)
	if err != nil {
		t.Fatalf("RebindOrchestrator: %v", err)
	}
	if len(targets) != 1 || targets[0].WatchID != "blocked" {
		t.Fatalf("targets = %+v, want the blocked watch", targets)
	}
	w, err := d.Watch("blocked")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if w.OrchestratorID != "ses_new" {
		t.Errorf("OrchestratorID = %q, want ses_new", w.OrchestratorID)
	}
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator && sub.ID != "ses_new" {
			t.Errorf("orchestrator subscriber = %+v, want ses_new", sub)
		}
	}
}

func TestBindOrchestratorAndRebind(t *testing.T) {
	d := openRebindTestDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := d.BindOrchestrator("project", "ses_old", "opencode", now); err != nil {
		t.Fatalf("BindOrchestrator: %v", err)
	}
	w := rebindFixtureWatch("w1", "ses_old", domain.StatusBlocked, true)
	if err := d.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	targets, err := d.BindOrchestratorAndRebind("project", "ses_new")
	if err != nil {
		t.Fatalf("BindOrchestratorAndRebind: %v", err)
	}
	if len(targets) != 1 || targets[0].WatchID != "w1" {
		t.Fatalf("targets = %+v, want w1", targets)
	}
	binding, bound, err := d.BoundOrchestrator("project")
	if err != nil || !bound {
		t.Fatalf("BoundOrchestrator: %v, %v", bound, err)
	}
	if binding.SessionID != "ses_new" || binding.Adapter != "opencode" {
		t.Errorf("binding = %+v, want ses_new/opencode", binding)
	}
	stored, err := d.Watch("w1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if stored.OrchestratorID != "ses_new" {
		t.Errorf("OrchestratorID = %q, want ses_new", stored.OrchestratorID)
	}

	if _, err := d.BindOrchestratorAndRebind("project", "ses_new"); err != nil {
		t.Errorf("same-session rebind: %v", err)
	}
	if _, err := d.BindOrchestratorAndRebind("unknown", "ses_new"); err == nil {
		t.Error("unbound project rebind accepted")
	}
}

func TestRebindOrchestratorRejectsInvalidArgs(t *testing.T) {
	d := openRebindTestDB(t)
	for _, tc := range []struct{ from, to string }{
		{"", "ses_new"},
		{"ses_old", ""},
		{"ses_same", "ses_same"},
	} {
		if _, err := d.RebindOrchestrator(tc.from, tc.to, false); err == nil {
			t.Errorf("RebindOrchestrator(%q, %q) accepted invalid input", tc.from, tc.to)
		}
	}
}
