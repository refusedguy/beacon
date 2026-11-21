package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func openControlPlaneTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestControlPlaneMigration(t *testing.T) {
	d := openControlPlaneTestDB(t)
	if d.Version() != latestVersion(t) {
		t.Errorf("Version() = %d, want %d", d.Version(), latestVersion(t))
	}
	for _, table := range []string{
		"blocker_incidents", "project_orchestrators", "runtime_stats",
	} {
		requireTable(t, d.readDB, table)
	}
	requireUserVersion(t, context.Background(), d.readDB, latestVersion(t))
}

func TestLiveWatchesIncludesBlocked(t *testing.T) {
	d := openControlPlaneTestDB(t)
	watches := []domain.Watch{
		{ID: "live-watch", Status: domain.StatusWatching},
		{ID: "notify-watch", Status: domain.StatusNotifying},
		{ID: "blocked-watch", Status: domain.StatusBlocked},
		{ID: "closed-watch", Status: domain.StatusClosed},
	}
	for _, w := range watches {
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", w.ID, err)
		}
	}
	active, err := d.ActiveWatches()
	if err != nil {
		t.Fatalf("ActiveWatches: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("ActiveWatches = %d, want 2 (no blocked watch)", len(active))
	}
	live, err := d.LiveWatches()
	if err != nil {
		t.Fatalf("LiveWatches: %v", err)
	}
	if len(live) != 3 {
		t.Fatalf("LiveWatches = %d, want 3 (blocked included, closed excluded)", len(live))
	}
	// queryWatches orders by id, not by insertion order. Compare the set of
	// live ids and their statuses so the assertion is order-insensitive.
	got := make(map[string]domain.Status, len(live))
	for _, w := range live {
		got[w.ID] = w.Status
	}
	want := map[string]domain.Status{
		"live-watch":    domain.StatusWatching,
		"notify-watch":  domain.StatusNotifying,
		"blocked-watch": domain.StatusBlocked,
	}
	for id, status := range want {
		if gotStatus, ok := got[id]; !ok || gotStatus != status {
			t.Errorf("LiveWatches[%s] = %q (present=%v), want %q", id, gotStatus, ok, status)
		}
	}
}

func TestGetOrCreateOpenBlockerDedup(t *testing.T) {
	d := openControlPlaneTestDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	first, created, err := d.GetOrCreateOpenBlocker(
		"org/repo", "main", []string{"ci/test", "gates"}, "watch-1", now)
	if err != nil {
		t.Fatalf("GetOrCreateOpenBlocker: %v", err)
	}
	if !created {
		t.Error("first call created=false, want true")
	}
	if first.Status != blockerStatusOpen || first.Signature == "" {
		t.Errorf("incident = %+v, want open with a signature", first)
	}

	// Different order of the same checks must find the same incident.
	second, created, err := d.GetOrCreateOpenBlocker(
		"org/repo", "main", []string{"gates", "ci/test"}, "watch-2", now)
	if err != nil {
		t.Fatalf("second GetOrCreateOpenBlocker: %v", err)
	}
	if created {
		t.Error("same failed checks created a second incident")
	}
	if second.ID != first.ID {
		t.Errorf("second ID = %s, want %s", second.ID, first.ID)
	}
	if len(second.BlockedWatchIDs) != 2 {
		t.Fatalf("BlockedWatchIDs = %v, want watch-1 and watch-2", second.BlockedWatchIDs)
	}

	third, created, err := d.GetOrCreateOpenBlocker(
		"org/repo", "main", []string{"gates", "ci/test"}, "watch-2", now)
	if err != nil {
		t.Fatalf("third GetOrCreateOpenBlocker: %v", err)
	}
	if created || len(third.BlockedWatchIDs) != 2 {
		t.Errorf("repeated watch = %+v (created=%v), want two ids and created=false", third, created)
	}
	if first.MaxFixAttempts != defaultMaxFixAttempts {
		t.Errorf("MaxFixAttempts = %d, want %d", first.MaxFixAttempts, defaultMaxFixAttempts)
	}

	active, err := d.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(active) != 1 || active[0].ID != first.ID {
		t.Errorf("ActiveBlockers = %+v, want one incident", active)
	}
}

