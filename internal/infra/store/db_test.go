package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// corpusV1 is the fixed pre-migration fixture: schema version 1 with state
// that must survive the move to later versions (corpus from ADR 0012).
const corpusV1 = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
INSERT INTO meta(key, value) VALUES ('ladder_seq', 7);
PRAGMA user_version = 1;
`

func latestVersion(t *testing.T) int {
	t.Helper()
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	latest := 0
	for _, name := range names {
		v, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("migration version %s: %v", name, err)
		}
		if v > latest {
			latest = v
		}
	}
	return latest
}

func requireTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&n)
	if err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	if n != 1 {
		t.Errorf("table %s missing", name)
	}
}

func requireUserVersion(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	got, err := userVersion(ctx, db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
}

func TestOpenFromEmpty(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	want := latestVersion(t)
	if got := d.Version(); got != want {
		t.Errorf("Version() = %d, want %d", got, want)
	}
	mode, err := d.JournalMode(context.Background())
	if err != nil {
		t.Fatalf("journal mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal mode = %q, want wal", mode)
	}
	for _, table := range []string{"meta", "watches", "obligations", "events"} {
		requireTable(t, d.readDB, table)
	}
	var stored int
	if err := d.readDB.QueryRow(
		`SELECT value FROM meta WHERE key = ?`, metaSchemaVersion,
	).Scan(&stored); err != nil {
		t.Fatalf("read meta schema version: %v", err)
	}
	if stored != want {
		t.Errorf("meta schema_version = %d, want %d", stored, want)
	}
}

func TestMigrateFromCorpusFixture(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	old, err := openPool(path, 1)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := old.ExecContext(ctx, corpusV1); err != nil {
		old.Close()
		t.Fatalf("apply corpus fixture: %v", err)
	}
	requireUserVersion(t, ctx, old, 1)
	if err := old.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated db: %v", err)
	}
	defer d.Close()

	want := latestVersion(t)
	requireUserVersion(t, ctx, d.readDB, want)
	if got := d.Version(); got != want {
		t.Errorf("Version() = %d, want %d", got, want)
	}
	for _, table := range []string{"watches", "obligations", "events"} {
		requireTable(t, d.readDB, table)
	}
	var ladder int
	if err := d.readDB.QueryRow(
		`SELECT value FROM meta WHERE key = 'ladder_seq'`,
	).Scan(&ladder); err != nil {
		t.Fatalf("read ladder_seq: %v", err)
	}
	if ladder != 7 {
		t.Errorf("ladder_seq = %d, want 7", ladder)
	}
}

func TestFailedMigrationDoesNotAdvanceVersion(t *testing.T) {
	ctx := context.Background()
	db, err := openPool(filepath.Join(t.TempDir(), "state.db"), 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	bad := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{Data: []byte(
			`CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);`)},
		"migrations/0002_bad.sql": &fstest.MapFile{Data: []byte(
			`THIS IS NOT VALID SQL;`)},
	}
	version, err := migrate(ctx, db, bad)
	if err == nil {
		t.Fatal("migrate with invalid SQL must fail")
	}
	if version != 1 {
		t.Errorf("migrate returned version %d, want 1", version)
	}
	requireUserVersion(t, ctx, db, 1)
	requireTable(t, db, "meta")

	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE name = 'phantom'`,
	).Scan(&n); err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
}

func TestWALPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for i := 0; i < 2; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		mode, err := d.JournalMode(context.Background())
		if err != nil {
			d.Close()
			t.Fatalf("journal mode #%d: %v", i, err)
		}
		if !strings.EqualFold(mode, "wal") {
			d.Close()
			t.Errorf("journal mode #%d = %q, want wal", i, mode)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
}

func TestTwoConcurrentWritersAreSerialized(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	const (
		writers    = 2
		perWriter  = 50
		wantWrites = writers * perWriter
	)
	var (
		wg       sync.WaitGroup
		inFlight int32
		overlap  int32
		errCh    = make(chan error, wantWrites)
	)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("w-%d-%d", w, i)
				err := d.Write(context.Background(), func(tx *sql.Tx) error {
					if n := atomic.AddInt32(&inFlight, 1); n != 1 {
						atomic.AddInt32(&overlap, 1)
					}
					defer atomic.AddInt32(&inFlight, -1)
					_, err := tx.Exec(`
						INSERT INTO watches
						  (id, repo, pr, head_sha, status, revision, payload)
						VALUES (?, ?, ?, ?, ?, ?, ?)`,
						id, "org/repo", i, "sha", "watching", 1, []byte(`{}`))
					return err
				})
				if err != nil {
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Errorf("concurrent write got SQLITE_BUSY: %v", err)
		} else {
			t.Errorf("concurrent write failed: %v", err)
		}
	}
	if overlap != 0 {
		t.Errorf("writer transactions overlapped %d times", overlap)
	}

	var count int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM watches`).Scan(&count); err != nil {
		t.Fatalf("count watches: %v", err)
	}
	if count != wantWrites {
		t.Errorf("watches = %d, want %d", count, wantWrites)
	}
}

func TestStorePortAndIdempotency(t *testing.T) {
	var _ domain.Store = (*DB)(nil)

	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	w := domain.Watch{
		ID:       "w1",
		Repo:     "org/repo",
		PR:       7,
		HeadSHA:  "abc",
		Status:   domain.StatusWatching,
		Verdict:  domain.VerdictWatching,
		Revision: 1,
	}
	if err := d.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	got, err := d.Watch("w1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.ID != w.ID || got.PR != w.PR || got.Status != domain.StatusWatching {
		t.Errorf("Watch round trip mismatch: %+v", got)
	}
	active, err := d.ActiveWatches()
	if err != nil || len(active) != 1 {
		t.Fatalf("ActiveWatches: %v, %+v", err, active)
	}

	o := domain.Obligation{
		ID:             "w1:1:s1",
		WatchID:        "w1",
		Revision:       1,
		SubscriberID:   "s1",
		VerdictPointer: domain.VerdictReady,
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- d.SaveObligation(o)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("SaveObligation: %v", err)
		}
	}

	var count int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM obligations`).Scan(&count); err != nil {
		t.Fatalf("count obligations: %v", err)
	}
	if count != 1 {
		t.Errorf("obligations = %d, want 1 (duplicate must be ignored)", count)
	}
	pending, err := d.PendingObligations()
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingObligations: %v, %+v", err, pending)
	}
	loaded, err := d.Obligation(o.ID)
	if err != nil || loaded.WatchID != "w1" {
		t.Fatalf("Obligation: %v, %+v", err, loaded)
	}

	if _, err := d.Watch("missing"); err == nil {
		t.Fatal("Watch(missing) must fail")
	} else {
		var appErr *domain.AppError
		if !errors.As(err, &appErr) || appErr.Code != domain.CodeWatchNotFound {
			t.Errorf("Watch(missing) error = %v, want AppError/WATCH_NOT_FOUND", err)
		}
	}
}

func TestStartTaskIdempotentRepeatReturnsOriginal(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	first, err := d.StartTask("req-abc", "task-1", []byte(`{"role":"executor"}`))
	if err != nil {
		t.Fatalf("first StartTask: %v", err)
	}
	if !first.Created || first.Conflict {
		t.Fatalf("first start = %+v, want created and no conflict", first)
	}
	if first.TaskID != "task-1" {
		t.Errorf("first TaskID = %q, want task-1", first.TaskID)
	}

	second, err := d.StartTask("req-abc", "task-2", []byte(`{"role":"orchestrator"}`))
	if err != nil {
		t.Fatalf("second StartTask: %v", err)
	}
	if second.Created || !second.Conflict {
		t.Fatalf("second start = %+v, want duplicate with RejectDuplicate conflict", second)
	}
	if second.TaskID != "task-1" {
		t.Errorf("repeat returned task %q, want original task-1", second.TaskID)
	}
	if string(second.Payload) != string(first.Payload) {
		t.Errorf("repeat payload = %q, want original %q", second.Payload, first.Payload)
	}

	count, err := d.TaskStartCount()
	if err != nil || count != 1 {
		t.Fatalf("TaskStartCount = %d, %v; want 1 (no second task)", count, err)
	}

	var storedKey string
	if err := d.readDB.QueryRow(`SELECT key FROM task_starts`).Scan(&storedKey); err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	if want := domain.IdempotencyKey(domain.StartNamespace, "req-abc"); storedKey != want {
		t.Errorf("stored key = %q, want namespaced %q", storedKey, want)
	}
}

