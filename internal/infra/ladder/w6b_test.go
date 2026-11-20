package ladder

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// TestProductionAdaptersBindLadderToSingleStore proves the adapters in this
// package read the same journal the transitions write. This is the seam the
// composition root uses to run Ladder and Escalator over one *store.DB.
func TestProductionAdaptersBindLadderToSingleStore(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := New(d, WithJitter(func(int) time.Duration { return 0 }))
	o := domain.Obligation{
		ID: "w6b-obl", WatchID: "w6b-watch", Revision: 1,
		SubscriberID: "orch", VerdictPointer: domain.VerdictReady, StagedAt: base,
	}
	if err := d.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	if err := l.Stage(ctx, o, domain.VerdictReady, base); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	w := domain.Watch{
		ID: o.WatchID, Status: domain.StatusNotifying, Revision: 1,
		Verdict: domain.VerdictReady,
		Subscribers: []domain.Subscriber{{
			ID: "orch", Role: domain.RoleOrchestrator, Target: "orchestrator",
		}},
	}
	if err := d.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	pending, err := l.PendingCount(ctx)
	if err != nil || pending != 1 {
		t.Fatalf("PendingCount = %d, %v; want 1, nil", pending, err)
	}

	episodes, err := NewStagedEpisodeStore(d, func() string { return "configured" }).StagedEpisodes()
	if err != nil {
		t.Fatalf("StagedEpisodes: %v", err)
	}
	if len(episodes) != 1 || episodes[0].Obligation.ID != o.ID {
		t.Fatalf("episodes = %+v, want the staged obligation", episodes)
	}
	if episodes[0].ConfiguredTarget != "configured" {
		t.Fatalf("configured target = %q, want configured", episodes[0].ConfiguredTarget)
	}

	store := NewEscalationStore(d)
	if n, err := store.EscalatedCount(ctx); err != nil || n != 0 {
		t.Fatalf("escalated before = %d, %v; want 0, nil", n, err)
	}
	esc := NewEscalator(
		NewStagedEpisodeStore(d, nil), store,
		&recordingSender{}, fixedClock{now: base.Add(Rung2After)},
		filepath.Join(t.TempDir(), "ESCALATIONS.md"),
	)
	if got, err := esc.Run(ctx); err != nil || got != 1 {
		t.Fatalf("escalator run = %d, %v; want 1, nil", got, err)
	}
	if n, err := store.EscalatedCount(ctx); err != nil || n != 1 {
		t.Fatalf("escalated after = %d, %v; want 1, nil", n, err)
	}
}

// TestRecentActivityDefersEscalation proves the third proof checked at 15
// minutes stops both rungs when an orchestrator is already engaged.
func TestRecentActivityDefersEscalation(t *testing.T) {
	h := newHarness(t)
	ep := newEpisode(t, h.db, "watch-active", h.staged, nil)
	ep.Watch.Subscribers[0].LastActivity = h.staged.Add(20 * time.Minute)
	h.src.items = []Episode{ep}
	h.at(Rung2After)

	got, err := h.esc.Run(context.Background())
	if err != nil || got != 0 {
		t.Fatalf("active episode run = %d, %v; want 0, nil", got, err)
	}
	if len(h.sender.sends) != 0 {
		t.Fatalf("active episode sent %d pointers, want 0", len(h.sender.sends))
	}
	if lines := h.lines(t); len(lines) != 0 {
		t.Fatalf("active episode wrote ESCALATIONS.md: %v", lines)
	}
}
