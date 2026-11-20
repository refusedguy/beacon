package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/ladder"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// TestLadderStamperStopsTimers proves the read and claim paths of the MCP tools
// cancel every ladder timer through the same store handle.
func TestLadderStamperStopsTimers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	lid := ladder.New(db)
	now := time.Now()
	stamper := ladderStamper{ObligationStamper: mcp.NewSQLObligationStamper(db), ladder: lid}

	for i, read := range []bool{true, false} {
		id := fmt.Sprintf("w6b-obl-%d", i)
		o := domain.Obligation{
			ID: id, WatchID: "w6b-watch-" + id, Revision: 1,
			SubscriberID: "sub", VerdictPointer: domain.VerdictReady, StagedAt: now,
		}
		if err := db.SaveObligation(o); err != nil {
			t.Fatalf("SaveObligation: %v", err)
		}
		if err := lid.Stage(ctx, o, domain.VerdictReady, now); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if read {
			if _, applied, err := stamper.StampRead(ctx, id, now.Add(time.Minute)); err != nil || !applied {
				t.Fatalf("StampRead = %v, %v; want applied", applied, err)
			}
		} else {
			if _, applied, err := stamper.StampClaimed(ctx, id, now.Add(time.Minute)); err != nil || !applied {
				t.Fatalf("StampClaimed = %v, %v; want applied", applied, err)
			}
		}
		m, err := lid.Position(ctx, id)
		if err != nil {
			t.Fatalf("Position: %v", err)
		}
		if m.Status != ladder.StatusStopped || !m.NextAt.IsZero() {
			t.Fatalf("marker = %+v, want stopped with no timers", m)
		}
	}
}

// TestLadderCountsProjectsStats proves beacon_stats.ladder reads the pending
// and escalated values from the store, not from process memory.
func TestLadderCountsProjectsStats(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	lid := ladder.New(db)
	now := time.Now()
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("w6b-count-%d", i)
		o := domain.Obligation{
			ID: id, WatchID: "w6b-watch", Revision: 1,
			SubscriberID: "sub", VerdictPointer: domain.VerdictReady, StagedAt: now,
		}
		if err := db.SaveObligation(o); err != nil {
			t.Fatalf("SaveObligation: %v", err)
		}
		if err := lid.Stage(ctx, o, domain.VerdictReady, now); err != nil {
			t.Fatalf("Stage: %v", err)
		}
	}

	pending, escalated := ladderCounts(db)
	if pending != 2 || escalated != 0 {
		t.Fatalf("ladderCounts = %d/%d, want 2/0", pending, escalated)
	}

	stamper := ladderStamper{ObligationStamper: mcp.NewSQLObligationStamper(db), ladder: lid}
	if _, _, err := stamper.StampRead(ctx, "w6b-count-0", now.Add(time.Minute)); err != nil {
		t.Fatalf("StampRead: %v", err)
	}
	pending, escalated = ladderCounts(db)
	if pending != 1 || escalated != 0 {
		t.Fatalf("ladderCounts after read = %d/%d, want 1/0", pending, escalated)
	}
}