func TestStartTaskDistinctKeysCreateDistinctTasks(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for i, key := range []string{"req-1", "req-2"} {
		res, err := d.StartTask(key, fmt.Sprintf("task-%d", i+1), []byte(key))
		if err != nil {
			t.Fatalf("StartTask(%s): %v", key, err)
		}
		if !res.Created || res.Conflict {
			t.Errorf("StartTask(%s) = %+v, want created with no conflict", key, res)
		}
	}
	count, err := d.TaskStartCount()
	if err != nil || count != 2 {
		t.Fatalf("TaskStartCount = %d, %v; want 2", count, err)
	}
}

func TestStartTaskRejectsEmptyInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, tc := range []struct{ key, taskID string }{
		{"", "task-1"},
		{"req-1", ""},
	} {
		_, err := d.StartTask(tc.key, tc.taskID, []byte("state"))
		if err == nil {
			t.Fatalf("StartTask(%q, %q) must fail", tc.key, tc.taskID)
		}
		var appErr *domain.AppError
		if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
			t.Errorf("StartTask(%q, %q) error = %v, want INVALID_INPUT", tc.key, tc.taskID, err)
		}
	}
}

// TestTaskJournalIsAppendOnly is the acceptance check for T3.43. It appends an
// event and an ack, snapshots the raw rows, appends more records, then proves
// the original rows are byte-for-byte unchanged (ADR 0036 §3).
func TestTaskJournalIsAppendOnly(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	// A secret in the first event must be redacted, and the redacted bytes must
	// then stay frozen for the life of the row.
	seq1, err := d.AppendEvent(ctx, "task-1", "started", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	seq2, err := d.AppendAck(ctx, "task-1", 1)
	if err != nil {
		t.Fatalf("AppendAck: %v", err)
	}

	before, err := d.JournalRecords(ctx, "task-1")
	if err != nil {
		t.Fatalf("JournalRecords before: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("journal rows = %d, want 2", len(before))
	}
	if before[0].Seq != seq1 || before[1].Seq != seq2 {
		t.Fatalf("seqs = %d,%d; want %d,%d", before[0].Seq, before[1].Seq, seq1, seq2)
	}
	if before[1].Kind != journalKindAck || before[1].Revision != 1 {
		t.Fatalf("ack row = %+v, want kind %q revision 1", before[1], journalKindAck)
	}
	if bytes.Contains(before[0].Payload, []byte("ghp_")) {
		t.Errorf("event payload kept a raw token: %s", before[0].Payload)
	}

	// Deep copy: JournalRecords returns payload bytes from the driver, and we
	// want to compare against a snapshot that later reads cannot touch.
	snapshot := make([]JournalRecord, len(before))
	for i, r := range before {
		snapshot[i] = r
		snapshot[i].Payload = append([]byte(nil), r.Payload...)
	}

	// Later writes: a new event and an ack for an older revision. Neither may
	// rewrite the two rows above.
	seq3, err := d.AppendEvent(ctx, "task-1", "progress", "halfway")
	if err != nil {
		t.Fatalf("second AppendEvent: %v", err)
	}
	seq4, err := d.AppendAck(ctx, "task-1", 1)
	if err != nil {
		t.Fatalf("second AppendAck: %v", err)
	}
	if seq3 <= seq2 || seq4 <= seq3 {
		t.Fatalf("seqs not monotonic: %d,%d,%d,%d", seq1, seq2, seq3, seq4)
	}

	after, err := d.JournalRecords(ctx, "task-1")
	if err != nil {
		t.Fatalf("JournalRecords after: %v", err)
	}
	if len(after) != 4 {
		t.Fatalf("journal rows after appends = %d, want 4", len(after))
	}
	for i := range snapshot {
		got, want := after[i], snapshot[i]
		if got.Seq != want.Seq || got.TaskID != want.TaskID || got.Kind != want.Kind ||
			got.Revision != want.Revision || got.CreatedAt != want.CreatedAt {
			t.Errorf("row %d metadata changed:\n got %+v\nwant %+v", i, got, want)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("row %d payload changed:\n got %s\nwant %s", i, got.Payload, want.Payload)
		}
	}
}

// TestTaskJournalRejectsRewrite proves the database trigger enforces
// append-only: a direct UPDATE or DELETE through Write fails, and the stored
// row is untouched.
func TestTaskJournalRejectsRewrite(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.AppendEvent(ctx, "task-1", "started", "original"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	before, err := d.JournalRecords(ctx, "task-1")
	if err != nil {
		t.Fatalf("JournalRecords before: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("journal rows = %d, want 1", len(before))
	}

	updateErr := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_journal SET payload = ? WHERE task_id = ?`,
			[]byte(`tampered`), "task-1")
		return err
	})
	if updateErr == nil {
		t.Error("UPDATE task_journal must be rejected by the append-only trigger")
	}

	deleteErr := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM task_journal WHERE task_id = ?`, "task-1")
		return err
	})
	if deleteErr == nil {
		t.Error("DELETE task_journal must be rejected by the append-only trigger")
	}

	rows, err := d.JournalRecords(ctx, "task-1")
	if err != nil {
		t.Fatalf("JournalRecords: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("journal rows after rewrites = %d, want 1", len(rows))
	}
	if rows[0].Seq != before[0].Seq || !bytes.Equal(rows[0].Payload, before[0].Payload) {
		t.Fatalf("row changed after failed rewrite:\n got %+v\nwant %+v", rows[0], before[0])
	}
}

// TestJournalStatusServedSeparately checks ADR 0025: the current status comes
// from the newest record alone, and asking for it does not touch the history.
func TestJournalStatusServedSeparately(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.JournalStatus(ctx, "empty"); err != nil {
		t.Fatalf("JournalStatus for unknown task: %v", err)
	} else if got, _ := d.JournalStatus(ctx, "empty"); got.Status != "" || got.Cursor != 0 {
		t.Fatalf("JournalStatus for unknown task = %+v, want zero", got)
	}

	if _, err := d.AppendEvent(ctx, "task-1", "started", "go"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := d.AppendEvent(ctx, "task-1", "progress", "halfway"); err != nil {
		t.Fatalf("second AppendEvent: %v", err)
	}
	seqAck, err := d.AppendAck(ctx, "task-1", 9)
	if err != nil {
		t.Fatalf("AppendAck: %v", err)
	}

	st, err := d.JournalStatus(ctx, "task-1")
	if err != nil {
		t.Fatalf("JournalStatus: %v", err)
	}
	if st.Status != journalKindAck || st.Cursor != seqAck || st.Revision != 9 {
		t.Fatalf("JournalStatus = %+v, want status %q cursor %d revision 9",
			st, journalKindAck, seqAck)
	}

	// Reading the status must not append, update or delete any journal row.
	again, err := d.JournalRecords(ctx, "task-1")
	if err != nil {
		t.Fatalf("JournalRecords after status: %v", err)
	}
	if len(again) != 3 {
		t.Fatalf("journal rows after status read = %d, want 3", len(again))
	}
	if again[2].Seq != seqAck || again[2].Revision != 9 {
		t.Fatalf("newest row changed after status read: %+v", again[2])
	}
}

func TestTaskJournalRejectsEmptyInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	for _, tc := range []struct{ taskID, kind string }{
		{"", "started"},
		{"task-1", ""},
	} {
		_, err := d.AppendEvent(ctx, tc.taskID, tc.kind, "msg")
		if err == nil {
			t.Fatalf("AppendEvent(%q, %q) must fail", tc.taskID, tc.kind)
		}
		var appErr *domain.AppError
		if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
			t.Errorf("AppendEvent(%q, %q) error = %v, want INVALID_INPUT", tc.taskID, tc.kind, err)
		}
	}
}

// TestInboxHeldMessageReturnedAfterRestart is the acceptance check for T3.65:
// a message held (inflight) by a process is returned after that process is
// replaced by one with the same PEER_NAME, and it is not duplicated.
func TestInboxHeldMessageReturnedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	const peer = "peer-A"
	msg := InboxMessage{
		WatchID: "w1", Revision: 3, SubscriberID: "s1",
		PeerName: peer, Payload: []byte(`{"kind":"wake"}`),
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := first.InboxPut(msg); err != nil {
		first.Close()
		t.Fatalf("InboxPut: %v", err)
	}
	claimed, err := first.InboxClaim(peer, now, time.Minute)
	if err != nil || len(claimed) != 1 {
		first.Close()
		t.Fatalf("InboxClaim before restart = %d, %v; want 1", len(claimed), err)
	}
	if claimed[0].State != inboxStateInflight {
		first.Close()
		t.Fatalf("held message state = %q, want %q", claimed[0].State, inboxStateInflight)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	// Restart with the same PEER_NAME. The lease from the dead process may not
	// have expired, but reclaim by PEER_NAME must still return the held message.
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer second.Close()
	got, err := second.InboxClaim(peer, now, time.Minute)
	if err != nil {
		t.Fatalf("InboxClaim after restart: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("InboxClaim after restart returned %d messages, want 1", len(got))
	}
	if want := InboxKey(msg.WatchID, msg.Revision, msg.SubscriberID); InboxKey(got[0].WatchID, got[0].Revision, got[0].SubscriberID) != want {
		t.Fatalf("reclaimed key = %q, want %q", InboxKey(got[0].WatchID, got[0].Revision, got[0].SubscriberID), want)
	}
	if !bytes.Equal(got[0].Payload, msg.Payload) {
		t.Errorf("reclaimed payload = %s, want %s", got[0].Payload, msg.Payload)
	}
	var count int
	if err := second.readDB.QueryRow(`SELECT count(*) FROM inbox_messages`).Scan(&count); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if count != 1 {
		t.Errorf("inbox rows = %d, want 1 (restart must not duplicate)", count)
	}
}

// TestInboxReclaimResetsStaleLease proves an expired lease does not keep a
// message held forever: the same PEER_NAME reclaims it and can claim it again.
func TestInboxReclaimResetsStaleLease(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	const peer = "worker"

	if err := d.InboxPut(InboxMessage{
		WatchID: "w2", Revision: 1, SubscriberID: "s2",
		PeerName: peer, Payload: []byte("held"),
	}); err != nil {
		t.Fatalf("InboxPut: %v", err)
	}
	held, err := d.InboxClaim(peer, now, time.Minute)
	if err != nil || len(held) != 1 {
		t.Fatalf("InboxClaim = %d, %v; want 1", len(held), err)
	}

	// After the lease deadline passed the message is stale and must be freed.
	later := now.Add(2 * time.Minute)
	n, err := d.InboxReclaim(peer, later)
	if err != nil || n != 1 {
		t.Fatalf("InboxReclaim = %d, %v; want 1 stale lease reset", n, err)
	}
	rows, err := d.InboxMessages(peer)
	if err != nil || len(rows) != 1 {
		t.Fatalf("InboxMessages = %d, %v; want 1", len(rows), err)
	}
	if rows[0].State != inboxStatePending || rows[0].LeaseUntil != 0 {
		t.Fatalf("reclaimed row = %+v, want pending with no lease", rows[0])
	}
	claimed, err := d.InboxClaim(peer, later, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("InboxClaim after reclaim = %d, %v; want 1", len(claimed), err)
	}
}

// TestInboxPutIdempotentByDeliveryKey checks the ADR 0039 §6 receiver key:
// a repeat delivery is ignored, while a different revision is a new message.
func TestInboxPutIdempotentByDeliveryKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	base := InboxMessage{WatchID: "w3", SubscriberID: "s3", PeerName: "peer"}

	for _, payload := range []string{`{"n":1}`, `{"n":2}`} {
		m := base
		m.Revision = 1
		m.Payload = []byte(payload)
		if err := d.InboxPut(m); err != nil {
			t.Fatalf("InboxPut: %v", err)
		}
	}
	m2 := base
	m2.Revision = 2
	m2.Payload = []byte(`{"n":3}`)
	if err := d.InboxPut(m2); err != nil {
		t.Fatalf("InboxPut rev 2: %v", err)
	}

	rows, err := d.InboxMessages("peer")
	if err != nil || len(rows) != 2 {
		t.Fatalf("InboxMessages = %d, %v; want 2", len(rows), err)
	}
	for _, r := range rows {
		if r.Revision == 1 && string(r.Payload) != `{"n":1}` {
			t.Errorf("duplicate delivery overwrote stored payload: %s", r.Payload)
		}
	}
}

// TestInboxAckRemovesMessage is at-least-once: a message stays stored until
// ack, and a repeated ack is harmless.
func TestInboxAckRemovesMessage(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	now := time.Now().UTC()

	msg := InboxMessage{
		WatchID: "w4", Revision: 1, SubscriberID: "s4",
		PeerName: "peer", Payload: []byte("body"),
	}
	if err := d.InboxPut(msg); err != nil {
		t.Fatalf("InboxPut: %v", err)
	}
	if _, err := d.InboxClaim("peer", now, time.Minute); err != nil {
		t.Fatalf("InboxClaim: %v", err)
	}
	rows, err := d.InboxMessages("peer")
	if err != nil || len(rows) != 1 {
		t.Fatalf("InboxMessages before ack = %d, %v; want 1", len(rows), err)
	}

	key := InboxKey(msg.WatchID, msg.Revision, msg.SubscriberID)
	deleted, err := d.InboxAck(key)
	if err != nil || !deleted {
		t.Fatalf("InboxAck = %v, %v; want true", deleted, err)
	}
	again, err := d.InboxAck(key)
	if err != nil || again {
		t.Fatalf("repeat InboxAck = %v, %v; want false, no error", again, err)
	}
	rows, err = d.InboxMessages("peer")
	if err != nil || len(rows) != 0 {
		t.Fatalf("InboxMessages after ack = %d, %v; want 0", len(rows), err)
	}
}

// TestInboxTreatsPeerPayloadAsUntrusted checks ADR 0039 §6 and T3.4: peer
// messages pass through Redact before they are stored.
func TestInboxTreatsPeerPayloadAsUntrusted(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	if err := d.InboxPut(InboxMessage{
		WatchID: "w5", Revision: 1, SubscriberID: "s5",
		PeerName: "peer", Payload: []byte("token " + secret),
	}); err != nil {
		t.Fatalf("InboxPut: %v", err)
	}
	rows, err := d.InboxMessages("peer")
	if err != nil || len(rows) != 1 {
		t.Fatalf("InboxMessages = %d, %v; want 1", len(rows), err)
	}
	if bytes.Contains(rows[0].Payload, []byte("ghp_")) {
		t.Errorf("stored inbox payload kept a raw token: %s", rows[0].Payload)
	}
}

func TestInboxRejectsEmptyInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	now := time.Now().UTC()

	cases := []InboxMessage{
		{Revision: 1, SubscriberID: "s", PeerName: "peer"},
		{WatchID: "w", Revision: 1, PeerName: "peer"},
		{WatchID: "w", Revision: 1, SubscriberID: "s"},
	}
	for _, m := range cases {
		if err := d.InboxPut(m); err == nil {
			t.Errorf("InboxPut(%+v) must fail", m)
		} else {
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Errorf("InboxPut error = %v, want INVALID_INPUT", err)
			}
		}
	}
	if _, err := d.InboxClaim("", now, time.Minute); err == nil {
		t.Error("InboxClaim with empty peer must fail")
	}
	if _, err := d.InboxReclaim("", now); err == nil {
		t.Error("InboxReclaim with empty peer must fail")
	}
}