func TestAssignFixerIncrementsAttempts(t *testing.T) {
	d := openControlPlaneTestDB(t)
	now := time.Now().UTC()
	b, _, err := d.GetOrCreateOpenBlocker("org/repo", "main", []string{"ci/test"}, "w1", now)
	if err != nil {
		t.Fatalf("GetOrCreateOpenBlocker: %v", err)
	}
	if b.FixAttempts != 0 {
		t.Fatalf("initial FixAttempts = %d, want 0", b.FixAttempts)
	}

	assigned, err := d.AssignFixer(b.ID, "fixer-1")
	if err != nil {
		t.Fatalf("AssignFixer: %v", err)
	}
	if assigned.FixerWatchID != "fixer-1" || assigned.FixAttempts != 1 {
		t.Errorf("assigned = %+v, want fixer-1 with one attempt", assigned)
	}
	assigned, err = d.AssignFixer(b.ID, "fixer-2")
	if err != nil {
		t.Fatalf("second AssignFixer: %v", err)
	}
	if assigned.FixerWatchID != "fixer-2" || assigned.FixAttempts != 2 {
		t.Errorf("assigned = %+v, want fixer-2 with two attempts", assigned)
	}
	active, err := d.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if active[0].FixAttempts != 2 {
		t.Errorf("persisted FixAttempts = %d, want 2", active[0].FixAttempts)
	}
}

func TestResolveBlockerUnblocksWatches(t *testing.T) {
	d := openControlPlaneTestDB(t)
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	b, _, err := d.GetOrCreateOpenBlocker(
		"org/repo", "main", []string{"ci/test"}, "w1", now)
	if err != nil {
		t.Fatalf("GetOrCreateOpenBlocker: %v", err)
	}
	_, _, err = d.GetOrCreateOpenBlocker(
		"org/repo", "main", []string{"ci/test"}, "w2", now)
	if err != nil {
		t.Fatalf("second GetOrCreateOpenBlocker: %v", err)
	}
	for _, id := range []string{"w1", "w2"} {
		w := domain.Watch{
			ID:        id,
			Repo:      "org/repo",
			Status:    domain.StatusBlocked,
			Verdict:   domain.VerdictBlocked,
			BlockerID: b.ID,
			Revision:  4,
			Blocked: &domain.BlockedInfo{
				Reason:       domain.BlockReasonBaseChecksFailed,
				FailedChecks: []string{"ci/test"},
				Since:        now,
			},
		}
		if err := d.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch(%s): %v", id, err)
		}
	}

	resolvedAt := now.Add(time.Hour)
	unblocked, err := d.ResolveBlocker(b.ID, resolvedAt)
	if err != nil {
		t.Fatalf("ResolveBlocker: %v", err)
	}
	if len(unblocked) != 2 {
		t.Fatalf("unblocked = %d, want 2", len(unblocked))
	}
	for _, w := range unblocked {
		if w.Status != domain.StatusWatching || w.Verdict != domain.VerdictWatching {
			t.Errorf("watch %s status/verdict = %q/%q, want watching/watching",
				w.ID, w.Status, w.Verdict)
		}
		if w.Blocked != nil || w.BlockerID != "" || w.Revision != 5 {
			t.Errorf("watch %s = %+v, want Blocked=nil, BlockerID empty, revision 5", w.ID, w)
		}
		stored, err := d.Watch(w.ID)
		if err != nil {
			t.Fatalf("Watch(%s): %v", w.ID, err)
		}
		if stored.Status != domain.StatusWatching || stored.Revision != 5 || stored.Blocked != nil {
			t.Errorf("stored watch %s = %+v", w.ID, stored)
		}
	}
	active, err := d.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("ActiveBlockers = %+v, want empty after resolve", active)
	}
	var savedAt int64
	if err := d.readDB.QueryRow(
		`SELECT resolved_at FROM blocker_incidents WHERE id = ?`, b.ID,
	).Scan(&savedAt); err != nil {
		t.Fatalf("read resolved_at: %v", err)
	}
	if savedAt != resolvedAt.UnixNano() {
		t.Errorf("resolved_at = %d, want %d", savedAt, resolvedAt.UnixNano())
	}

	again, err := d.ResolveBlocker(b.ID, resolvedAt)
	if err != nil || len(again) != 0 {
		t.Errorf("repeated ResolveBlocker = %+v, %v, want no watches", again, err)
	}
}

