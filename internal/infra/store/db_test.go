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