// TestWatchOutcomeWakesAllReaders is the acceptance check for T3.79: one
// worker result wakes all fifteen processes that opened the state file. Each
// reader is a separate DB on the same path and receives the outcome through
// its own reads; no broker is involved.
func TestWatchOutcomeWakesAllReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("Open writer: %v", err)
	}
	defer writer.Close()

	const readers = 15
	dbs := make([]*DB, readers)
	for i := range dbs {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open reader %d: %v", i, err)
		}
		defer d.Close()
		dbs[i] = d
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	chans := make([]<-chan WatchOutcome, readers)
	for i, d := range dbs {
		ch, err := d.WatchOutcome(ctx, "worker-key")
		if err != nil {
			t.Fatalf("WatchOutcome reader %d: %v", i, err)
		}
		chans[i] = ch
	}

	for i, ch := range chans {
		select {
		case o, open := <-ch:
			t.Fatalf("reader %d got %+v (open=%v) before publish", i, o, open)
		default:
		}
	}

	want := WatchOutcome{
		Key: "worker-key", Revision: 7,
		Result: []byte(`{"verdict":"ready"}`),
	}
	if err := writer.PublishOutcome(want); err != nil {
		t.Fatalf("PublishOutcome: %v", err)
	}

	for i, ch := range chans {
		select {
		case o, open := <-ch:
			if !open {
				t.Fatalf("reader %d channel closed without outcome", i)
			}
			if o.Key != want.Key || o.Revision != want.Revision || !bytes.Equal(o.Result, want.Result) {
				t.Errorf("reader %d outcome = %+v, want %+v", i, o, want)
			}
			if _, open := <-ch; open {
				t.Errorf("reader %d got a second outcome from one result", i)
			}
		case <-ctx.Done():
			t.Fatalf("reader %d was not woken: %v", i, ctx.Err())
		}
	}
}

