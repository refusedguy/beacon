package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func openSnapshotsDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestSaveSnapshotRoundTrip(t *testing.T) {
	d := openSnapshotsDB(t)
	at := time.Date(2026, 11, 20, 10, 30, 0, 123456789, time.UTC)
	payload := []byte(`{"watches":[],"counters":{"poll_cycles":7}}`)
	seq, err := d.SaveSnapshot(Snapshot{SchemaVersion: 3, Payload: payload, CreatedAt: at})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if seq <= 0 {
		t.Fatalf("SaveSnapshot seq = %d, want positive", seq)
	}
	requireTable(t, d.readDB, "snapshots")

	got, found, err := d.LatestSnapshot()
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if !found {
		t.Fatal("LatestSnapshot found = false, want true")
	}
	if got.Seq != seq {
		t.Errorf("Seq = %d, want %d", got.Seq, seq)
	}
	if got.SchemaVersion != 3 {
		t.Errorf("SchemaVersion = %d, want 3", got.SchemaVersion)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload = %q, want %q", got.Payload, payload)
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, at)
	}
}

func TestSnapshotSchemaVersionRoundTripsPerSnapshot(t *testing.T) {
	d := openSnapshotsDB(t)
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for i, version := range []int{1, 6, 12} {
		_, err := d.SaveSnapshot(Snapshot{
			SchemaVersion: version,
			Payload:       []byte{byte(i)},
			CreatedAt:     base.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("SaveSnapshot(version=%d): %v", version, err)
		}
	}
	got, found, err := d.LatestSnapshot()
	if err != nil || !found {
		t.Fatalf("LatestSnapshot = %v, %v; want found", got, err)
	}
	if got.SchemaVersion != 12 {
		t.Errorf("latest SchemaVersion = %d, want 12", got.SchemaVersion)
	}
	if !bytes.Equal(got.Payload, []byte{2}) {
		t.Errorf("latest Payload = %q, want %q", got.Payload, []byte{2})
	}
}

func TestLatestSnapshotReturnsNewest(t *testing.T) {
	d := openSnapshotsDB(t)
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for i, payload := range []string{"first", "second", "third"} {
		_, err := d.SaveSnapshot(Snapshot{
			SchemaVersion: 1,
			Payload:       []byte(payload),
			CreatedAt:     base.Add(time.Duration(i) * time.Hour),
		})
		if err != nil {
			t.Fatalf("SaveSnapshot(%s): %v", payload, err)
		}
	}
	got, found, err := d.LatestSnapshot()
	if err != nil || !found {
		t.Fatalf("LatestSnapshot = %v, %v; want found", got, err)
	}
	if string(got.Payload) != "third" {
		t.Errorf("latest payload = %q, want third", got.Payload)
	}
	if want := base.Add(2 * time.Hour); !got.CreatedAt.Equal(want) {
		t.Errorf("latest CreatedAt = %v, want %v", got.CreatedAt, want)
	}
	count, err := d.SnapshotCount()
	if err != nil {
		t.Fatalf("SnapshotCount: %v", err)
	}
	if count != 3 {
		t.Errorf("SnapshotCount = %d, want 3", count)
	}
}

func TestLatestSnapshotEmptyDB(t *testing.T) {
	d := openSnapshotsDB(t)
	got, found, err := d.LatestSnapshot()
	if err != nil {
		t.Fatalf("LatestSnapshot on empty store: %v", err)
	}
	if found {
		t.Errorf("LatestSnapshot found = true on empty store, got %+v", got)
	}
	if !got.CreatedAt.IsZero() || got.SchemaVersion != 0 || got.Payload != nil || got.Seq != 0 {
		t.Errorf("LatestSnapshot on empty store = %+v, want zero value", got)
	}
	count, err := d.SnapshotCount()
	if err != nil {
		t.Fatalf("SnapshotCount on empty store: %v", err)
	}
	if count != 0 {
		t.Errorf("SnapshotCount = %d, want 0", count)
	}
	// A read must not create the table.
	var n int
	if err := d.readDB.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'snapshots'`,
	).Scan(&n); err != nil {
		t.Fatalf("check snapshots table: %v", err)
	}
	if n != 0 {
		t.Error("reading an empty store created the snapshots table")
	}
}

func TestSnapshotSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	at := time.Date(2026, 11, 20, 8, 15, 0, 0, time.UTC)
	payload := []byte(`{"counters":{"verdict_published":{"ready":4}}}`)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := d.SaveSnapshot(Snapshot{SchemaVersion: 5, Payload: payload, CreatedAt: at}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, found, err := reopened.LatestSnapshot()
	if err != nil || !found {
		t.Fatalf("LatestSnapshot after reopen = %v, %v; want found", got, err)
	}
	if got.SchemaVersion != 5 || !bytes.Equal(got.Payload, payload) || !got.CreatedAt.Equal(at) {
		t.Errorf("snapshot after reopen = %+v, want version 5 and original payload/time", got)
	}
}

func TestSaveSnapshotFillsZeroTime(t *testing.T) {
	d := openSnapshotsDB(t)
	before := time.Now().UTC()
	if _, err := d.SaveSnapshot(Snapshot{SchemaVersion: 1, Payload: []byte("x")}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	after := time.Now().UTC()
	got, found, err := d.LatestSnapshot()
	if err != nil || !found {
		t.Fatalf("LatestSnapshot = %v, %v; want found", got, err)
	}
	if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v, want within [%v, %v]", got.CreatedAt, before, after)
	}
}

func TestSaveSnapshotTxRollbackLeavesNoSnapshot(t *testing.T) {
	d := openSnapshotsDB(t)
	boom := errors.New("boom")
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := SaveSnapshotTx(tx, Snapshot{SchemaVersion: 1, Payload: []byte("x")}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write error = %v, want boom", err)
	}
	count, err := d.SnapshotCount()
	if err != nil {
		t.Fatalf("SnapshotCount: %v", err)
	}
	if count != 0 {
		t.Errorf("SnapshotCount after rollback = %d, want 0", count)
	}
}

func TestSaveSnapshotRejectsNegativeSchemaVersion(t *testing.T) {
	d := openSnapshotsDB(t)
	if _, err := d.SaveSnapshot(Snapshot{SchemaVersion: -1, Payload: []byte("x")}); err == nil {
		t.Error("SaveSnapshot with negative schema version succeeded")
	}
	count, err := d.SnapshotCount()
	if err != nil {
		t.Fatalf("SnapshotCount: %v", err)
	}
	if count != 0 {
		t.Errorf("SnapshotCount = %d, want 0", count)
	}
}

// healthyWatch is a Watch with live work and no block: restoring it with an
// active session is an ordinary resume, and without a session a fresh prime.
func healthyWatch() *domain.Watch {
	return &domain.Watch{
		ID:      "watch-1",
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
	}
}

func activeSession() *Session {
	return &Session{ID: "sess-1", Status: SessionStatusActive}
}

func TestClassifyRecoveryAllOutcomes(t *testing.T) {
	blocked := healthyWatch()
	blocked.Status = domain.StatusBlocked
	blocked.Blocked = &domain.BlockedInfo{Reason: domain.BlockReasonBaseChecksFailed}
	terminal := healthyWatch()
	terminal.Verdict = domain.VerdictMerged
	closedSession := activeSession()
	closedSession.Status = SessionStatusClosed

	cases := []struct {
		name string
		node RecoveryNode
		want RecoveryOutcome
	}{
		{
			name: "resumed",
			node: RecoveryNode{Session: activeSession(), Watch: healthyWatch()},
			want: OutcomeResumed,
		},
		{
			name: "fresh-primed",
			node: RecoveryNode{Watch: healthyWatch()},
			want: OutcomeFreshPrimed,
		},
		{
			name: "awaiting-decision",
			node: RecoveryNode{Session: activeSession(), Watch: &domain.Watch{
				Status: domain.StatusWatching, Verdict: domain.VerdictReady,
			}},
			want: OutcomeAwaitingDecision,
		},
		{
			name: "attention-required-rebase",
			node: RecoveryNode{Session: activeSession(), Watch: &domain.Watch{
				Status: domain.StatusWatching, Verdict: domain.VerdictRebase,
			}},
			want: OutcomeAttentionRequired,
		},
		{
			name: "attention-required-blocked",
			node: RecoveryNode{Session: activeSession(), Watch: blocked},
			want: OutcomeAttentionRequired,
		},
		{
			name: "attention-required-closed-session",
			node: RecoveryNode{Session: closedSession, Watch: healthyWatch()},
			want: OutcomeAttentionRequired,
		},
		{
			name: "attention-required-terminal-watch",
			node: RecoveryNode{Session: activeSession(), Watch: terminal},
			want: OutcomeAttentionRequired,
		},
		{
			name: "failed-decode-error",
			node: RecoveryNode{
				Session: activeSession(), Watch: healthyWatch(),
				Err: errors.New("decode payload"),
			},
			want: OutcomeFailed,
		},
		{
			name: "failed-missing-watch",
			node: RecoveryNode{Session: activeSession()},
			want: OutcomeFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyRecovery(tc.node); got != tc.want {
				t.Errorf("ClassifyRecovery = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyRecoveryAbsentSession(t *testing.T) {
	// A healthy Watch with no durable session is primed fresh, not resumed and
	// not failed. The absence of a session is itself the deciding input.
	node := RecoveryNode{Watch: healthyWatch()}
	if got := ClassifyRecovery(node); got != OutcomeFreshPrimed {
		t.Errorf("ClassifyRecovery with no session = %q, want %q", got, OutcomeFreshPrimed)
	}
}

func TestClassifyRecoveryFailedIsNotSilentlyReprimed(t *testing.T) {
	// A failed restore is reported even when there is no session, so it can
	// never be mistaken for an ordinary fresh prime (ADR 0113).
	node := RecoveryNode{Err: errors.New("decode payload"), Watch: healthyWatch()}
	if got := ClassifyRecovery(node); got != OutcomeFailed {
		t.Errorf("ClassifyRecovery with error and no session = %q, want %q", got, OutcomeFailed)
	}
}