func TestResolveBlockersByBaseGreen(t *testing.T) {
	d := openControlPlaneTestDB(t)
	now := time.Now().UTC()
	green, _, err := d.GetOrCreateOpenBlocker("org/repo", "main", []string{"ci/test"}, "w-green", now)
	if err != nil {
		t.Fatalf("green blocker: %v", err)
	}
	red, _, err := d.GetOrCreateOpenBlocker("org/repo", "main", []string{"gates"}, "w-red", now)
	if err != nil {
		t.Fatalf("red blocker: %v", err)
	}
	for _, id := range []string{"w-green", "w-red"} {
		if err := d.SaveWatch(domain.Watch{
			ID: id, Status: domain.StatusBlocked, Verdict: domain.VerdictBlocked,
			BlockerID: green.ID,
			Blocked:   &domain.BlockedInfo{Reason: domain.BlockReasonBaseChecksFailed, FailedChecks: []string{"ci/test"}},
		}); err != nil {
			t.Fatalf("SaveWatch(%s): %v", id, err)
		}
	}

	resolved, err := d.ResolveBlockersByBaseGreen(
		"org/repo", "main", map[string]bool{"gates": true}, now)
	if err != nil {
		t.Fatalf("ResolveBlockersByBaseGreen: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != green.ID {
		t.Fatalf("resolved = %+v, want only the green incident %s", resolved, green.ID)
	}
	active, err := d.ActiveBlockers()
	if err != nil {
		t.Fatalf("ActiveBlockers: %v", err)
	}
	if len(active) != 1 || active[0].ID != red.ID {
		t.Fatalf("ActiveBlockers = %+v, want the red incident %s", active, red.ID)
	}
	greenWatch, err := d.Watch("w-green")
	if err != nil {
		t.Fatalf("Watch(w-green): %v", err)
	}
	if greenWatch.Status != domain.StatusWatching {
		t.Errorf("w-green status = %q, want watching", greenWatch.Status)
	}
	redWatch, err := d.Watch("w-red")
	if err != nil {
		t.Fatalf("Watch(w-red): %v", err)
	}
	if redWatch.Status != domain.StatusBlocked {
		t.Errorf("w-red status = %q, want blocked", redWatch.Status)
	}
}

func TestOrchestratorBindingAndRuntimeStats(t *testing.T) {
	d := openControlPlaneTestDB(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	if _, bound, err := d.BoundOrchestrator("project"); err != nil || bound {
		t.Fatalf("BoundOrchestrator before bind = %v, %v, want unbound", bound, err)
	}

	if err := d.BindOrchestrator("project", "ses-1", "opencode", now); err != nil {
		t.Fatalf("BindOrchestrator: %v", err)
	}
	binding, bound, err := d.BoundOrchestrator("project")
	if err != nil || !bound {
		t.Fatalf("BoundOrchestrator: %v, %v", bound, err)
	}
	if binding.ProjectKey != "project" || binding.SessionID != "ses-1" ||
		binding.Adapter != "opencode" || !binding.BoundAt.Equal(now) {
		t.Errorf("binding = %+v", binding)
	}
	if err := d.BindOrchestrator("project", "ses-2", "opencode", now.Add(time.Minute)); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	binding, _, err = d.BoundOrchestrator("project")
	if err != nil || binding.SessionID != "ses-2" {
		t.Errorf("rebound binding = %+v, %v", binding, err)
	}
	if err := d.BindOrchestrator("", "ses-1", "opencode", now); err == nil {
		t.Error("empty project key accepted")
	}

	if _, ok, err := d.LoadRuntimeStats(); err != nil || ok {
		t.Fatalf("LoadRuntimeStats before save = %v, %v", ok, err)
	}
	payload := []byte(`{"watches":2}`)
	if err := d.SaveRuntimeStats(payload, now); err != nil {
		t.Fatalf("SaveRuntimeStats: %v", err)
	}
	got, ok, err := d.LoadRuntimeStats()
	if err != nil || !ok || !bytes.Equal(got, payload) {
		t.Errorf("LoadRuntimeStats = %s, %v, %v", got, ok, err)
	}
}

func TestMarkObligationDeliveredPersists(t *testing.T) {
	d := openControlPlaneTestDB(t)
	o := domain.Obligation{
		ID: "w1:1:s1", WatchID: "w1", Revision: 1, SubscriberID: "s1",
		VerdictPointer: domain.VerdictReady,
	}
	if err := d.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	at := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	if err := d.MarkObligationDelivered(o.ID, at, 1, "first error"); err != nil {
		t.Fatalf("MarkObligationDelivered: %v", err)
	}
	stored, err := d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !stored.DeliveredAt.Equal(at) || len(stored.Attempts) != 1 {
		t.Errorf("obligation = %+v, want deliveredAt and one attempt", stored)
	}
	pending, err := d.PendingObligations()
	if err != nil || len(pending) != 0 {
		t.Errorf("PendingObligations = %+v, %v, want empty", pending, err)
	}
	var delivered int
	if err := d.readDB.QueryRow(
		`SELECT delivered_at FROM obligations WHERE id = ?`, o.ID,
	).Scan(&delivered); err != nil {
		t.Fatalf("read delivered_at: %v", err)
	}
	if delivered != 1 {
		t.Errorf("delivered_at = %d, want 1", delivered)
	}
	if err := d.MarkObligationDelivered(o.ID, at.Add(time.Minute), 1, "retry error"); err != nil {
		t.Fatalf("repeated MarkObligationDelivered: %v", err)
	}
	stored, err = d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation after repeat: %v", err)
	}
	if !stored.DeliveredAt.Equal(at) || len(stored.Attempts) != 1 {
		t.Errorf("repeat changed obligation = %+v", stored)
	}
}

func TestResetControlPlaneState(t *testing.T) {
	d := openControlPlaneTestDB(t)
	now := time.Now().UTC()
	if err := d.SaveWatch(domain.Watch{ID: "w1", Status: domain.StatusWatching}); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := d.SaveObligation(domain.Obligation{ID: "o1", WatchID: "w1"}); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	if _, _, err := d.GetOrCreateOpenBlocker("org/repo", "main", []string{"ci"}, "w1", now); err != nil {
		t.Fatalf("GetOrCreateOpenBlocker: %v", err)
	}
	if err := d.BindOrchestrator("project", "ses-1", "opencode", now); err != nil {
		t.Fatalf("BindOrchestrator: %v", err)
	}
	if err := d.SaveRuntimeStats([]byte(`{}`), now); err != nil {
		t.Fatalf("SaveRuntimeStats: %v", err)
	}
	if err := d.ResetControlPlaneState(context.Background()); err != nil {
		t.Fatalf("ResetControlPlaneState: %v", err)
	}
	for _, table := range []string{
		"watches", "obligations", "blocker_incidents", "events", "runtime_stats",
	} {
		var n int
		if err := d.readDB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after reset", table, n)
		}
	}
	active, err := d.ActiveBlockers()
	if err != nil || len(active) != 0 {
		t.Errorf("ActiveBlockers after reset = %+v, %v", active, err)
	}
	if _, ok, err := d.LoadRuntimeStats(); err != nil || ok {
		t.Errorf("LoadRuntimeStats after reset = %v, %v", ok, err)
	}
	// The binding is project identity, not run state, so it survives a reset.
	if _, bound, err := d.BoundOrchestrator("project"); err != nil || !bound {
		t.Errorf("binding after reset = %v, %v", bound, err)
	}
	var meta int
	if err := d.readDB.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`,
	).Scan(&meta); err != nil || meta != 1 {
		t.Errorf("meta table after reset = %d, %v", meta, err)
	}
	// Reset must remain safe when the optional ledger table is absent.
	if err := d.ResetControlPlaneState(context.Background()); err != nil {
		t.Errorf("repeated reset: %v", err)
	}
}