// TestWatchOutcomeLateReaderFindsResultInFile proves the result is durable in
// the file, not held in a process-local broker: a reader that opens after the
// writer closed still receives the same outcome on its first read.
func TestWatchOutcomeLateReaderFindsResultInFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("Open writer: %v", err)
	}
	want := WatchOutcome{Key: "late-worker", Revision: 3, Result: []byte("done")}
	if err := writer.PublishOutcome(want); err != nil {
		t.Fatalf("PublishOutcome: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	late, err := Open(path)
	if err != nil {
		t.Fatalf("Open late reader: %v", err)
	}
	defer late.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := late.WatchOutcome(ctx, want.Key)
	if err != nil {
		t.Fatalf("WatchOutcome: %v", err)
	}
	select {
	case o, open := <-ch:
		if !open || o.Key != want.Key || o.Revision != want.Revision || !bytes.Equal(o.Result, want.Result) {
			t.Fatalf("late outcome = %+v (open=%v), want %+v", o, open, want)
		}
	case <-ctx.Done():
		t.Fatalf("late reader was not woken: %v", ctx.Err())
	}
}

// TestWatchOutcomeStopsOnCancel keeps a watcher from leaking after its caller
// gives up waiting.
func TestWatchOutcomeStopsOnCancel(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := d.WatchOutcome(ctx, "no-result")
	if err != nil {
		t.Fatalf("WatchOutcome: %v", err)
	}
	cancel()
	select {
	case o, open := <-ch:
		if open {
			t.Errorf("cancelled watcher delivered %+v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop on cancel")
	}
}

func TestWatchOutcomeRejectsEmptyKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, key := range []string{"", "   "} {
		if _, err := d.WatchOutcome(context.Background(), key); err == nil {
			t.Errorf("WatchOutcome(%q) must fail", key)
		} else {
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Errorf("WatchOutcome(%q) error = %v, want INVALID_INPUT", key, err)
			}
		}
		if err := d.PublishOutcome(WatchOutcome{Key: key}); err == nil {
			t.Errorf("PublishOutcome(%q) must fail", key)
		}
	}
}

