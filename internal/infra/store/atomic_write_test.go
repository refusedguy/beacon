package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func verdictCommitFixture(t *testing.T) VerdictCommit {
	t.Helper()
	staged := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	w := domain.Watch{
		ID:       "w1",
		Repo:     "org/repo",
		PR:       7,
		HeadSHA:  "abc123",
		Status:   domain.StatusNotifying,
		Verdict:  domain.VerdictReady,
		Revision: 3,
	}
	o := domain.Obligation{
		ID:             domain.ObligationKey(w.ID, w.Revision, "s1"),
		WatchID:        w.ID,
		Revision:       w.Revision,
		SubscriberID:   "s1",
		VerdictPointer: w.Verdict,
		StagedAt:       staged,
	}
	return VerdictCommit{
		Watch:      w,
		Obligation: o,
		Wake: WakeIntent{
			WatchID:      w.ID,
			ObligationID: o.ID,
			Revision:     w.Revision,
			StagedAt:     staged,
		},
	}
}

func verdictTableCount(t *testing.T, d *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.readDB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

func verdictWakeCount(t *testing.T, d *DB, obligationID string) int {
	t.Helper()
	return verdictTableCount(t, d, `
		SELECT count(*) FROM events
		WHERE json_extract(payload, '$.kind') = ?
		  AND json_extract(payload, '$.obligationID') = ?`,
		wakeIntentKind, obligationID)
}

func TestCommitVerdictRollbackLeavesNoPartialState(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	c := verdictCommitFixture(t)
	tx, err := d.writeConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := CommitVerdictTx(tx, c); err != nil {
		tx.Rollback()
		t.Fatalf("CommitVerdictTx: %v", err)
	}
	// The caller rejects the work after the three writes are staged in tx.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if _, err := d.Watch(c.Watch.ID); err == nil {
		t.Error("verdict visible after rollback")
	}
	if _, err := d.Obligation(c.Obligation.ID); err == nil {
		t.Error("obligation visible after rollback")
	}
	if got := verdictWakeCount(t, d, c.Wake.ObligationID); got != 0 {
		t.Errorf("wake intents after rollback = %d, want 0", got)
	}
	if got := verdictTableCount(t, d, `SELECT count(*) FROM watches`); got != 0 {
		t.Errorf("watches after rollback = %d, want 0", got)
	}
	if got := verdictTableCount(t, d, `SELECT count(*) FROM obligations`); got != 0 {
		t.Errorf("obligations after rollback = %d, want 0", got)
	}
	if got := verdictTableCount(t, d, `SELECT count(*) FROM events`); got != 0 {
		t.Errorf("events after rollback = %d, want 0", got)
	}
}

func TestCommitVerdictRepeatIsNoOp(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	c := verdictCommitFixture(t)
	if err := d.CommitVerdict(ctx, c); err != nil {
		t.Fatalf("first CommitVerdict: %v", err)
	}
	// Same key, different staged time: the id must win over the timestamp.
	c.Wake.StagedAt = c.Wake.StagedAt.Add(time.Second)
	if err := d.CommitVerdict(ctx, c); err != nil {
		t.Fatalf("second CommitVerdict: %v", err)
	}

	if got := verdictTableCount(t, d, `SELECT count(*) FROM watches`); got != 1 {
		t.Errorf("watches = %d, want 1", got)
	}
	if got := verdictTableCount(t, d, `SELECT count(*) FROM obligations`); got != 1 {
		t.Errorf("obligations = %d, want 1 (UNIQUE triple)", got)
	}
	if got := verdictTableCount(t, d, `SELECT count(*) FROM events`); got != 1 {
		t.Errorf("events = %d, want 1 (one wake intent)", got)
	}

	w, err := d.Watch(c.Watch.ID)
	if err != nil || w.Verdict != domain.VerdictReady || w.Revision != 3 {
		t.Fatalf("Watch after repeat: %+v, %v", w, err)
	}
	o, err := d.Obligation(c.Obligation.ID)
	if err != nil || o.VerdictPointer != domain.VerdictReady {
		t.Fatalf("Obligation after repeat: %+v, %v", o, err)
	}
}

func TestCommitVerdictCommitsBeforeAnswer(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	c := verdictCommitFixture(t)
	if got := verdictWakeCount(t, d, c.Wake.ObligationID); got != 0 {
		t.Fatalf("wake intents before commit = %d, want 0", got)
	}
	// On return the writer has committed. A reader on the separate read pool
	// must see all three components without waiting or polling.
	if err := d.CommitVerdict(ctx, c); err != nil {
		t.Fatalf("CommitVerdict: %v", err)
	}
	w, err := d.Watch(c.Watch.ID)
	if err != nil || w.Verdict != c.Watch.Verdict {
		t.Fatalf("Watch at answer time: %+v, %v", w, err)
	}
	o, err := d.Obligation(c.Obligation.ID)
	if err != nil || o.ID != c.Obligation.ID {
		t.Fatalf("Obligation at answer time: %+v, %v", o, err)
	}
	if got := verdictWakeCount(t, d, c.Wake.ObligationID); got != 1 {
		t.Errorf("wake intents at answer time = %d, want 1", got)
	}
}

func TestCommitVerdictRejectsMismatchedTriple(t *testing.T) {
	base := verdictCommitFixture(t)
	cases := []struct {
		name   string
		mutate func(*VerdictCommit)
	}{
		{"empty watch", func(c *VerdictCommit) { c.Watch.ID = "" }},
		{"wrong obligation key", func(c *VerdictCommit) { c.Obligation.ID = "hand-made" }},
		{"revision mismatch", func(c *VerdictCommit) { c.Wake.Revision++ }},
		{"wake points elsewhere", func(c *VerdictCommit) { c.Wake.ObligationID = "other" }},
		{"verdict pointer mismatch", func(c *VerdictCommit) { c.Obligation.VerdictPointer = domain.VerdictRebase }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if err := validateVerdictCommit(c); err == nil {
				t.Fatal("mismatched triple must be rejected")
			}
		})
	}
	if err := CommitVerdictTx(nil, base); err == nil {
		t.Fatal("nil transaction must be rejected")
	}
	if err := CommitVerdict(context.Background(), nil, base); err == nil {
		t.Fatal("nil writer must be rejected")
	}
}