// TestWatchOutcomeKeepsInboxWorking guards T3.65: adding the outcome table
// and its writer path must not change durable inbox put, claim or ack.
func TestWatchOutcomeKeepsInboxWorking(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	msg := InboxMessage{
		WatchID: "w6", Revision: 1, SubscriberID: "s6",
		PeerName: "peer", Payload: []byte("body"),
	}
	if err := d.InboxPut(msg); err != nil {
		t.Fatalf("InboxPut: %v", err)
	}
	want := WatchOutcome{Key: "shared-writer", Revision: 1, Result: []byte("done")}
	if err := d.PublishOutcome(want); err != nil {
		t.Fatalf("PublishOutcome: %v", err)
	}

	claimed, err := d.InboxClaim("peer", time.Now().UTC(), time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("InboxClaim after PublishOutcome = %d, %v; want 1", len(claimed), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := d.WatchOutcome(ctx, want.Key)
	if err != nil {
		t.Fatalf("WatchOutcome: %v", err)
	}
	select {
	case o, open := <-ch:
		if !open || !bytes.Equal(o.Result, want.Result) {
			t.Fatalf("outcome = %+v (open=%v), want %+v", o, open, want)
		}
	case <-ctx.Done():
		t.Fatalf("watcher was not woken: %v", ctx.Err())
	}
	if ok, err := d.InboxAck(InboxKey(msg.WatchID, msg.Revision, msg.SubscriberID)); err != nil || !ok {
		t.Fatalf("InboxAck after WatchOutcome = %v, %v; want true", ok, err)
	}
}

// TestWatchOutcomeRedactsResult applies ADR 0012 to worker results as it does
// to inbox payloads (T3.4): a secret in the result never reaches the file.
func TestWatchOutcomeRedactsResult(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	if err := d.PublishOutcome(WatchOutcome{Key: "secret-worker", Result: []byte("token " + secret)}); err != nil {
		t.Fatalf("PublishOutcome: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := d.WatchOutcome(ctx, "secret-worker")
	if err != nil {
		t.Fatalf("WatchOutcome: %v", err)
	}
	select {
	case o := <-ch:
		if bytes.Contains(o.Result, []byte("ghp_")) {
			t.Errorf("delivered outcome kept a raw token: %s", o.Result)
		}
	case <-ctx.Done():
		t.Fatalf("watcher was not woken: %v", ctx.Err())
	}
}

// replayScenarioSteps builds a three-step scenario that touches three durable
// tables: a task start (ADR 0031), an inbox message (ADR 0039) and a journal
// event (ADR 0036). The steps use the same idempotent SQL the store uses, and
// run counts how many times Apply was actually called. If a replay executes a
// step a second time, run grows and the journal gains a second append-only row.
func replayScenarioSteps(t *testing.T, run *int32) []ReplayStep {
	t.Helper()
	return []ReplayStep{
		{
			Name: "task-start",
			Apply: func(tx *sql.Tx) error {
				atomic.AddInt32(run, 1)
				if _, err := tx.Exec(createTaskStartsSQL); err != nil {
					return err
				}
				_, err := tx.Exec(`
					INSERT OR IGNORE INTO task_starts (key, task_id, payload, created_at)
					VALUES (?, ?, ?, ?)`,
					domain.IdempotencyKey(domain.StartNamespace, "req-abc"),
					"task-1", []byte(`{"role":"executor"}`), time.Now().UTC().UnixNano())
				return err
			},
		},
		{
			Name: "inbox-put",
			Apply: func(tx *sql.Tx) error {
				atomic.AddInt32(run, 1)
				if err := ensureInbox(tx); err != nil {
					return err
				}
				m := InboxMessage{
					WatchID: "w1", Revision: 3, SubscriberID: "s1",
					PeerName: "peer-A", Payload: []byte(`{"kind":"wake"}`),
				}
				_, err := tx.Exec(`
					INSERT OR IGNORE INTO inbox_messages
					  (key, watch_id, revision, subscriber_id, peer_name, payload, state, lease_until, created_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
					InboxKey(m.WatchID, m.Revision, m.SubscriberID),
					m.WatchID, m.Revision, m.SubscriberID, m.PeerName, Redact(m.Payload),
					inboxStatePending, time.Now().UTC().UnixNano())
				return err
			},
		},
		{
			Name: "journal-event",
			Apply: func(tx *sql.Tx) error {
				atomic.AddInt32(run, 1)
				for _, stmt := range createTaskJournalSQL {
					if _, err := tx.Exec(stmt); err != nil {
						return err
					}
				}
				_, err := tx.Exec(`
					INSERT INTO task_journal (task_id, kind, revision, payload, created_at)
					VALUES (?, ?, ?, ?, ?)`,
					"task-1", "wake", 0, []byte(`{"kind":"wake"}`), time.Now().UTC().UnixNano())
				return err
			},
		},
	}
}

type replayEffects struct {
	replays int
	tasks   int
	inbox   int
	journal int
}

func snapshotReplayEffects(t *testing.T, d *DB) replayEffects {
	t.Helper()
	replays, err := d.ReplayCount()
	if err != nil {
		t.Fatalf("ReplayCount: %v", err)
	}
	tasks, err := d.TaskStartCount()
	if err != nil {
		t.Fatalf("TaskStartCount: %v", err)
	}
	inbox, err := d.InboxMessages("peer-A")
	if err != nil {
		t.Fatalf("InboxMessages: %v", err)
	}
	journal, err := d.JournalRecords(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("JournalRecords: %v", err)
	}
	return replayEffects{replays: replays, tasks: tasks, inbox: len(inbox), journal: len(journal)}
}

// TestReplayWholeScenarioHasNoSecondEffect is the acceptance check for T3.90.
// The same scenario replayed in full returns the first result and produces no
// second task, inbox message, journal record or replay row (ADR 0042 §6).
func TestReplayWholeScenarioHasNoSecondEffect(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var run int32
	steps := replayScenarioSteps(t, &run)
	result := []byte(`{"verdict":"ready","cursor":9}`)

	first, err := d.Replay("scenario-1", steps, result)
	if err != nil {
		t.Fatalf("first Replay: %v", err)
	}
	if !first.Created || first.Replayed {
		t.Fatalf("first replay = %+v, want created and not replayed", first)
	}
	if !bytes.Equal(first.Result, result) {
		t.Errorf("first result = %s, want %s", first.Result, result)
	}
	if got := atomic.LoadInt32(&run); got != 3 {
		t.Fatalf("steps run on first replay = %d, want 3", got)
	}
	before := snapshotReplayEffects(t, d)
	if before != (replayEffects{replays: 1, tasks: 1, inbox: 1, journal: 1}) {
		t.Fatalf("effects after first replay = %+v, want one of each", before)
	}

	second, err := d.Replay("scenario-1", steps, result)
	if err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if !second.Replayed || second.Created {
		t.Fatalf("second replay = %+v, want replayed and not created", second)
	}
	if !bytes.Equal(second.Result, first.Result) {
		t.Errorf("replayed result = %s, want stored %s", second.Result, first.Result)
	}
	if got := atomic.LoadInt32(&run); got != 3 {
		t.Errorf("steps run after repeat = %d, want 3 (no step may run twice)", got)
	}
	after := snapshotReplayEffects(t, d)
	if after != before {
		t.Errorf("effects after repeat = %+v, want unchanged %+v", after, before)
	}

	stored, ok, err := d.ScenarioResult("scenario-1")
	if err != nil || !ok {
		t.Fatalf("ScenarioResult: %v, %v; want found", ok, err)
	}
	if !bytes.Equal(stored, result) {
		t.Errorf("stored result = %s, want %s", stored, result)
	}
}

// TestReplayKeySeparateFromClientRequestID proves the scenario key does not
// reuse the per-command clientRequestId from ADR 0028 or the task-start key
// from ADR 0031, even when all three receive the same raw string.
func TestReplayKeySeparateFromClientRequestID(t *testing.T) {
	const raw = "req-abc"
	replay := ReplayKey(raw)
	if replay == raw {
		t.Fatalf("ReplayKey(%q) = raw clientRequestId %q", raw, replay)
	}
	if want := domain.IdempotencyKey(replayNamespace, raw); replay != want {
		t.Fatalf("ReplayKey(%q) = %q, want %q", raw, replay, want)
	}
	if replay == domain.IdempotencyKey(domain.StartNamespace, raw) {
		t.Fatal("scenario key collides with task-start key")
	}
	if replay != ReplayKey(raw) {
		t.Fatal("ReplayKey is not deterministic")
	}

	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, err := d.StartTask(raw, "task-1", []byte("start")); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	var run int32
	if _, err := d.Replay(raw, replayScenarioSteps(t, &run), []byte("scenario")); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if n, err := d.TaskStartCount(); err != nil || n != 1 {
		t.Fatalf("TaskStartCount = %d, %v; want 1", n, err)
	}
	if n, err := d.ReplayCount(); err != nil || n != 1 {
		t.Fatalf("ReplayCount = %d, %v; want 1", n, err)
	}
}

// TestReplayDistinctScenariosCreateDistinctEffects shows that the key scopes
// the scenario: a new key runs a new scenario and records a second replay.
func TestReplayDistinctScenariosCreateDistinctEffects(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, key := range []string{"scenario-1", "scenario-2"} {
		var run int32
		res, err := d.Replay(key, replayScenarioSteps(t, &run), []byte(key))
		if err != nil {
			t.Fatalf("Replay(%s): %v", key, err)
		}
		if !res.Created || res.Replayed {
			t.Errorf("Replay(%s) = %+v, want created", key, res)
		}
	}
	if n, err := d.ReplayCount(); err != nil || n != 2 {
		t.Fatalf("ReplayCount = %d, %v; want 2", n, err)
	}
	journal, err := d.JournalRecords(context.Background(), "task-1")
	if err != nil || len(journal) != 2 {
		t.Fatalf("JournalRecords = %d, %v; want 2", len(journal), err)
	}
}

// TestReplayFailedStepLeavesNoKey proves a failed run is not cached: the key
// and all side effects roll back together, so a retry can run the scenario.
func TestReplayFailedStepLeavesNoKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var run int32
	steps := replayScenarioSteps(t, &run)
	boom := errors.New("boom")
	steps[2].Apply = func(tx *sql.Tx) error {
		atomic.AddInt32(&run, 1)
		return boom
	}
	if _, err := d.Replay("scenario-1", steps, []byte("result")); !errors.Is(err, boom) {
		t.Fatalf("Replay with failing step = %v, want boom", err)
	}
	if n, err := d.ReplayCount(); err != nil || n != 0 {
		t.Fatalf("ReplayCount after failure = %d, %v; want 0", n, err)
	}
	if n, err := d.TaskStartCount(); err != nil || n != 0 {
		t.Fatalf("TaskStartCount after failure = %d, %v; want 0 (rolled back)", n, err)
	}

	good := replayScenarioSteps(t, &run)
	res, err := d.Replay("scenario-1", good, []byte("result"))
	if err != nil || !res.Created {
		t.Fatalf("retry Replay = %+v, %v; want created", res, err)
	}
	if n, err := d.ReplayCount(); err != nil || n != 1 {
		t.Fatalf("ReplayCount after retry = %d, %v; want 1", n, err)
	}
}

func TestReplayRejectsEmptyInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	bad := []struct {
		name  string
		key   string
		steps []ReplayStep
	}{
		{"empty key", "", nil},
		{"blank key", "  ", nil},
		{"nil apply", "scenario-1", []ReplayStep{{Name: "step", Apply: nil}}},
		{"empty name", "scenario-1", []ReplayStep{{Name: "  ", Apply: func(*sql.Tx) error { return nil }}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Replay(tc.key, tc.steps, []byte("result"))
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Fatalf("Replay error = %v, want INVALID_INPUT", err)
			}
		})
	}
	if n, err := d.ReplayCount(); err != nil || n != 0 {
		t.Fatalf("ReplayCount after rejected input = %d, %v; want 0", n, err)
	}
}

// TestReplayRedactsResult applies ADR 0012 to a scenario result as it does to
// inbox payloads and worker outcomes: a secret never reaches the file.
func TestReplayRedactsResult(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	res, err := d.Replay("scenario-secret", nil, []byte("token ghp_abcdefghijklmnopqrstuvwxyz0123456789"))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if bytes.Contains(res.Result, []byte("ghp_")) {
		t.Errorf("first result kept a raw token: %s", res.Result)
	}
	stored, ok, err := d.ScenarioResult("scenario-secret")
	if err != nil || !ok {
		t.Fatalf("ScenarioResult: %v, %v; want found", ok, err)
	}
	if bytes.Contains(stored, []byte("ghp_")) {
		t.Errorf("stored result kept a raw token: %s", stored)
	}
}

// checkpointWaitSteps writes steps 1..n with a state blob for each one.
func checkpointWaitSteps(t *testing.T, d *DB, key, taskID string, n int) {
	t.Helper()
	for step := 1; step <= n; step++ {
		state := []byte(fmt.Sprintf(`{"step":%d}`, step))
		res, err := d.CheckpointWait(key, taskID, step, state, nil)
		if err != nil {
			t.Fatalf("CheckpointWait(step %d): %v", step, err)
		}
		if !res.Advanced {
			t.Fatalf("CheckpointWait(step %d) = %+v, want advanced", step, res)
		}
	}
}

// TestCheckpointWaitResumesFromLastStep is the acceptance check for T3.92.
// A restarted wait resumes from the last recorded step, and a stale step
// cannot move progress backwards.
func TestCheckpointWaitResumesFromLastStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	checkpointWaitSteps(t, d, "wait-1", "task-1", 3)
	step, state, found, err := d.ResumeWait("wait-1")
	if err != nil || !found {
		t.Fatalf("ResumeWait: step=%d found=%v err=%v; want found", step, found, err)
	}
	if step != 3 {
		t.Fatalf("resume step = %d, want 3", step)
	}
	if want := []byte(`{"step":3}`); !bytes.Equal(state, want) {
		t.Errorf("resume state = %s, want %s", state, want)
	}

	// An out-of-order callback must not reset the wait to its first step.
	stale, err := d.CheckpointWait("wait-1", "task-1", 1, []byte(`{"step":1}`), nil)
	if err != nil {
		t.Fatalf("stale CheckpointWait: %v", err)
	}
	if stale.Advanced || stale.Created {
		t.Fatalf("stale checkpoint = %+v, want no write", stale)
	}
	if stale.Checkpoint.Step != 3 {
		t.Errorf("stale checkpoint returned step %d, want stored 3", stale.Checkpoint.Step)
	}

	// A restart is a new DB handle on the same file.
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	step, state, found, err = reopened.ResumeWait("wait-1")
	if err != nil || !found {
		t.Fatalf("ResumeWait after restart: step=%d found=%v err=%v; want found", step, found, err)
	}
	if step != 3 {
		t.Fatalf("resume step after restart = %d, want 3", step)
	}
	if want := []byte(`{"step":3}`); !bytes.Equal(state, want) {
		t.Errorf("resume state after restart = %s, want %s", state, want)
	}
	if n, err := reopened.WaitCheckpointCount(); err != nil || n != 1 {
		t.Fatalf("WaitCheckpointCount = %d, %v; want 1", n, err)
	}
}

// TestCheckpointWaitNoCheckpointStartsFromZero proves an empty store is not
// treated as a recorded step 0.
func TestCheckpointWaitNoCheckpointStartsFromZero(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	step, state, found, err := d.ResumeWait("wait-fresh")
	if err != nil || found {
		t.Fatalf("ResumeWait on fresh store: step=%d found=%v err=%v; want not found", step, found, err)
	}
	if step != 0 || state != nil {
		t.Errorf("fresh resume = (%d, %s), want (0, nil)", step, state)
	}
}

// TestCheckpointWaitStateAndStepCommitTogether proves ADR 0015: the step and
// the task state save are one transaction. A failing save commits neither.
func TestCheckpointWaitStateAndStepCommitTogether(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// saveState writes into the task-starts table through the same Tx, which
	// makes the rollback observable from outside the checkpoint row.
	saveTaskStart := func(id string) func(*sql.Tx) error {
		return func(tx *sql.Tx) error {
			if _, err := tx.Exec(createTaskStartsSQL); err != nil {
				return err
			}
			_, err := tx.Exec(`
				INSERT INTO task_starts (key, task_id, payload, created_at)
				VALUES (?, ?, ?, ?)`,
				domain.IdempotencyKey(domain.StartNamespace, id), "task-1", []byte("state"),
				time.Now().UTC().UnixNano())
			return err
		}
	}
	if _, err := d.CheckpointWait("wait-1", "task-1", 1, []byte(`{"step":1}`), saveTaskStart("state-1")); err != nil {
		t.Fatalf("first CheckpointWait: %v", err)
	}

	boom := errors.New("save failed")
	failing := func(tx *sql.Tx) error {
		if err := saveTaskStart("state-2")(tx); err != nil {
			return err
		}
		return boom
	}
	if _, err := d.CheckpointWait("wait-1", "task-1", 2, []byte(`{"step":2}`), failing); !errors.Is(err, boom) {
		t.Fatalf("CheckpointWait with failing save = %v, want boom", err)
	}
	step, state, found, err := d.ResumeWait("wait-1")
	if err != nil || !found {
		t.Fatalf("ResumeWait after failure: step=%d found=%v err=%v; want found", step, found, err)
	}
	if step != 1 {
		t.Errorf("step after failed save = %d, want 1 (rolled back)", step)
	}
	if want := []byte(`{"step":1}`); !bytes.Equal(state, want) {
		t.Errorf("state after failed save = %s, want %s", state, want)
	}
	if n, err := d.TaskStartCount(); err != nil || n != 1 {
		t.Fatalf("TaskStartCount after failure = %d, %v; want 1 (state rolled back)", n, err)
	}

	// The stale state key is now free: a retry with the same step commits.
	if _, err := d.CheckpointWait("wait-1", "task-1", 2, []byte(`{"step":2}`), saveTaskStart("state-2")); err != nil {
		t.Fatalf("retry CheckpointWait: %v", err)
	}
	step, _, _, err = d.ResumeWait("wait-1")
	if err != nil || step != 2 {
		t.Fatalf("resume step after retry = %d, %v; want 2", step, err)
	}
	if n, err := d.TaskStartCount(); err != nil || n != 2 {
		t.Fatalf("TaskStartCount after retry = %d, %v; want 2", n, err)
	}
}

// TestCheckpointWaitKeySeparateFromOtherOperations shows the checkpoint key
// cannot collide with a clientRequestId, a task start or a scenario replay,
// even when all four receive the same raw string.
func TestCheckpointWaitKeySeparateFromOtherOperations(t *testing.T) {
	const raw = "req-abc"
	checkpoint := WaitCheckpointKey(raw)
	if checkpoint == raw {
		t.Fatalf("WaitCheckpointKey(%q) = raw request id", raw)
	}
	if want := domain.IdempotencyKey(waitCheckpointNamespace, raw); checkpoint != want {
		t.Fatalf("WaitCheckpointKey(%q) = %q, want %q", raw, checkpoint, want)
	}
	if checkpoint == ReplayKey(raw) || checkpoint == domain.IdempotencyKey(domain.StartNamespace, raw) {
		t.Fatal("checkpoint key collides with replay or task-start key")
	}
	if checkpoint != WaitCheckpointKey(raw) {
		t.Fatal("WaitCheckpointKey is not deterministic")
	}

	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, err := d.StartTask(raw, "task-1", []byte("start")); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	var run int32
	if _, err := d.Replay(raw, replayScenarioSteps(t, &run), []byte("scenario")); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if _, err := d.CheckpointWait(raw, "task-1", 2, []byte(`{"step":2}`), nil); err != nil {
		t.Fatalf("CheckpointWait: %v", err)
	}
	if n, err := d.TaskStartCount(); err != nil || n != 1 {
		t.Fatalf("TaskStartCount = %d, %v; want 1", n, err)
	}
	if n, err := d.ReplayCount(); err != nil || n != 1 {
		t.Fatalf("ReplayCount = %d, %v; want 1", n, err)
	}
	if n, err := d.WaitCheckpointCount(); err != nil || n != 1 {
		t.Fatalf("WaitCheckpointCount = %d, %v; want 1", n, err)
	}
	c, found, err := d.WaitCheckpoint(raw)
	if err != nil || !found {
		t.Fatalf("WaitCheckpoint: %v, %v; want found", found, err)
	}
	if c.Key != checkpoint || c.Step != 2 || c.TaskID != "task-1" {
		t.Errorf("checkpoint = %+v, want key %q step 2", c, checkpoint)
	}
}

// TestCheckpointWaitDistinctWaitsKeepDistinctProgress scopes progress to one
// wait: advancing one key leaves another key at its own step.
func TestCheckpointWaitDistinctWaitsKeepDistinctProgress(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	checkpointWaitSteps(t, d, "wait-1", "task-1", 3)
	checkpointWaitSteps(t, d, "wait-2", "task-2", 1)
	if n, err := d.WaitCheckpointCount(); err != nil || n != 2 {
		t.Fatalf("WaitCheckpointCount = %d, %v; want 2", n, err)
	}
	for key, want := range map[string]int{"wait-1": 3, "wait-2": 1} {
		step, _, found, err := d.ResumeWait(key)
		if err != nil || !found {
			t.Fatalf("ResumeWait(%s): step=%d found=%v err=%v; want found", key, step, found, err)
		}
		if step != want {
			t.Errorf("ResumeWait(%s) = %d, want %d", key, step, want)
		}
	}
}

func TestCheckpointWaitRejectsInvalidInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	bad := []struct {
		name   string
		key    string
		taskID string
		step   int
	}{
		{"empty key", "", "task-1", 1},
		{"blank key", "  ", "task-1", 1},
		{"empty task id", "wait-1", "", 1},
		{"blank task id", "wait-1", "  ", 1},
		{"negative step", "wait-1", "task-1", -1},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.CheckpointWait(tc.key, tc.taskID, tc.step, []byte("state"), nil)
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Fatalf("CheckpointWait error = %v, want INVALID_INPUT", err)
			}
		})
	}
	if _, _, found, err := d.ResumeWait("  "); err == nil || found {
		t.Fatalf("ResumeWait(blank) = found %v, err %v; want INVALID_INPUT", found, err)
	}
	if n, err := d.WaitCheckpointCount(); err != nil || n != 0 {
		t.Fatalf("WaitCheckpointCount after rejected input = %d, %v; want 0", n, err)
	}
}

// TestCheckpointWaitRedactsState applies ADR 0012 to task state as it does to
// inbox payloads, outcomes and scenario results: a secret never reaches the
// file, and resume returns the redacted copy.
func TestCheckpointWaitRedactsState(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	secret := []byte("token ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	res, err := d.CheckpointWait("wait-secret", "task-1", 1, secret, nil)
	if err != nil {
		t.Fatalf("CheckpointWait: %v", err)
	}
	if bytes.Contains(res.Checkpoint.State, []byte("ghp_")) {
		t.Errorf("checkpoint state kept a raw token: %s", res.Checkpoint.State)
	}
	if !bytes.Contains(secret, []byte("ghp_")) {
		t.Fatal("test input lost its secret before the write")
	}
	_, state, found, err := d.ResumeWait("wait-secret")
	if err != nil || !found {
		t.Fatalf("ResumeWait: found=%v err=%v; want found", found, err)
	}
	if bytes.Contains(state, []byte("ghp_")) {
		t.Errorf("resumed state kept a raw token: %s", state)
	}
	stored, found, err := d.WaitCheckpoint("wait-secret")
	if err != nil || !found {
		t.Fatalf("WaitCheckpoint: found=%v err=%v; want found", found, err)
	}
	if bytes.Contains(stored.State, []byte("ghp_")) {
		t.Errorf("stored state kept a raw token: %s", stored.State)
	}
}

// completionEmit records one delivery of a completion notice. The counter
// observes the call and the durable row observes the committed effect inside
// the same transaction as the notice (ADR 0044 feature 5).
func completionEmit(emitted *int32) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		atomic.AddInt32(emitted, 1)
		if _, err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS completion_effects (
			  seq INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO completion_effects (seq) VALUES (NULL)`)
		return err
	}
}

func completionEffectCount(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM completion_effects`).Scan(&n); err != nil {
		t.Fatalf("count completion effects: %v", err)
	}
	return n
}

// TestNotifyCompletionRedeliveryEmitsOnce is the acceptance test for ADR 0044
// feature 5: a background task's completion notice is emitted once, and a
// re-delivery of the same stable event ID creates neither a second notice nor
// a second effect.
func TestNotifyCompletionRedeliveryEmitsOnce(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	const eventID = "evt-42"
	firstPayload := []byte(`{"status":"done","task":"task-1"}`)
	var emitted int32
	first, err := d.NotifyCompletion(eventID, "task-1", firstPayload, completionEmit(&emitted))
	if err != nil {
		t.Fatalf("first NotifyCompletion: %v", err)
	}
	if !first.Created || first.Duplicate {
		t.Fatalf("first notice = %+v, want created and not duplicate", first)
	}
	if !bytes.Equal(first.Notification.Payload, firstPayload) {
		t.Errorf("first payload = %s, want %s", first.Notification.Payload, firstPayload)
	}
	if got := atomic.LoadInt32(&emitted); got != 1 {
		t.Fatalf("emit calls after first delivery = %d, want 1", got)
	}
	if got := completionEffectCount(t, d); got != 1 {
		t.Fatalf("committed effects after first delivery = %d, want 1", got)
	}

	// The producer retries after a crash with the same event ID and a
	// different payload. The receiver must keep the first notice and emit
	// nothing.
	retryPayload := []byte(`{"status":"done","task":"task-1","retry":true}`)
	second, err := d.NotifyCompletion(eventID, "task-1", retryPayload, completionEmit(&emitted))
	if err != nil {
		t.Fatalf("redelivered NotifyCompletion: %v", err)
	}
	if !second.Duplicate || second.Created {
		t.Fatalf("redelivered notice = %+v, want duplicate and not created", second)
	}
	if !bytes.Equal(second.Notification.Payload, firstPayload) {
		t.Errorf("redelivered payload = %s, want first %s", second.Notification.Payload, firstPayload)
	}
	if got := atomic.LoadInt32(&emitted); got != 1 {
		t.Errorf("emit calls after redelivery = %d, want 1 (no second notification)", got)
	}
	if got := completionEffectCount(t, d); got != 1 {
		t.Errorf("committed effects after redelivery = %d, want 1 (no second effect)", got)
	}
	if n, err := d.CompletionNotificationCount(); err != nil || n != 1 {
		t.Fatalf("CompletionNotificationCount = %d, %v; want 1", n, err)
	}

	stored, found, err := d.CompletionNotification(eventID)
	if err != nil || !found {
		t.Fatalf("CompletionNotification: found=%v err=%v; want found", found, err)
	}
	if stored.EventID != eventID || stored.TaskID != "task-1" || !bytes.Equal(stored.Payload, firstPayload) {
		t.Errorf("stored notice = %+v, want event %q task-1 and first payload", stored, eventID)
	}
}

// TestNotifyCompletionFailedEmitLeavesNoKey proves a failed delivery is not
// cached: the key, the effect and the notice roll back together, so a retry
// can still emit exactly once.
func TestNotifyCompletionFailedEmitLeavesNoKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	boom := errors.New("boom")
	var emitted int32
	_, err = d.NotifyCompletion("evt-1", "task-1", []byte("notice"), func(tx *sql.Tx) error {
		atomic.AddInt32(&emitted, 1)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("NotifyCompletion with failing emit = %v, want boom", err)
	}
	if n, err := d.CompletionNotificationCount(); err != nil || n != 0 {
		t.Fatalf("CompletionNotificationCount after failure = %d, %v; want 0", n, err)
	}
	if _, found, err := d.CompletionNotification("evt-1"); err != nil || found {
		t.Fatalf("CompletionNotification after failure: found=%v err=%v; want not found", found, err)
	}

	res, err := d.NotifyCompletion("evt-1", "task-1", []byte("notice"), completionEmit(&emitted))
	if err != nil || !res.Created {
		t.Fatalf("retry NotifyCompletion = %+v, %v; want created", res, err)
	}
	if n, err := d.CompletionNotificationCount(); err != nil || n != 1 {
		t.Fatalf("CompletionNotificationCount after retry = %d, %v; want 1", n, err)
	}
	if got := atomic.LoadInt32(&emitted); got != 2 {
		t.Errorf("emit calls after retry = %d, want 2 (failed emit ran once, retry once)", got)
	}
	if got := completionEffectCount(t, d); got != 1 {
		t.Errorf("committed effects after retry = %d, want 1", got)
	}
}

// TestNotifyCompletionKeySeparateFromOtherOperations proves the completion
// notice key does not reuse the keys of ADR 0028, 0031, 0042 or 0043, even
// when every operation receives the same raw string.
func TestNotifyCompletionKeySeparateFromOtherOperations(t *testing.T) {
	const raw = "req-abc"
	notice := CompletionNotificationKey(raw)
	if notice == raw {
		t.Fatalf("CompletionNotificationKey(%q) = raw request id", raw)
	}
	if want := domain.IdempotencyKey(completionNotificationNamespace, raw); notice != want {
		t.Fatalf("CompletionNotificationKey(%q) = %q, want %q", raw, notice, want)
	}
	if notice == domain.IdempotencyKey(domain.StartNamespace, raw) ||
		notice == ReplayKey(raw) || notice == WaitCheckpointKey(raw) {
		t.Fatal("completion notice key collides with start, replay or checkpoint key")
	}
	if notice != CompletionNotificationKey(raw) {
		t.Fatal("CompletionNotificationKey is not deterministic")
	}

	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, err := d.StartTask(raw, "task-1", []byte("start")); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	var run int32
	if _, err := d.Replay(raw, replayScenarioSteps(t, &run), []byte("scenario")); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if _, err := d.CheckpointWait(raw, "task-1", 2, []byte(`{"step":2}`), nil); err != nil {
		t.Fatalf("CheckpointWait: %v", err)
	}
	var emitted int32
	if _, err := d.NotifyCompletion(raw, "task-1", []byte("done"), completionEmit(&emitted)); err != nil {
		t.Fatalf("NotifyCompletion: %v", err)
	}
	if n, err := d.TaskStartCount(); err != nil || n != 1 {
		t.Fatalf("TaskStartCount = %d, %v; want 1", n, err)
	}
	if n, err := d.ReplayCount(); err != nil || n != 1 {
		t.Fatalf("ReplayCount = %d, %v; want 1", n, err)
	}
	if n, err := d.WaitCheckpointCount(); err != nil || n != 1 {
		t.Fatalf("WaitCheckpointCount = %d, %v; want 1", n, err)
	}
	if n, err := d.CompletionNotificationCount(); err != nil || n != 1 {
		t.Fatalf("CompletionNotificationCount = %d, %v; want 1", n, err)
	}
}

// TestNotifyCompletionDistinctEventsEmitSeparately scopes deduplication to one
// event ID: a new event emits a new notice for the same task.
func TestNotifyCompletionDistinctEventsEmitSeparately(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var emitted int32
	for _, eventID := range []string{"evt-1", "evt-2"} {
		res, err := d.NotifyCompletion(eventID, "task-1", []byte(eventID), completionEmit(&emitted))
		if err != nil {
			t.Fatalf("NotifyCompletion(%s): %v", eventID, err)
		}
		if !res.Created || res.Duplicate {
			t.Errorf("NotifyCompletion(%s) = %+v, want created", eventID, res)
		}
	}
	if got := atomic.LoadInt32(&emitted); got != 2 {
		t.Errorf("emit calls = %d, want 2", got)
	}
	if got := completionEffectCount(t, d); got != 2 {
		t.Errorf("committed effects = %d, want 2", got)
	}
	if n, err := d.CompletionNotificationCount(); err != nil || n != 2 {
		t.Fatalf("CompletionNotificationCount = %d, %v; want 2", n, err)
	}
}

// TestNotifyCompletionRedactsPayload applies ADR 0012 to notice payloads as it
// does to inbox messages, outcomes and scenario results: a secret never
// reaches the file and a repeat returns the redacted copy.
func TestNotifyCompletionRedactsPayload(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	secret := []byte(`{"status":"done","token":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`)
	var emitted int32
	res, err := d.NotifyCompletion("evt-secret", "task-1", secret, completionEmit(&emitted))
	if err != nil {
		t.Fatalf("NotifyCompletion: %v", err)
	}
	if bytes.Contains(res.Notification.Payload, []byte("ghp_")) {
		t.Errorf("returned notice kept a raw token: %s", res.Notification.Payload)
	}
	if !bytes.Contains(secret, []byte("ghp_")) {
		t.Fatal("test input lost its secret before the write")
	}
	stored, found, err := d.CompletionNotification("evt-secret")
	if err != nil || !found {
		t.Fatalf("CompletionNotification: found=%v err=%v; want found", found, err)
	}
	if bytes.Contains(stored.Payload, []byte("ghp_")) {
		t.Errorf("stored notice kept a raw token: %s", stored.Payload)
	}
}

func TestNotifyCompletionRejectsInvalidInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	bad := []struct {
		name    string
		eventID string
		taskID  string
		emit    func(*sql.Tx) error
	}{
		{"empty event id", "", "task-1", completionEmit(new(int32))},
		{"blank event id", "  ", "task-1", completionEmit(new(int32))},
		{"empty task id", "evt-1", "", completionEmit(new(int32))},
		{"blank task id", "evt-1", "  ", completionEmit(new(int32))},
		{"nil emit", "evt-1", "task-1", nil},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.NotifyCompletion(tc.eventID, tc.taskID, []byte("notice"), tc.emit)
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Fatalf("NotifyCompletion error = %v, want INVALID_INPUT", err)
			}
		})
	}
	if n, err := d.CompletionNotificationCount(); err != nil || n != 0 {
		t.Fatalf("CompletionNotificationCount after rejected input = %d, %v; want 0", n, err)
	}
}
