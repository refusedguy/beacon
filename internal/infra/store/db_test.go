package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
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

// TestMigration0007ParityAcrossInstallDates checks that a database opened from
// user_version 5 ends with the same registry schema as a fresh database. Version
// 7 adds columns that were appended to 0005 after that version had shipped.
func TestMigration0007ParityAcrossInstallDates(t *testing.T) {
	ctx := context.Background()
	oldPath := filepath.Join(t.TempDir(), "v5.db")
	old, err := openPool(oldPath, 1, driverName)
	if err != nil {
		t.Fatalf("open v5 db: %v", err)
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("migration version %s: %v", name, err)
		}
		if version > 5 {
			continue
		}
		body, err := fs.ReadFile(migrationFS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := old.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := old.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
		t.Fatalf("set user_version 5: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close v5 db: %v", err)
	}

	upgraded, err := Open(oldPath)
	if err != nil {
		t.Fatalf("Open v5 db: %v", err)
	}
	defer upgraded.Close()
	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open fresh db: %v", err)
	}
	defer fresh.Close()

	if upgraded.Version() != fresh.Version() {
		t.Errorf("version from v5 = %d, fresh = %d", upgraded.Version(), fresh.Version())
	}
	for _, table := range []string{"agent_registry", "watch_bindings"} {
		want := tableColumnSigs(t, fresh.readDB, table)
		got := tableColumnSigs(t, upgraded.readDB, table)
		for col, sig := range want {
			if got[col] != sig {
				t.Errorf("%s.%s from v5 = %q, fresh = %q", table, col, got[col], sig)
			}
		}
	}
}

func TestMigrateFromCorpusFixture(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	old, err := openPool(path, 1, driverName)
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
	db, err := openPool(filepath.Join(t.TempDir(), "state.db"), 1, driverName)
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

func TestWriteLockWaitHistogramRecordsTwoWriters(t *testing.T) {
	metrics := observe.NewMetrics()
	previous := observe.Default()
	observe.SetDefault(observe.NewRecorder(nil, metrics))
	defer observe.SetDefault(previous)

	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- d.Write(context.Background(), func(tx *sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first writer did not enter its transaction")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- d.Write(context.Background(), func(tx *sql.Tx) error {
			return nil
		})
	}()

	// Wait until the second job is in the write queue behind the first writer's
	// open transaction. The queue length is the observable state; the deadline
	// comes from the runner, not from a fixed delay.
	deadline := time.Now().Add(5 * time.Second)
	if runnerDeadline, ok := t.Deadline(); ok && runnerDeadline.Before(deadline) {
		deadline = runnerDeadline
	}
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for len(d.writeCh) == 0 {
		if !time.Now().Before(deadline) {
			t.Fatal("second writer did not queue behind the first")
		}
		select {
		case <-poll.C:
		case <-t.Context().Done():
			t.Fatal("test context ended before the second writer queued")
		}
	}
	close(release)

	for i, done := range []chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("writer %d did not finish", i)
		}
	}

	count, sum := d.LockWaitStats()
	if count < 1 {
		t.Errorf("write lock wait count = %d, want >= 1", count)
	}
	if sum <= 0 {
		t.Errorf("write lock wait sum = %v, want > 0", sum)
	}

	var got struct {
		Histograms map[string]struct {
			Count uint64  `json:"count"`
			Sum   float64 `json:"sum"`
		} `json:"histograms"`
	}
	if err := json.Unmarshal(metrics.JSON(), &got); err != nil {
		t.Fatalf("metrics JSON: %v", err)
	}
	h, ok := got.Histograms[HistogramDBLockWait]
	if !ok {
		t.Fatalf("histogram %q missing from metrics JSON: %v", HistogramDBLockWait, got.Histograms)
	}
	if h.Count < 1 || h.Sum <= 0 {
		t.Errorf("%s = count %d sum %v, want count >= 1 and sum > 0", HistogramDBLockWait, h.Count, h.Sum)
	}
}

func TestStorePortAndIdempotency(t *testing.T) {
	var (
		_ domain.WatchStore               = (*DB)(nil)
		_ domain.ObligationStore          = (*DB)(nil)
		_ domain.BlockerIncidents         = (*DB)(nil)
		_ domain.FixerBinding             = (*DB)(nil)
		_ domain.OrchestratorBindingStore = (*DB)(nil)
	)

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

// TestStoreBlockedWatchSurvivesReopen checks that the block details live in
// the Watch JSON payload and survive a close/reopen without a migration.
func TestStoreBlockedWatchSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	since := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	want := domain.Watch{
		ID:       "w-blocked",
		Repo:     "org/repo",
		PR:       12,
		HeadSHA:  "abc",
		Status:   domain.StatusBlocked,
		Verdict:  domain.VerdictBlocked,
		Revision: 4,
		Blocked: &domain.BlockedInfo{
			Reason:       domain.BlockReasonBaseChecksFailed,
			FailedChecks: []string{"ci/test", "gates"},
			Since:        since,
			BlockedBy:    "ci-bridge",
		},
	}

	check := func(tag string, d *DB) {
		t.Helper()
		got, err := d.Watch(want.ID)
		if err != nil {
			t.Fatalf("%s Watch: %v", tag, err)
		}
		if got.Status != domain.StatusBlocked || got.Verdict != domain.VerdictBlocked {
			t.Errorf("%s status/verdict = %q/%q, want blocked/blocked", tag, got.Status, got.Verdict)
		}
		if got.Blocked == nil {
			t.Fatalf("%s blocked info is nil", tag)
		}
		if got.Blocked.Reason != want.Blocked.Reason || got.Blocked.BlockedBy != want.Blocked.BlockedBy ||
			!got.Blocked.Since.Equal(want.Blocked.Since) ||
			len(got.Blocked.FailedChecks) != 2 || got.Blocked.FailedChecks[0] != "ci/test" || got.Blocked.FailedChecks[1] != "gates" {
			t.Errorf("%s blocked info = %+v, want %+v", tag, got.Blocked, want.Blocked)
		}
	}

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := first.SaveWatch(want); err != nil {
		first.Close()
		t.Fatalf("SaveWatch: %v", err)
	}
	check("before reopen", first)
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer second.Close()
	check("after reopen", second)
}

// TestStoreLegacyWatchWithoutBlocked checks that a payload written before the
// Blocked field existed still decodes: the absent key becomes nil, not an error.
func TestStoreLegacyWatchWithoutBlocked(t *testing.T) {
	legacy := []byte(`{"id":"w1","status":"watching","verdict":"watching","revision":1}`)
	got, err := decodeWatch(legacy)
	if err != nil {
		t.Fatalf("decodeWatch(legacy): %v", err)
	}
	if got.Blocked != nil {
		t.Errorf("legacy payload decoded with blocked info: %+v", got.Blocked)
	}
	if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictWatching || got.Revision != 1 {
		t.Errorf("legacy payload decoded wrong: %+v", got)
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

// TestTaskLogThreePartsAndActivityAreSeparate is the acceptance check for
// T3.119 (ADR 0051 feature 2): request, outcome and report are three separate
// records, and live activity is read from its own store, never mixed into the
// three parts.
func TestTaskLogThreePartsAndActivityAreSeparate(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	parts := []struct{ part, payload string }{
		{LogPartRequest, `{"prompt":"build it"}`},
		{LogPartOutcome, `{"result":"done"}`},
		{LogPartReport, `{"summary":"shipped"}`},
	}
	for _, p := range parts {
		if err := d.SetLogPart("task-1", p.part, []byte(p.payload)); err != nil {
			t.Fatalf("SetLogPart(%s): %v", p.part, err)
		}
	}
	if err := d.SetActivity("task-1", "compiling"); err != nil {
		t.Fatalf("SetActivity: %v", err)
	}

	got, err := d.TaskLogParts("task-1")
	if err != nil {
		t.Fatalf("TaskLogParts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("TaskLogParts = %d rows, want 3 separate records", len(got))
	}
	for i, want := range parts {
		if got[i].Part != want.part || string(got[i].Payload) != want.payload {
			t.Errorf("part %d = %+v, want %s/%s", i, got[i], want.part, want.payload)
		}
		if got[i].TaskID != "task-1" || got[i].CreatedAt == 0 {
			t.Errorf("part %d missing metadata: %+v", i, got[i])
		}
	}

	act, found, err := d.Activity("task-1")
	if err != nil || !found {
		t.Fatalf("Activity: found=%v err=%v; want found", found, err)
	}
	if act.Message != "compiling" {
		t.Errorf("activity = %q, want compiling", act.Message)
	}

	// The three-part table must hold exactly the three parts and nothing else.
	var count int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM task_log WHERE task_id = ?`, "task-1").Scan(&count); err != nil {
		t.Fatalf("count task_log: %v", err)
	}
	if count != 3 {
		t.Errorf("task_log rows = %d, want 3 (activity must not be stored here)", count)
	}
	var activityRows int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM task_activity`).Scan(&activityRows); err != nil {
		t.Fatalf("count task_activity: %v", err)
	}
	if activityRows != 1 {
		t.Errorf("task_activity rows = %d, want 1", activityRows)
	}
}

// TestTaskLogSetLogPartIsIdempotentPerPart proves a redelivery cannot overwrite
// an already written part with a different payload.
func TestTaskLogSetLogPartIsIdempotentPerPart(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.SetLogPart("task-2", LogPartRequest, []byte("first")); err != nil {
		t.Fatalf("first SetLogPart: %v", err)
	}
	if err := d.SetLogPart("task-2", LogPartRequest, []byte("second")); err != nil {
		t.Fatalf("repeat SetLogPart: %v", err)
	}
	parts, err := d.TaskLogParts("task-2")
	if err != nil || len(parts) != 1 {
		t.Fatalf("TaskLogParts = %d, %v; want 1", len(parts), err)
	}
	if string(parts[0].Payload) != "first" {
		t.Errorf("repeat overwrote part: %s, want first", parts[0].Payload)
	}
}

// TestTaskLogActivityIsCurrentNotHistory checks that live activity holds only
// the latest message, is cleared independently, and never enters the three
// durable parts.
func TestTaskLogActivityIsCurrentNotHistory(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.SetLogPart("task-3", LogPartRequest, []byte("ask")); err != nil {
		t.Fatalf("SetLogPart: %v", err)
	}
	if err := d.SetActivity("task-3", "step one"); err != nil {
		t.Fatalf("SetActivity #1: %v", err)
	}
	if err := d.SetActivity("task-3", "step two"); err != nil {
		t.Fatalf("SetActivity #2: %v", err)
	}
	act, found, err := d.Activity("task-3")
	if err != nil || !found || act.Message != "step two" {
		t.Fatalf("Activity = %+v found=%v err=%v; want latest step two", act, found, err)
	}
	parts, err := d.TaskLogParts("task-3")
	if err != nil || len(parts) != 1 {
		t.Fatalf("TaskLogParts after activity = %d, %v; want 1 (activity not a part)", len(parts), err)
	}

	if err := d.ClearActivity("task-3"); err != nil {
		t.Fatalf("ClearActivity: %v", err)
	}
	if _, found, err := d.Activity("task-3"); err != nil || found {
		t.Fatalf("Activity after clear: found=%v err=%v; want not found", found, err)
	}
	if err := d.ClearActivity("task-3"); err != nil {
		t.Fatalf("second ClearActivity: %v", err)
	}
	parts, err = d.TaskLogParts("task-3")
	if err != nil || len(parts) != 1 {
		t.Fatalf("TaskLogParts after clear = %d, %v; want 1", len(parts), err)
	}
}

// TestTaskViewShowsRunningAndReadyAsDifferentStates checks the CLI-facing
// states: work in progress until the report exists, then result ready. The
// report flips the state; activity and the other parts stay separate.
func TestTaskViewShowsRunningAndReadyAsDifferentStates(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	view, err := d.TaskView("task-4")
	if err != nil {
		t.Fatalf("TaskView on empty: %v", err)
	}
	if view.State != TaskStateRunning || len(view.Parts) != 0 || view.HasActivity {
		t.Fatalf("empty view = %+v, want running with no parts or activity", view)
	}

	if err := d.SetLogPart("task-4", LogPartRequest, []byte("ask")); err != nil {
		t.Fatalf("SetLogPart request: %v", err)
	}
	if err := d.SetActivity("task-4", "working"); err != nil {
		t.Fatalf("SetActivity: %v", err)
	}
	view, err = d.TaskView("task-4")
	if err != nil {
		t.Fatalf("TaskView with request: %v", err)
	}
	if view.State != TaskStateRunning {
		t.Errorf("state with request = %q, want running", view.State)
	}
	if !view.HasActivity || view.Activity.Message != "working" {
		t.Errorf("view activity = %+v, want working", view.Activity)
	}

	if err := d.SetLogPart("task-4", LogPartOutcome, []byte("result")); err != nil {
		t.Fatalf("SetLogPart outcome: %v", err)
	}
	view, err = d.TaskView("task-4")
	if err != nil {
		t.Fatalf("TaskView with outcome: %v", err)
	}
	if view.State != TaskStateRunning {
		t.Errorf("state with outcome = %q, want running until report", view.State)
	}

	if err := d.SetLogPart("task-4", LogPartReport, []byte("summary")); err != nil {
		t.Fatalf("SetLogPart report: %v", err)
	}
	if err := d.ClearActivity("task-4"); err != nil {
		t.Fatalf("ClearActivity: %v", err)
	}
	view, err = d.TaskView("task-4")
	if err != nil {
		t.Fatalf("TaskView with report: %v", err)
	}
	if view.State != TaskStateReady {
		t.Errorf("state with report = %q, want result_ready", view.State)
	}
	if len(view.Parts) != 3 {
		t.Errorf("view parts = %d, want 3", len(view.Parts))
	}
	if view.HasActivity {
		t.Errorf("view has activity after clear: %+v", view.Activity)
	}
}

// TestTaskLogSurvivesRestart proves the three parts and the live activity are
// durable in the file, readable by a new DB handle on the same path.
func TestTaskLogSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := first.SetLogPart("task-5", LogPartRequest, []byte("ask")); err != nil {
		first.Close()
		t.Fatalf("SetLogPart: %v", err)
	}
	if err := first.SetLogPart("task-5", LogPartReport, []byte("summary")); err != nil {
		first.Close()
		t.Fatalf("SetLogPart report: %v", err)
	}
	if err := first.SetActivity("task-5", "finalizing"); err != nil {
		first.Close()
		t.Fatalf("SetActivity: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer second.Close()

	view, err := second.TaskView("task-5")
	if err != nil {
		t.Fatalf("TaskView after restart: %v", err)
	}
	if view.State != TaskStateReady || len(view.Parts) != 2 {
		t.Fatalf("view after restart = %+v, want ready with 2 parts", view)
	}
	if !view.HasActivity || view.Activity.Message != "finalizing" {
		t.Errorf("activity after restart = %+v, want finalizing", view.Activity)
	}
}

func TestTaskLogRejectsInvalidInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	bad := []struct {
		name   string
		taskID string
		part   string
	}{
		{"empty task id", "", LogPartRequest},
		{"blank task id", "  ", LogPartRequest},
		{"invalid part", "task-1", "status"},
		{"empty part", "task-1", ""},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := d.SetLogPart(tc.taskID, tc.part, []byte("payload"))
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Fatalf("SetLogPart error = %v, want INVALID_INPUT", err)
			}
		})
	}
	if _, err := d.TaskLogParts("  "); err == nil {
		t.Error("TaskLogParts(blank) must fail")
	}
	if err := d.SetActivity("", "x"); err == nil {
		t.Error("SetActivity(empty) must fail")
	}
	if _, _, err := d.Activity("  "); err == nil {
		t.Error("Activity(blank) must fail")
	}
	if err := d.ClearActivity("  "); err == nil {
		t.Error("ClearActivity(blank) must fail")
	}
	if ok, err := tableExists(context.Background(), d.readDB, "task_log"); err != nil {
		t.Fatalf("check task_log: %v", err)
	} else if ok {
		var count int
		if err := d.readDB.QueryRow(`SELECT count(*) FROM task_log`).Scan(&count); err != nil {
			t.Fatalf("count task_log: %v", err)
		}
		if count != 0 {
			t.Errorf("task_log rows after rejected input = %d, want 0", count)
		}
	}
}

// TestTaskLogDoesNotDuplicateAdr0025Status proves the new three-part log does
// not reuse or disturb the ADR 0025 status/cursor journal. Reading and writing
// task_log and task_activity leave task_journal and JournalStatus untouched.
func TestTaskLogDoesNotDuplicateAdr0025Status(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	seq, err := d.AppendEvent(ctx, "task-7", "started", "go")
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	before, err := d.JournalStatus(ctx, "task-7")
	if err != nil {
		t.Fatalf("JournalStatus before: %v", err)
	}
	if before.Status != "started" || before.Cursor != seq {
		t.Fatalf("JournalStatus before = %+v, want started/cursor %d", before, seq)
	}

	if err := d.SetLogPart("task-7", LogPartRequest, []byte("ask")); err != nil {
		t.Fatalf("SetLogPart: %v", err)
	}
	if err := d.SetLogPart("task-7", LogPartReport, []byte("summary")); err != nil {
		t.Fatalf("SetLogPart report: %v", err)
	}
	if err := d.SetActivity("task-7", "running"); err != nil {
		t.Fatalf("SetActivity: %v", err)
	}
	if _, err := d.TaskView("task-7"); err != nil {
		t.Fatalf("TaskView: %v", err)
	}

	after, err := d.JournalStatus(ctx, "task-7")
	if err != nil {
		t.Fatalf("JournalStatus after: %v", err)
	}
	if after != before {
		t.Errorf("JournalStatus changed: got %+v, want %+v", after, before)
	}
	rows, err := d.JournalRecords(ctx, "task-7")
	if err != nil || len(rows) != 1 {
		t.Fatalf("JournalRecords = %d, %v; want 1 (log writes must not append)", len(rows), err)
	}
}

// TestAcceptTaskWALBeforeAcceptedReply is the core of T3.123. The "accepted"
// reply callback runs only after the task state is committed to the WAL, so a
// fresh connection polling the issued id at reply time already finds it.
func TestAcceptTaskWALBeforeAcceptedReply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	const id = "task-wal-1"
	state := []byte(`{"phase":"running"}`)

	// Before acceptance the issued id is not on disk.
	if _, found, err := d.AcceptedTask(id); err != nil || found {
		t.Fatalf("AcceptedTask before accept: found=%v err=%v", found, err)
	}

	var (
		replyCalls     int
		foundAtReply   bool
		payloadAtReply []byte
	)
	reply := func() error {
		replyCalls++
		// A fresh connection models a restarted process polling the id. It is
		// opened at the reply point, after AcceptTask's write has committed.
		reader, err := openPool(path, 1, driverName)
		if err != nil {
			return err
		}
		defer reader.Close()
		s, found, err := acceptedTask(ctx, reader, id)
		if err != nil {
			return err
		}
		foundAtReply = found
		payloadAtReply = s.Payload
		return nil
	}

	if err := d.AcceptTask(ctx, id, state, reply); err != nil {
		t.Fatalf("AcceptTask: %v", err)
	}
	if replyCalls != 1 {
		t.Errorf("reply calls = %d, want 1", replyCalls)
	}
	if !foundAtReply {
		t.Error("issued task id was not on disk at the accepted reply")
	}
	if string(payloadAtReply) != string(state) {
		t.Errorf("payload at reply = %q, want %q", payloadAtReply, state)
	}
}

// TestAcceptTaskFoundAfterRestart proves the ADR acceptance check: after the
// process restarts, polling the issued id finds the state that was committed
// before the reply.
func TestAcceptTaskFoundAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	state := []byte(`{"phase":"running","n":1}`)
	if err := first.AcceptTask(ctx, "task-wal-2", state, func() error { return nil }); err != nil {
		first.Close()
		t.Fatalf("AcceptTask: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	s, found, err := second.AcceptedTask("task-wal-2")
	if err != nil {
		t.Fatalf("AcceptedTask after restart: %v", err)
	}
	if !found {
		t.Fatal("issued task id not found after restart")
	}
	if string(s.Payload) != string(state) {
		t.Errorf("payload after restart = %q, want %q", s.Payload, state)
	}
}

// TestAcceptTaskNoReplyWhenWriteFails proves the ordering holds in the failure
// direction too: if the WAL write cannot commit, no accepted reply is emitted.
func TestAcceptTaskNoReplyWhenWriteFails(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	replied := false
	err = d.AcceptTask(ctx, "task-wal-3", []byte("state"), func() error {
		replied = true
		return nil
	})
	if err == nil {
		t.Fatal("AcceptTask on a closed store must fail")
	}
	if replied {
		t.Error("accepted reply was emitted despite a failed WAL write")
	}
}

// TestAcceptTaskRepeatDoesNotDuplicate keeps acceptance keyed by the issued id:
// a repeat updates the stored state but never creates a second task.
func TestAcceptTaskRepeatDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	replies := 0
	reply := func() error { replies++; return nil }
	if err := d.AcceptTask(ctx, "same", []byte("v1"), reply); err != nil {
		t.Fatalf("first AcceptTask: %v", err)
	}
	if err := d.AcceptTask(ctx, "same", []byte("v2"), reply); err != nil {
		t.Fatalf("second AcceptTask: %v", err)
	}
	if replies != 2 {
		t.Errorf("reply calls = %d, want 2 (one per call)", replies)
	}
	if n, err := d.AcceptedTaskCount(); err != nil || n != 1 {
		t.Errorf("AcceptedTaskCount = %d, %v; want 1", n, err)
	}
	s, found, err := d.AcceptedTask("same")
	if err != nil || !found {
		t.Fatalf("AcceptedTask: found=%v err=%v; want found", found, err)
	}
	if string(s.Payload) != "v2" {
		t.Errorf("payload after repeat = %q, want v2", s.Payload)
	}
}

func TestAcceptTaskRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	noReply := func() error { return nil }
	if err := d.AcceptTask(ctx, "", []byte("s"), noReply); err == nil {
		t.Error("empty task id must be rejected")
	}
	if err := d.AcceptTask(ctx, "  ", []byte("s"), noReply); err == nil {
		t.Error("blank task id must be rejected")
	}
	if err := d.AcceptTask(ctx, "task", []byte("s"), nil); err == nil {
		t.Error("nil reply must be rejected")
	}
	if _, _, err := d.AcceptedTask(" "); err == nil {
		t.Error("AcceptedTask(blank) must fail")
	}
	if n, err := d.AcceptedTaskCount(); err != nil || n != 0 {
		t.Errorf("AcceptedTaskCount after rejected input = %d, %v; want 0", n, err)
	}
}

// queueFinishEffect records one completion side effect and commits a durable
// row for it inside the same transaction as the queue completion. The counter
// observes the call and the row observes the commit (ADR 0058 feature 5).
func queueFinishEffect(called *int32) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		atomic.AddInt32(called, 1)
		if _, err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS queue_effects (
			  seq INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO queue_effects (seq) VALUES (NULL)`)
		return err
	}
}

func queueEffectCount(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.readDB.QueryRow(`
		SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'queue_effects'
	`).Scan(&n); err != nil {
		t.Fatalf("check queue_effects: %v", err)
	}
	if n == 0 {
		return 0
	}
	if err := d.readDB.QueryRow(`SELECT count(*) FROM queue_effects`).Scan(&n); err != nil {
		t.Fatalf("count queue effects: %v", err)
	}
	return n
}

// TestNamedQueueAtomicContinuation is the acceptance check for ADR 0058 feature 5:
// the continuation task and the completion of the current task commit in one
// transaction, and the continuation is immediately leaseable from its queue.
func TestNamedQueueAtomicContinuation(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.EnqueueQueueTask("build", "build-1", []byte("first job")); err != nil {
		t.Fatalf("EnqueueQueueTask build-1: %v", err)
	}
	if err := d.EnqueueQueueTask("build", "build-2", []byte("second job")); err != nil {
		t.Fatalf("EnqueueQueueTask build-2: %v", err)
	}

	now := time.Now()
	lease, found, err := d.LeaseQueueTask("build", "worker-a", now, time.Minute)
	if err != nil {
		t.Fatalf("LeaseQueueTask: %v", err)
	}
	if !found {
		t.Fatal("lease found no task in build queue")
	}
	if lease.TaskID != "build-1" || lease.Queue != "build" || lease.OwnerID != "worker-a" {
		t.Fatalf("lease = %+v, want build-1 in build owned by worker-a", lease)
	}
	if lease.FencingToken == 0 {
		t.Fatal("lease has no fencing token")
	}
	if string(lease.Payload) != "first job" {
		t.Errorf("lease payload = %q, want first job", lease.Payload)
	}

	var effects int32
	next := &QueueContinuation{Queue: "verify", TaskID: "verify-1", Payload: []byte("verify job")}
	res, err := d.CompleteQueueTask("build-1", "worker-a", lease.FencingToken, next, queueFinishEffect(&effects))
	if err != nil {
		t.Fatalf("CompleteQueueTask: %v", err)
	}
	if !res.Completed || !res.Continued {
		t.Fatalf("completion = %+v, want completed and continued", res)
	}
	if res.TaskID != "build-1" || res.FencingToken != lease.FencingToken {
		t.Errorf("completion ids = %+v, want build-1 at token %d", res, lease.FencingToken)
	}
	if got := atomic.LoadInt32(&effects); got != 1 {
		t.Errorf("finish calls = %d, want 1", got)
	}
	if got := queueEffectCount(t, d); got != 1 {
		t.Errorf("committed finish rows = %d, want 1", got)
	}

	task, found, err := d.QueueTask("build-1")
	if err != nil || !found {
		t.Fatalf("QueueTask build-1: found=%v err=%v", found, err)
	}
	if task.State != queueStateDone || task.FencingToken != lease.FencingToken {
		t.Errorf("completed task = %+v, want done at token %d", task, lease.FencingToken)
	}
	cont, found, err := d.QueueTask("verify-1")
	if err != nil || !found {
		t.Fatalf("QueueTask verify-1: found=%v err=%v", found, err)
	}
	if cont.Queue != "verify" || cont.State != queueStatePending || string(cont.Payload) != "verify job" {
		t.Errorf("continuation = %+v, want pending verify job in verify", cont)
	}

	// The remaining build task and the new verify task are both leaseable from
	// their own named queues.
	nextBuild, found, err := d.LeaseQueueTask("build", "worker-b", now, time.Minute)
	if err != nil || !found || nextBuild.TaskID != "build-2" {
		t.Fatalf("next build lease = %+v, found=%v, err=%v; want build-2", nextBuild, found, err)
	}
	vlease, found, err := d.LeaseQueueTask("verify", "worker-v", now, time.Minute)
	if err != nil || !found || vlease.TaskID != "verify-1" {
		t.Fatalf("verify lease = %+v, found=%v, err=%v; want verify-1", vlease, found, err)
	}
}

// TestQueueLeaseFencesStaleCompletion proves the lease is fenced: reclaiming an
// expired lease issues a higher token, and the old worker's completion is
// refused in full, so it cannot emit a side effect or enqueue a continuation.
func TestQueueLeaseFencesStaleCompletion(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.EnqueueQueueTask("q", "task-1", []byte("work")); err != nil {
		t.Fatalf("EnqueueQueueTask: %v", err)
	}
	now := time.Now()
	first, found, err := d.LeaseQueueTask("q", "worker-1", now, time.Minute)
	if err != nil || !found {
		t.Fatalf("first lease: %+v found=%v err=%v", first, found, err)
	}

	// The lease expired; a second worker reclaims the same task with a new token.
	later := now.Add(2 * time.Minute)
	second, found, err := d.LeaseQueueTask("q", "worker-2", later, time.Minute)
	if err != nil || !found {
		t.Fatalf("second lease: %+v found=%v err=%v", second, found, err)
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("reclaimed task = %q, want %q", second.TaskID, first.TaskID)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("reclaim token %d not higher than %d", second.FencingToken, first.FencingToken)
	}

	var effects int32
	orphan := &QueueContinuation{Queue: "q2", TaskID: "orphan", Payload: []byte("must not exist")}
	_, err = d.CompleteQueueTask("task-1", "worker-1", first.FencingToken, orphan, queueFinishEffect(&effects))
	if !IsQueueFenceStale(err) {
		t.Fatalf("stale completion error = %v, want stale fence", err)
	}
	if got := atomic.LoadInt32(&effects); got != 0 {
		t.Errorf("stale completion ran finish %d times, want 0", got)
	}
	if got := queueEffectCount(t, d); got != 0 {
		t.Errorf("stale completion committed %d effect rows, want 0", got)
	}
	if _, found, err := d.QueueTask("orphan"); err != nil || found {
		t.Fatalf("stale completion enqueued continuation: found=%v err=%v", found, err)
	}
	task, found, err := d.QueueTask("task-1")
	if err != nil || !found {
		t.Fatalf("QueueTask after stale: found=%v err=%v", found, err)
	}
	if task.State != queueStateLeased || task.OwnerID != "worker-2" || task.FencingToken != second.FencingToken {
		t.Errorf("task after stale = %+v, want leased by worker-2 at token %d", task, second.FencingToken)
	}

	// The current token still completes the task and enqueues the continuation.
	res, err := d.CompleteQueueTask("task-1", "worker-2", second.FencingToken, orphan, queueFinishEffect(&effects))
	if err != nil || !res.Completed || !res.Continued {
		t.Fatalf("current completion = %+v, %v; want completed and continued", res, err)
	}
	if got := queueEffectCount(t, d); got != 1 {
		t.Errorf("committed effects after completion = %d, want 1", got)
	}
	if _, found, err := d.QueueTask("orphan"); err != nil || !found {
		t.Fatalf("continuation after completion: found=%v err=%v; want found", found, err)
	}
}

// TestQueueCompletionRollsBackOnFailedFinish proves the continuation is not
// stored when the completion's side effect fails: the whole transaction rolls
// back and a retry with the same lease succeeds once.
func TestQueueCompletionRollsBackOnFailedFinish(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.EnqueueQueueTask("q", "t", []byte("work")); err != nil {
		t.Fatalf("EnqueueQueueTask: %v", err)
	}
	lease, found, err := d.LeaseQueueTask("q", "w", time.Now(), time.Minute)
	if err != nil || !found {
		t.Fatalf("lease: %+v found=%v err=%v", lease, found, err)
	}

	boom := errors.New("boom")
	var called int32
	failFinish := func(tx *sql.Tx) error {
		atomic.AddInt32(&called, 1)
		if _, err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS queue_effects (
			  seq INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO queue_effects (seq) VALUES (NULL)`); err != nil {
			return err
		}
		return boom
	}
	next := &QueueContinuation{Queue: "next", TaskID: "next-1", Payload: []byte("next")}
	_, err = d.CompleteQueueTask("t", "w", lease.FencingToken, next, failFinish)
	if !errors.Is(err, boom) {
		t.Fatalf("completion with failing finish = %v, want boom", err)
	}
	if got := atomic.LoadInt32(&called); got != 1 {
		t.Errorf("finish calls = %d, want 1", got)
	}
	if got := queueEffectCount(t, d); got != 0 {
		t.Errorf("effect rows after failed finish = %d, want 0", got)
	}
	if _, found, err := d.QueueTask("next-1"); err != nil || found {
		t.Fatalf("continuation after failed finish: found=%v err=%v; want not found", found, err)
	}
	task, found, err := d.QueueTask("t")
	if err != nil || !found {
		t.Fatalf("QueueTask after failed finish: found=%v err=%v", found, err)
	}
	if task.State != queueStateLeased || task.FencingToken != lease.FencingToken {
		t.Errorf("task after failed finish = %+v, want leased at token %d", task, lease.FencingToken)
	}

	var effects int32
	res, err := d.CompleteQueueTask("t", "w", lease.FencingToken, next, queueFinishEffect(&effects))
	if err != nil || !res.Completed || !res.Continued {
		t.Fatalf("retry completion = %+v, %v; want completed and continued", res, err)
	}
	if got := queueEffectCount(t, d); got != 1 {
		t.Errorf("effect rows after retry = %d, want 1", got)
	}
}

// TestQueueCompletionIdempotentRepeat keeps a retried completion from enqueuing
// the continuation a second time.
func TestQueueCompletionIdempotentRepeat(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.EnqueueQueueTask("q", "t", []byte("work")); err != nil {
		t.Fatalf("EnqueueQueueTask: %v", err)
	}
	lease, found, err := d.LeaseQueueTask("q", "w", time.Now(), time.Minute)
	if err != nil || !found {
		t.Fatalf("lease: %+v found=%v err=%v", lease, found, err)
	}

	var effects int32
	next := &QueueContinuation{Queue: "next", TaskID: "next-1", Payload: []byte("n")}
	first, err := d.CompleteQueueTask("t", "w", lease.FencingToken, next, queueFinishEffect(&effects))
	if err != nil || !first.Completed || !first.Continued {
		t.Fatalf("first completion = %+v, %v; want completed and continued", first, err)
	}
	second, err := d.CompleteQueueTask("t", "w", lease.FencingToken, next, queueFinishEffect(&effects))
	if err != nil {
		t.Fatalf("repeat completion: %v", err)
	}
	if second.Completed || second.Continued {
		t.Fatalf("repeat completion = %+v, want no new effect", second)
	}
	if got := atomic.LoadInt32(&effects); got != 1 {
		t.Errorf("finish calls after repeat = %d, want 1", got)
	}
	if got := queueEffectCount(t, d); got != 1 {
		t.Errorf("effect rows after repeat = %d, want 1", got)
	}
	tasks, err := d.QueueTasks("next")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("QueueTasks(next) = %d tasks, %v; want 1", len(tasks), err)
	}
}

// TestNamedQueuesAreIsolated keeps tasks of one queue out of another.
func TestNamedQueuesAreIsolated(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.EnqueueQueueTask("alpha", "a1", []byte("a")); err != nil {
		t.Fatalf("EnqueueQueueTask alpha: %v", err)
	}
	if err := d.EnqueueQueueTask("beta", "b1", []byte("b")); err != nil {
		t.Fatalf("EnqueueQueueTask beta: %v", err)
	}
	now := time.Now()
	al, found, err := d.LeaseQueueTask("alpha", "w", now, time.Minute)
	if err != nil || !found || al.TaskID != "a1" {
		t.Fatalf("alpha lease = %+v found=%v err=%v; want a1", al, found, err)
	}
	if _, found, err := d.LeaseQueueTask("alpha", "w", now, time.Minute); err != nil || found {
		t.Fatalf("alpha second lease found=%v err=%v; want no task (b1 is in beta)", found, err)
	}
	bl, found, err := d.LeaseQueueTask("beta", "w", now, time.Minute)
	if err != nil || !found || bl.TaskID != "b1" {
		t.Fatalf("beta lease = %+v found=%v err=%v; want b1", bl, found, err)
	}
	aTasks, err := d.QueueTasks("alpha")
	if err != nil || len(aTasks) != 1 || aTasks[0].ID != "a1" {
		t.Fatalf("QueueTasks(alpha) = %+v, %v; want only a1", aTasks, err)
	}
	bTasks, err := d.QueueTasks("beta")
	if err != nil || len(bTasks) != 1 || bTasks[0].ID != "b1" {
		t.Fatalf("QueueTasks(beta) = %+v, %v; want only b1", bTasks, err)
	}
}

// TestQueueFenceTokenMonotonicAcrossRestart proves the counter is durable: a
// lease taken after a reopen carries a higher token than one before it.
func TestQueueFenceTokenMonotonicAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := first.EnqueueQueueTask("q", "t1", []byte("1")); err != nil {
		first.Close()
		t.Fatalf("EnqueueQueueTask t1: %v", err)
	}
	lease1, found, err := first.LeaseQueueTask("q", "w", time.Now(), time.Minute)
	if err != nil || !found {
		first.Close()
		t.Fatalf("lease #1: %+v found=%v err=%v", lease1, found, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	if err := second.EnqueueQueueTask("q", "t2", []byte("2")); err != nil {
		t.Fatalf("EnqueueQueueTask t2: %v", err)
	}
	lease2, found, err := second.LeaseQueueTask("q", "w", time.Now(), time.Minute)
	if err != nil || !found {
		t.Fatalf("lease #2: %+v found=%v err=%v", lease2, found, err)
	}
	if lease2.FencingToken <= lease1.FencingToken {
		t.Fatalf("token after restart %d, want higher than %d", lease2.FencingToken, lease1.FencingToken)
	}
}

func TestQueueRejectsInvalidInput(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.EnqueueQueueTask("  ", "t", []byte("x")); err == nil {
		t.Error("EnqueueQueueTask with blank queue must fail")
	}
	if err := d.EnqueueQueueTask("q", " ", []byte("x")); err == nil {
		t.Error("EnqueueQueueTask with blank task id must fail")
	}
	now := time.Now()
	if _, _, err := d.LeaseQueueTask(" ", "w", now, time.Minute); err == nil {
		t.Error("LeaseQueueTask with blank queue must fail")
	}
	if _, _, err := d.LeaseQueueTask("q", " ", now, time.Minute); err == nil {
		t.Error("LeaseQueueTask with blank owner must fail")
	}
	if _, _, err := d.LeaseQueueTask("q", "w", now, 0); err == nil {
		t.Error("LeaseQueueTask with zero ttl must fail")
	}
	if _, err := d.CompleteQueueTask(" ", "w", 1, nil, nil); err == nil {
		t.Error("CompleteQueueTask with blank task id must fail")
	}
	if _, err := d.CompleteQueueTask("t", " ", 1, nil, nil); err == nil {
		t.Error("CompleteQueueTask with blank owner must fail")
	}
	if _, err := d.CompleteQueueTask("t", "w", 0, nil, nil); err == nil {
		t.Error("CompleteQueueTask with zero token must fail")
	}
	if _, err := d.CompleteQueueTask("t", "w", 1, &QueueContinuation{Queue: " ", TaskID: "n"}, nil); err == nil {
		t.Error("CompleteQueueTask with blank continuation queue must fail")
	}
	if _, err := d.CompleteQueueTask("t", "w", 1, &QueueContinuation{Queue: "q", TaskID: " "}, nil); err == nil {
		t.Error("CompleteQueueTask with blank continuation task id must fail")
	}
	tasks, err := d.QueueTasks("q")
	if err != nil || len(tasks) != 0 {
		t.Fatalf("QueueTasks after rejected input = %d, %v; want 0", len(tasks), err)
	}
}

// TestFIFOPauseResumeCancel is the acceptance check for ADR 0059 feature 5:
// tasks leave the queue in FIFO order, pause stops dispatch, resume restarts
// it and cancel drops a task from anywhere in the queue.
func TestFIFOPauseResumeCancel(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, id := range []string{"job-1", "job-2", "job-3"} {
		if err := d.FIFOEnqueue(id, []byte("payload "+id)); err != nil {
			t.Fatalf("FIFOEnqueue %s: %v", id, err)
		}
	}
	first, found, err := d.FIFOClaim()
	if err != nil || !found || first.ID != "job-1" {
		t.Fatalf("first claim = %+v, found=%v, err=%v; want job-1", first, found, err)
	}
	if first.State != fifoStateInflight || string(first.Payload) != "payload job-1" {
		t.Errorf("first claim = %+v; want inflight job-1 payload", first)
	}

	if err := d.FIFOPause(); err != nil {
		t.Fatalf("FIFOPause: %v", err)
	}
	paused, err := d.FIFOPaused()
	if err != nil || !paused {
		t.Fatalf("FIFOPaused = %v, %v; want true", paused, err)
	}
	if _, found, err := d.FIFOClaim(); err != nil || found {
		t.Fatalf("claim while paused found=%v err=%v; want no dispatch", found, err)
	}

	removed, err := d.FIFOCancel("job-3")
	if err != nil || !removed {
		t.Fatalf("FIFOCancel job-3 = %v, %v; want removed", removed, err)
	}
	removed, err = d.FIFOCancel("job-3")
	if err != nil || removed {
		t.Fatalf("repeat FIFOCancel = %v, %v; want false", removed, err)
	}

	if err := d.FIFOResume(); err != nil {
		t.Fatalf("FIFOResume: %v", err)
	}
	paused, err = d.FIFOPaused()
	if err != nil || paused {
		t.Fatalf("FIFOPaused after resume = %v, %v; want false", paused, err)
	}
	second, found, err := d.FIFOClaim()
	if err != nil || !found || second.ID != "job-2" {
		t.Fatalf("second claim = %+v, found=%v, err=%v; want job-2", second, found, err)
	}
	if _, found, err := d.FIFOClaim(); err != nil || found {
		t.Fatalf("claim after queue drained found=%v err=%v; want no task", found, err)
	}

	for _, id := range []string{"job-1", "job-2"} {
		done, err := d.FIFOComplete(id)
		if err != nil || !done {
			t.Fatalf("FIFOComplete %s = %v, %v; want true", id, done, err)
		}
	}
	done, err := d.FIFOComplete("job-2")
	if err != nil || done {
		t.Fatalf("repeat FIFOComplete = %v, %v; want false", done, err)
	}
	state, err := d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState: %v", err)
	}
	if state.Paused || len(state.Items) != 0 {
		t.Fatalf("final FIFOState = %+v; want empty and running", state)
	}
}

// TestFIFOCrashSafeResume proves the queue continues from the state file after
// a process restart: order, the pause command and inflight items all survive,
// and FIFORecover returns claimed items to their original FIFO positions.
func TestFIFOCrashSafeResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := first.FIFOEnqueue(id, []byte("payload "+id)); err != nil {
			first.Close()
			t.Fatalf("FIFOEnqueue %s: %v", id, err)
		}
	}
	for _, want := range []string{"a", "b"} {
		it, found, err := first.FIFOClaim()
		if err != nil || !found || it.ID != want {
			first.Close()
			t.Fatalf("claim #%s = %+v, found=%v, err=%v", want, it, found, err)
		}
	}
	// The process dies holding a and b; c is still pending. The pause command
	// and both inflight rows are committed to the one state file.
	if err := first.FIFOPause(); err != nil {
		first.Close()
		t.Fatalf("FIFOPause: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	paused, err := second.FIFOPaused()
	if err != nil || !paused {
		t.Fatalf("pause after restart = %v, %v; want true", paused, err)
	}
	state, err := second.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState after restart: %v", err)
	}
	if len(state.Items) != 3 {
		t.Fatalf("items after restart = %d, want 3", len(state.Items))
	}
	for i, want := range []struct {
		id    string
		state string
	}{
		{"a", fifoStateInflight},
		{"b", fifoStateInflight},
		{"c", fifoStatePending},
	} {
		got := state.Items[i]
		if got.ID != want.id || got.State != want.state {
			t.Errorf("item %d = %+v, want %s in %s", i, got, want.id, want.state)
		}
	}
	if _, found, err := second.FIFOClaim(); err != nil || found {
		t.Fatalf("claim while restarted-paused found=%v err=%v; want no dispatch", found, err)
	}

	if err := second.FIFOResume(); err != nil {
		t.Fatalf("FIFOResume: %v", err)
	}
	n, err := second.FIFORecover()
	if err != nil || n != 2 {
		t.Fatalf("FIFORecover = %d, %v; want 2", n, err)
	}
	n, err = second.FIFORecover()
	if err != nil || n != 0 {
		t.Fatalf("repeat FIFORecover = %d, %v; want 0", n, err)
	}

	for _, want := range []string{"a", "b", "c"} {
		it, found, err := second.FIFOClaim()
		if err != nil || !found || it.ID != want {
			t.Fatalf("recovered claim = %+v, found=%v, err=%v; want %s", it, found, err, want)
		}
		if it.State != fifoStateInflight {
			t.Errorf("recovered claim %s state = %q, want inflight", it.ID, it.State)
		}
	}
	if _, found, err := second.FIFOClaim(); err != nil || found {
		t.Fatalf("claim after recovery drained found=%v err=%v; want no task", found, err)
	}
}

// TestFIFOEnqueueIdempotentAndRedaction keeps a repeated enqueue from adding a
// second row and keeps secrets out of the state file. It also checks that
// blank ids are rejected by each command.
func TestFIFOEnqueueIdempotentAndRedaction(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	secret := []byte("token ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	if err := d.FIFOEnqueue("task", secret); err != nil {
		t.Fatalf("FIFOEnqueue: %v", err)
	}
	if err := d.FIFOEnqueue("task", []byte("second write ignored")); err != nil {
		t.Fatalf("repeat FIFOEnqueue: %v", err)
	}
	state, err := d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState: %v", err)
	}
	if len(state.Items) != 1 || state.Items[0].ID != "task" {
		t.Fatalf("FIFOState items = %+v; want one task", state.Items)
	}
	if bytes.Contains(state.Items[0].Payload, []byte("ghp_")) {
		t.Errorf("stored payload kept a raw token: %s", state.Items[0].Payload)
	}

	if err := d.FIFOEnqueue(" ", nil); err == nil {
		t.Error("FIFOEnqueue with blank id must fail")
	}
	if _, err := d.FIFOCancel(" "); err == nil {
		t.Error("FIFOCancel with blank id must fail")
	}
	if _, err := d.FIFOComplete(" "); err == nil {
		t.Error("FIFOComplete with blank id must fail")
	}
}

// installFIFOBatchPoison adds a trigger that aborts the insert of one id. It is
// the fault injected into a batch to prove all inserts share one transaction:
// the failing insert makes FIFOEnqueueBatch return an error, so the caller's
// Write rolls the whole batch back instead of leaving earlier rows committed.
func installFIFOBatchPoison(t *testing.T, d *DB) {
	t.Helper()
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureFIFOQueue(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			CREATE TRIGGER fifo_batch_poison BEFORE INSERT ON fifo_queue
			WHEN NEW.id = 'poison'
			BEGIN SELECT RAISE(ABORT, 'poison rejected'); END`)
		return err
	})
	if err != nil {
		t.Fatalf("install poison trigger: %v", err)
	}
}

// TestFIFOEnqueueBatchOneTransaction is the acceptance check for T3.155. A
// failing insert in the middle of a batch rolls back the whole batch, which
// only happens when every insert is in ONE transaction. With one Write per
// task the earlier rows would already be committed. The good batch then proves
// order is preserved and FIFOClaim still serves the batch (T3.138).
func TestFIFOEnqueueBatchOneTransaction(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	installFIFOBatchPoison(t, d)
	poisoned := []FIFOItem{
		{ID: "good-1", Payload: []byte("one")},
		{ID: "poison", Payload: []byte("bad")},
		{ID: "good-2", Payload: []byte("two")},
	}
	if _, err := d.FIFOEnqueueBatch(poisoned); err == nil {
		t.Fatal("batch with an aborting insert must fail")
	}
	state, err := d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState after failed batch: %v", err)
	}
	if len(state.Items) != 0 {
		t.Fatalf("items after failed batch = %d, want 0 (whole batch must roll back)", len(state.Items))
	}

	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TRIGGER fifo_batch_poison`)
		return err
	}); err != nil {
		t.Fatalf("drop poison trigger: %v", err)
	}

	good := []FIFOItem{
		{ID: "a", Payload: []byte("one")},
		{ID: "b", Payload: []byte("two")},
		{ID: "c", Payload: []byte("three")},
	}
	res, err := d.FIFOEnqueueBatch(good)
	if err != nil {
		t.Fatalf("FIFOEnqueueBatch: %v", err)
	}
	if res.Inserted != 3 || res.Ignored != 0 {
		t.Fatalf("batch result = %+v, want 3 inserted and 0 ignored", res)
	}
	state, err = d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState after batch: %v", err)
	}
	if len(state.Items) != 3 {
		t.Fatalf("items after batch = %d, want 3", len(state.Items))
	}
	for i, want := range []string{"a", "b", "c"} {
		if state.Items[i].ID != want || state.Items[i].State != fifoStatePending {
			t.Errorf("item %d = %+v, want %s pending", i, state.Items[i], want)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		it, found, err := d.FIFOClaim()
		if err != nil || !found || it.ID != want {
			t.Fatalf("claim = %+v, found=%v, err=%v; want %s in enqueue order", it, found, err, want)
		}
	}
}

// TestFIFOEnqueueBatchIdempotentAndRedacted keeps the batch under the same
// rules as a single enqueue: a repeated id inserts nothing and keeps the first
// payload, secrets are redacted, and a blank id rejects the whole batch before
// any row is written.
func TestFIFOEnqueueBatchIdempotentAndRedacted(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	secret := []byte("token ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	first := []FIFOItem{
		{ID: "x", Payload: secret},
		{ID: "y", Payload: []byte("two")},
		{ID: "z", Payload: []byte("three")},
	}
	res, err := d.FIFOEnqueueBatch(first)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if res.Inserted != 3 || res.Ignored != 0 {
		t.Fatalf("first batch = %+v, want 3 inserted", res)
	}

	repeat := []FIFOItem{
		{ID: "x", Payload: []byte("overwrite")},
		{ID: "y", Payload: []byte("overwrite")},
		{ID: "z", Payload: []byte("overwrite")},
	}
	res, err = d.FIFOEnqueueBatch(repeat)
	if err != nil {
		t.Fatalf("repeat batch: %v", err)
	}
	if res.Inserted != 0 || res.Ignored != 3 {
		t.Fatalf("repeat batch = %+v, want 0 inserted and 3 ignored", res)
	}

	state, err := d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState: %v", err)
	}
	if len(state.Items) != 3 {
		t.Fatalf("items after repeat = %d, want 3", len(state.Items))
	}
	if bytes.Contains(state.Items[0].Payload, []byte("ghp_")) {
		t.Errorf("stored payload kept a raw token: %s", state.Items[0].Payload)
	}
	if string(state.Items[0].Payload) == "overwrite" {
		t.Error("repeat overwrote the first payload")
	}
	if !bytes.Contains(secret, []byte("ghp_")) {
		t.Fatal("test input lost its secret before the write")
	}

	_, err = d.FIFOEnqueueBatch([]FIFOItem{{ID: "ok"}, {ID: " "}})
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
		t.Fatalf("batch with blank id error = %v, want INVALID_INPUT", err)
	}
	state, err = d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState after rejected batch: %v", err)
	}
	if len(state.Items) != 3 {
		t.Fatalf("items after rejected batch = %d, want 3 (nothing committed)", len(state.Items))
	}
}

// TestFIFOEnqueueBatchEmptyAndMixedWithSingle keeps an empty batch a no-op and
// proves batched and single enqueues share one FIFO. A task enqueued by
// FIFOEnqueue and tasks enqueued by FIFOEnqueueBatch are claimed in the order
// they were written, and FIFOComplete still removes them (T3.138).
func TestFIFOEnqueueBatchEmptyAndMixedWithSingle(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, empty := range [][]FIFOItem{nil, {}} {
		res, err := d.FIFOEnqueueBatch(empty)
		if err != nil || res.Inserted != 0 || res.Ignored != 0 {
			t.Fatalf("empty batch = %+v, %v; want a zero no-op", res, err)
		}
	}
	state, err := d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState after empty batch: %v", err)
	}
	if len(state.Items) != 0 {
		t.Fatalf("items after empty batch = %d, want 0", len(state.Items))
	}

	if err := d.FIFOEnqueue("single", []byte("s")); err != nil {
		t.Fatalf("FIFOEnqueue: %v", err)
	}
	batch := []FIFOItem{
		{ID: "batch-1", Payload: []byte("b1")},
		{ID: "batch-2", Payload: []byte("b2")},
	}
	if _, err := d.FIFOEnqueueBatch(batch); err != nil {
		t.Fatalf("FIFOEnqueueBatch: %v", err)
	}
	for _, want := range []string{"single", "batch-1", "batch-2"} {
		it, found, err := d.FIFOClaim()
		if err != nil || !found || it.ID != want {
			t.Fatalf("claim = %+v, found=%v, err=%v; want %s", it, found, err, want)
		}
		done, err := d.FIFOComplete(it.ID)
		if err != nil || !done {
			t.Fatalf("FIFOComplete %s = %v, %v; want true", it.ID, done, err)
		}
	}
	state, err = d.FIFOState()
	if err != nil {
		t.Fatalf("FIFOState after completion: %v", err)
	}
	if len(state.Items) != 0 {
		t.Fatalf("items after completion = %d, want 0", len(state.Items))
	}
}

// ---- ADR 265: operation receipts, tombstones and exactly-once effects ----

func newOperationStore(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS effect_runs (marker TEXT PRIMARY KEY)`)
		return err
	}); err != nil {
		t.Fatalf("create effect_runs: %v", err)
	}
	return d
}

func countingEffect(counter *atomic.Int64) func(*sql.Tx) ([]byte, error) {
	return func(tx *sql.Tx) ([]byte, error) {
		counter.Add(1)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO effect_runs (marker) VALUES (?)`, "effect"); err != nil {
			return nil, err
		}
		return []byte("stored-result"), nil
	}
}

func effectRunCount(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM effect_runs`).Scan(&n); err != nil {
		t.Fatalf("count effect_runs: %v", err)
	}
	return n
}

func TestExecuteOperationParallelRetriesRunEffectOnce(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64
	params := []byte(`{"repo":"r","pr":7}`)

	const n = 16
	type outcome struct {
		res OperationResult
		err error
	}
	outcomes := make(chan outcome, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := d.ExecuteOperation(domain.OperationID("op-parallel"), params, countingEffect(&counter))
			outcomes <- outcome{res: res, err: err}
		}()
	}
	wg.Wait()
	close(outcomes)

	created, duplicate := 0, 0
	for got := range outcomes {
		if got.err != nil {
			t.Fatalf("ExecuteOperation: %v", got.err)
		}
		switch {
		case got.res.Created:
			created++
		case got.res.Duplicate:
			duplicate++
		default:
			t.Fatalf("unexpected result: %+v", got.res)
		}
		if got.res.Receipt.OperationID != domain.OperationID("op-parallel") {
			t.Fatalf("operation id = %q, want op-parallel", got.res.Receipt.OperationID)
		}
	}
	if created != 1 || duplicate != n-1 {
		t.Fatalf("created=%d duplicate=%d, want 1 and %d", created, duplicate, n-1)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("effect calls = %d, want 1", got)
	}
	if got := effectRunCount(t, d); got != 1 {
		t.Fatalf("effect rows = %d, want 1", got)
	}
	if got, err := d.OperationCount(); err != nil || got != 1 {
		t.Fatalf("OperationCount = %d, %v; want 1", got, err)
	}
}

func TestExecuteOperationLostResponseReturnsStoredResult(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64
	params := []byte(`{"amount":42}`)

	first, err := d.ExecuteOperation(domain.OperationID("op-lost"), params, countingEffect(&counter))
	if err != nil || !first.Created {
		t.Fatalf("first ExecuteOperation = %+v, %v; want Created", first, err)
	}
	// The reply never reached the caller. The receipt and effect are committed.
	if first.Receipt.State != domain.OperationSucceeded || first.Receipt.NextAction != domain.NextActionDone {
		t.Fatalf("receipt = %+v, want succeeded/done", first.Receipt)
	}

	retry, err := d.ExecuteOperation(domain.OperationID("op-lost"), params, countingEffect(&counter))
	if err != nil || !retry.Duplicate {
		t.Fatalf("retry = %+v, %v; want Duplicate", retry, err)
	}
	if !bytes.Equal(retry.Receipt.Result, first.Receipt.Result) {
		t.Fatalf("result = %q, want %q", retry.Receipt.Result, first.Receipt.Result)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("effect calls = %d, want 1", got)
	}
	if got := effectRunCount(t, d); got != 1 {
		t.Fatalf("effect rows = %d, want 1", got)
	}
}

func TestExecuteOperationConflictingParameters(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64

	first, err := d.ExecuteOperation(domain.OperationID("op-conflict"), []byte(`{"to":"alice"}`), countingEffect(&counter))
	if err != nil || !first.Created {
		t.Fatalf("first ExecuteOperation = %+v, %v; want Created", first, err)
	}

	second, err := d.ExecuteOperation(domain.OperationID("op-conflict"), []byte(`{"to":"bob"}`), countingEffect(&counter))
	if err != nil {
		t.Fatalf("second ExecuteOperation: %v", err)
	}
	if !second.Conflict || second.Created || second.Duplicate {
		t.Fatalf("second result = %+v, want Conflict", second)
	}
	if second.Receipt.ParamsHash != first.Receipt.ParamsHash {
		t.Fatalf("params hash changed on conflict: %q -> %q", first.Receipt.ParamsHash, second.Receipt.ParamsHash)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("effect calls = %d, want 1", got)
	}
	if got := effectRunCount(t, d); got != 1 {
		t.Fatalf("effect rows = %d, want 1", got)
	}
}

func TestExecuteOperationIndeterminateOutcomeWritesTombstone(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64
	params := []byte(`{"call":"downstream"}`)

	apply := func(tx *sql.Tx) ([]byte, error) {
		counter.Add(1)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO effect_runs (marker) VALUES (?)`, "effect"); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("gateway 5xx: %w", domain.ErrIndeterminateOutcome)
	}
	first, err := d.ExecuteOperation(domain.OperationID("op-unknown"), params, apply)
	if !domain.IsIndeterminate(err) {
		t.Fatalf("err = %v, want indeterminate", err)
	}
	if !first.OutcomeUnknown || first.Created {
		t.Fatalf("first result = %+v, want tombstone", first)
	}
	r := first.Receipt
	if r.State != domain.OperationFailed || r.Reason != domain.TombstoneReasonUnknown || !r.IsTombstone() {
		t.Fatalf("receipt = %+v, want failed/unknown tombstone", r)
	}
	if r.ObservedState() != domain.OperationOutcomeUnknown || r.NextAction != domain.NextActionWaitForHuman {
		t.Fatalf("receipt = %+v, want outcome_unknown/wait_for_human", r)
	}

	retry, err := d.ExecuteOperation(domain.OperationID("op-unknown"), params, countingEffect(&counter))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !retry.OutcomeUnknown || retry.Created || retry.Duplicate {
		t.Fatalf("retry = %+v, want OutcomeUnknown", retry)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("effect calls = %d, want 1 (retry must not re-run)", got)
	}
	if got := effectRunCount(t, d); got != 1 {
		t.Fatalf("effect rows = %d, want 1", got)
	}
}

func TestMarkOutcomeUnknownStopsRetry(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64
	params := []byte(`{"call":"timeout"}`)
	opID := domain.OperationID("op-lost-reply")

	accepted, err := d.BeginOperation(opID, params)
	if err != nil || accepted.State != domain.OperationAccepted {
		t.Fatalf("BeginOperation = %+v, %v; want accepted", accepted, err)
	}

	tomb, err := d.MarkOutcomeUnknown(opID, params)
	if err != nil || !tomb.IsTombstone() || tomb.ObservedState() != domain.OperationOutcomeUnknown {
		t.Fatalf("MarkOutcomeUnknown = %+v, %v; want tombstone", tomb, err)
	}

	retry, err := d.ExecuteOperation(opID, params, countingEffect(&counter))
	if err != nil || !retry.OutcomeUnknown {
		t.Fatalf("retry = %+v, %v; want OutcomeUnknown", retry, err)
	}
	if got := counter.Load(); got != 0 {
		t.Fatalf("effect calls = %d, want 0", got)
	}
	if got := effectRunCount(t, d); got != 0 {
		t.Fatalf("effect rows = %d, want 0", got)
	}

	// A second mark is idempotent and cannot move the tombstone.
	again, err := d.MarkOutcomeUnknown(opID, params)
	if err != nil || again.UpdatedAt != tomb.UpdatedAt {
		t.Fatalf("second mark = %+v, %v; want unchanged", again, err)
	}
}

func TestOperationStateTransitions(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64
	params := []byte(`{"k":"v"}`)

	accepted, err := d.BeginOperation(domain.OperationID("op-states"), params)
	if err != nil || accepted.State != domain.OperationAccepted || accepted.NextAction != domain.NextActionPoll {
		t.Fatalf("BeginOperation = %+v, %v; want accepted/poll", accepted, err)
	}
	running, err := d.StartOperation(domain.OperationID("op-states"))
	if err != nil || running.State != domain.OperationRunning {
		t.Fatalf("StartOperation = %+v, %v; want running", running, err)
	}

	inFlight, err := d.ExecuteOperation(domain.OperationID("op-states"), params, countingEffect(&counter))
	if err != nil || !inFlight.InFlight || inFlight.Receipt.State != domain.OperationRunning {
		t.Fatalf("in-flight retry = %+v, %v; want InFlight", inFlight, err)
	}
	if got := counter.Load(); got != 0 {
		t.Fatalf("effect calls during flight = %d, want 0", got)
	}

	// A fresh id runs the effect and lands in succeeded.
	done, err := d.ExecuteOperation(domain.OperationID("op-done"), params, countingEffect(&counter))
	if err != nil || !done.Created || done.Receipt.State != domain.OperationSucceeded || done.Receipt.NextAction != domain.NextActionDone {
		t.Fatalf("ExecuteOperation = %+v, %v; want succeeded/done", done, err)
	}
	// Starting a completed operation cannot drag it back into flight.
	after, err := d.StartOperation(domain.OperationID("op-done"))
	if err != nil || after.State != domain.OperationSucceeded {
		t.Fatalf("StartOperation after success = %+v, %v; want succeeded", after, err)
	}
}

func TestExecuteOperationDefiniteFailureIsRecorded(t *testing.T) {
	d := newOperationStore(t)
	var counter atomic.Int64
	params := []byte(`{"k":"v"}`)

	fail := func(tx *sql.Tx) ([]byte, error) {
		counter.Add(1)
		return nil, errors.New("downstream refused")
	}
	first, err := d.ExecuteOperation(domain.OperationID("op-failed"), params, fail)
	if err == nil || !strings.Contains(err.Error(), "downstream refused") {
		t.Fatalf("err = %v, want definite failure", err)
	}
	if first.Receipt.State != domain.OperationFailed || first.Receipt.IsTombstone() ||
		first.Receipt.ObservedState() != domain.OperationFailed || first.Receipt.NextAction != domain.NextActionRetry {
		t.Fatalf("receipt = %+v, want failed/retry", first.Receipt)
	}

	retry, err := d.ExecuteOperation(domain.OperationID("op-failed"), params, countingEffect(&counter))
	if err != nil || !retry.Duplicate {
		t.Fatalf("retry = %+v, %v; want Duplicate", retry, err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("effect calls = %d, want 1", got)
	}
}

func TestOperationRejectsInvalidInput(t *testing.T) {
	d := newOperationStore(t)
	apply := func(tx *sql.Tx) ([]byte, error) { return nil, nil }

	if _, err := d.BeginOperation("", []byte(`{}`)); err == nil {
		t.Fatal("BeginOperation accepted empty operation id")
	}
	if _, err := d.ExecuteOperation("", []byte(`{}`), apply); err == nil {
		t.Fatal("ExecuteOperation accepted empty operation id")
	}
	if _, err := d.ExecuteOperation(domain.OperationID("op"), []byte(`{}`), nil); err == nil {
		t.Fatal("ExecuteOperation accepted nil apply")
	}
	if _, err := d.MarkOutcomeUnknown("", []byte(`{}`)); err == nil {
		t.Fatal("MarkOutcomeUnknown accepted empty operation id")
	}
	if _, _, err := d.Operation(""); err == nil {
		t.Fatal("Operation accepted empty operation id")
	}
	if _, err := d.StartOperation(domain.OperationID("op-missing")); err == nil {
		t.Fatal("StartOperation accepted unknown operation id")
	}
}

// allMigrations is the canonical corpus that startup applies. Keep this list
// in sync with migrations/*.sql. TestStartupMigrationsMirror fails when a file
// is added, removed or renamed on either side, so a migration dropped from the
// fixture breaks the build.
var allMigrations = []string{
	"migrations/0001_init.sql",
	"migrations/0002_watches.sql",
	"migrations/0003_obligations.sql",
	"migrations/0004_events.sql",
	"migrations/0005_agent_registry.sql",
	"migrations/0006_control_plane.sql",
	"migrations/0007_registry_columns.sql",
}

// migrationFileExclusions records corpus files that are intentionally not
// applied at startup. An unlisted extra file fails the mirror test.
var migrationFileExclusions = map[string]string{}

// schemaExclusions documents every object the live instance adds after
// migrations. Each entry carries the reason it is absent from the corpus.
// An unlisted runtime object fails TestMigrationFixtureParity.
type schemaExclusions struct {
	tables  map[string]string
	indexes map[string]string
	columns map[string]map[string]string
}

var runtimeSchemaExclusions = schemaExclusions{
	tables: map[string]string{
		"watch_archive": "createWatchArchiveSQL creates the archive table at Open; the migration corpus predates runtime archive creation",
	},
	indexes: map[string]string{
		"events_by_scope":         "ensureEventLog creates the replay index at Open; migration 0004 predates scoped events",
		"watch_archive_closed_at": "createWatchArchiveSQL creates the closed_at index at Open; the migration corpus predates runtime archive creation",
	},
	columns: map[string]map[string]string{
		"events": {
			"scope": "ensureEventLog adds scope at Open; other components still write the 0004 shape",
			"type":  "ensureEventLog adds type at Open; other components still write the 0004 shape",
		},
	},
}

func embeddedMigrationSet(t *testing.T) map[string]struct{} {
	t.Helper()
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

// TestStartupMigrationsMirror checks that startup applies exactly
// allMigrations. A migration missing from the embedded corpus or present
// without an entry in allMigrations fails the build.
func TestStartupMigrationsMirror(t *testing.T) {
	got := embeddedMigrationSet(t)
	want := make(map[string]struct{}, len(allMigrations))
	for _, name := range allMigrations {
		want[name] = struct{}{}
		if _, err := fs.ReadFile(migrationFS, name); err != nil {
			t.Errorf("allMigrations lists %s but it is not embedded: %v", name, err)
		}
	}
	for name := range got {
		if _, ok := want[name]; ok {
			continue
		}
		if reason, ok := migrationFileExclusions[name]; ok {
			t.Logf("corpus file %s excluded from startup: %s", name, reason)
			continue
		}
		t.Errorf("embedded corpus has %s but allMigrations omits it; add it to allMigrations or record an exclusion with a reason", name)
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("allMigrations lists %s but it is not embedded; a migration missing from the fixture must fail the build", name)
		}
	}
}

func schemaTableSet(t *testing.T, db *sql.DB) map[string]struct{} {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	set := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		set[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read tables: %v", err)
	}
	return set
}

func schemaIndexSet(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	set := map[string]string{}
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		set[name] = strings.Join(strings.Fields(body), " ")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read indexes: %v", err)
	}
	return set
}

func tableColumnSigs(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA table_info("`+table+`")`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	sigs := map[string]string{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		sigs[name] = fmt.Sprintf("type=%s notnull=%d default=%q pk=%d", typ, notNull, dflt.String, pk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read table_info(%s): %v", table, err)
	}
	return sigs
}

// TestMigrationFixtureParity applies allMigrations through the same migrate
// path startup uses and compares the result with a freshly opened instance.
// Tables, columns and indexes from the corpus must be present with the same
// definition. Anything the instance adds on top needs an entry in
// runtimeSchemaExclusions with a reason.
func TestMigrationFixtureParity(t *testing.T) {
	ctx := context.Background()

	fixture := fstest.MapFS{}
	for _, name := range allMigrations {
		body, err := fs.ReadFile(migrationFS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fixture[name] = &fstest.MapFile{Data: body}
	}
	fixtureDB, err := openPool(filepath.Join(t.TempDir(), "fixture.db"), 1, driverName)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer fixtureDB.Close()
	if _, err := migrate(ctx, fixtureDB, fixture); err != nil {
		t.Fatalf("apply fixture corpus: %v", err)
	}

	instance, err := Open(filepath.Join(t.TempDir(), "instance.db"))
	if err != nil {
		t.Fatalf("Open instance: %v", err)
	}
	defer instance.Close()

	fixtureTables := schemaTableSet(t, fixtureDB)
	instanceTables := schemaTableSet(t, instance.readDB)
	for name := range fixtureTables {
		if _, ok := instanceTables[name]; !ok {
			t.Errorf("instance schema missing table %s that migrations create", name)
		}
	}
	for name := range instanceTables {
		if _, ok := fixtureTables[name]; ok {
			continue
		}
		if reason, ok := runtimeSchemaExclusions.tables[name]; ok {
			t.Logf("table %s excluded from parity: %s", name, reason)
			continue
		}
		t.Errorf("instance has table %s that migrations do not create; add a migration or an exclusion with a reason", name)
	}

	for name := range fixtureTables {
		wantCols := tableColumnSigs(t, fixtureDB, name)
		gotCols := tableColumnSigs(t, instance.readDB, name)
		for col, wantSig := range wantCols {
			gotSig, ok := gotCols[col]
			if !ok {
				t.Errorf("instance is missing %s.%s from the migration corpus", name, col)
				continue
			}
			if gotSig != wantSig {
				t.Errorf("%s.%s = %q in instance, %q from migrations", name, col, gotSig, wantSig)
			}
		}
		for col := range gotCols {
			if _, ok := wantCols[col]; ok {
				continue
			}
			if reason, ok := runtimeSchemaExclusions.columns[name][col]; ok {
				t.Logf("column %s.%s excluded from parity: %s", name, col, reason)
				continue
			}
			t.Errorf("instance has column %s.%s that migrations do not create; add a migration or an exclusion with a reason", name, col)
		}
	}

	fixtureIndexes := schemaIndexSet(t, fixtureDB)
	instanceIndexes := schemaIndexSet(t, instance.readDB)
	for name, wantSQL := range fixtureIndexes {
		gotSQL, ok := instanceIndexes[name]
		if !ok {
			t.Errorf("instance schema missing index %s that migrations create", name)
			continue
		}
		if gotSQL != wantSQL {
			t.Errorf("index %s = %q in instance, %q from migrations", name, gotSQL, wantSQL)
		}
	}
	for name := range instanceIndexes {
		if _, ok := fixtureIndexes[name]; ok {
			continue
		}
		if reason, ok := runtimeSchemaExclusions.indexes[name]; ok {
			t.Logf("index %s excluded from parity: %s", name, reason)
			continue
		}
		t.Errorf("instance has index %s that migrations do not create; add a migration or an exclusion with a reason", name)
	}
}

// redactionSource names one stored or returned value that must not carry a
// raw secret. read fetches a fresh copy after the write so every table the
// scenarios touch is checked through one path (ADR 0012).
type redactionSource struct {
	prefix     string
	quoted     bool
	fatal      bool
	wantMarker []byte
	read       func() []byte
}

// assertRedacted checks every source for the raw token marker and reports a
// leak with the caller's message. It preserves one check per stored or
// returned value so a secret written through any store path cannot escape.
func assertRedacted(t *testing.T, sources []redactionSource) {
	t.Helper()
	for _, src := range sources {
		got := src.read()
		if bytes.Contains(got, []byte("ghp_")) ||
			(len(src.wantMarker) > 0 && !bytes.Contains(got, src.wantMarker)) {
			if src.quoted {
				if src.fatal {
					t.Fatalf("%s: %q", src.prefix, got)
				}
				t.Errorf("%s: %q", src.prefix, got)
				continue
			}
			if src.fatal {
				t.Fatalf("%s: %s", src.prefix, got)
			}
			t.Errorf("%s: %s", src.prefix, got)
		}
	}
}

// TestStoreRedaction keeps the eight ADR 0012 scenarios in one table. Each
// case names its secret source, writes through a different store path, and
// lists every value that must come back redacted.
func TestStoreRedaction(t *testing.T) {
	const secretToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	scenarios := []struct {
		name  string
		setup func(t *testing.T, d *DB) []redactionSource
	}{
		{
			name: "worker outcome result",
			setup: func(t *testing.T, d *DB) []redactionSource {
				secret := []byte("token " + secretToken)
				if err := d.PublishOutcome(WatchOutcome{Key: "secret-worker", Result: secret}); err != nil {
					t.Fatalf("PublishOutcome: %v", err)
				}
				return []redactionSource{{
					prefix: "delivered outcome kept a raw token",
					read: func() []byte {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						ch, err := d.WatchOutcome(ctx, "secret-worker")
						if err != nil {
							t.Fatalf("WatchOutcome: %v", err)
						}
						select {
						case o := <-ch:
							return o.Result
						case <-ctx.Done():
							t.Fatalf("watcher was not woken: %v", ctx.Err())
							return nil
						}
					},
				}}
			},
		},
		{
			name: "scenario result",
			setup: func(t *testing.T, d *DB) []redactionSource {
				res, err := d.Replay("scenario-secret", nil, []byte("token "+secretToken))
				if err != nil {
					t.Fatalf("Replay: %v", err)
				}
				return []redactionSource{
					{
						prefix: "first result kept a raw token",
						read:   func() []byte { return res.Result },
					},
					{
						prefix: "stored result kept a raw token",
						read: func() []byte {
							stored, ok, err := d.ScenarioResult("scenario-secret")
							if err != nil || !ok {
								t.Fatalf("ScenarioResult: %v, %v; want found", ok, err)
							}
							return stored
						},
					},
				}
			},
		},
		{
			name: "checkpoint state",
			setup: func(t *testing.T, d *DB) []redactionSource {
				secret := []byte("token " + secretToken)
				res, err := d.CheckpointWait("wait-secret", "task-1", 1, secret, nil)
				if err != nil {
					t.Fatalf("CheckpointWait: %v", err)
				}
				if !bytes.Contains(secret, []byte("ghp_")) {
					t.Fatal("test input lost its secret before the write")
				}
				return []redactionSource{
					{
						prefix: "checkpoint state kept a raw token",
						read:   func() []byte { return res.Checkpoint.State },
					},
					{
						prefix: "resumed state kept a raw token",
						read: func() []byte {
							_, state, found, err := d.ResumeWait("wait-secret")
							if err != nil || !found {
								t.Fatalf("ResumeWait: found=%v err=%v; want found", found, err)
							}
							return state
						},
					},
					{
						prefix: "stored state kept a raw token",
						read: func() []byte {
							stored, found, err := d.WaitCheckpoint("wait-secret")
							if err != nil || !found {
								t.Fatalf("WaitCheckpoint: found=%v err=%v; want found", found, err)
							}
							return stored.State
						},
					},
				}
			},
		},
		{
			name: "completion notice payload",
			setup: func(t *testing.T, d *DB) []redactionSource {
				secret := []byte(`{"status":"done","token":"` + secretToken + `"}`)
				var emitted int32
				res, err := d.NotifyCompletion("evt-secret", "task-1", secret, completionEmit(&emitted))
				if err != nil {
					t.Fatalf("NotifyCompletion: %v", err)
				}
				if !bytes.Contains(secret, []byte("ghp_")) {
					t.Fatal("test input lost its secret before the write")
				}
				return []redactionSource{
					{
						prefix: "returned notice kept a raw token",
						read:   func() []byte { return res.Notification.Payload },
					},
					{
						prefix: "stored notice kept a raw token",
						read: func() []byte {
							stored, found, err := d.CompletionNotification("evt-secret")
							if err != nil || !found {
								t.Fatalf("CompletionNotification: found=%v err=%v; want found", found, err)
							}
							return stored.Payload
						},
					},
				}
			},
		},
		{
			name: "task log parts and activity",
			setup: func(t *testing.T, d *DB) []redactionSource {
				secret := []byte("token " + secretToken)
				if err := d.SetLogPart("task-6", LogPartReport, secret); err != nil {
					t.Fatalf("SetLogPart: %v", err)
				}
				if err := d.SetActivity("task-6", string(secret)); err != nil {
					t.Fatalf("SetActivity: %v", err)
				}
				if !bytes.Contains(secret, []byte("ghp_")) {
					t.Fatal("test input lost its secret before the write")
				}
				return []redactionSource{
					{
						prefix: "stored part kept a raw token",
						read: func() []byte {
							parts, err := d.TaskLogParts("task-6")
							if err != nil || len(parts) != 1 {
								t.Fatalf("TaskLogParts = %d, %v; want 1", len(parts), err)
							}
							return parts[0].Payload
						},
					},
					{
						prefix: "stored activity kept a raw token",
						quoted: true,
						read: func() []byte {
							act, found, err := d.Activity("task-6")
							if err != nil || !found {
								t.Fatalf("Activity: found=%v err=%v; want found", found, err)
							}
							return []byte(act.Message)
						},
					},
				}
			},
		},
		{
			name: "queue task and continuation",
			setup: func(t *testing.T, d *DB) []redactionSource {
				secret := []byte(`{"token":"` + secretToken + `"}`)
				if err := d.EnqueueQueueTask("q", "t", secret); err != nil {
					t.Fatalf("EnqueueQueueTask: %v", err)
				}
				task, found, err := d.QueueTask("t")
				if err != nil || !found {
					t.Fatalf("QueueTask: found=%v err=%v", found, err)
				}
				lease, found, err := d.LeaseQueueTask("q", "w", time.Now(), time.Minute)
				if err != nil || !found {
					t.Fatalf("lease: %+v found=%v err=%v", lease, found, err)
				}
				next := &QueueContinuation{Queue: "q2", TaskID: "t2", Payload: secret}
				if _, err := d.CompleteQueueTask("t", "w", lease.FencingToken, next, nil); err != nil {
					t.Fatalf("CompleteQueueTask: %v", err)
				}
				return []redactionSource{
					{
						prefix: "stored task kept a raw token",
						read:   func() []byte { return task.Payload },
					},
					{
						prefix: "lease kept a raw token",
						read:   func() []byte { return lease.Payload },
					},
					{
						prefix: "stored continuation kept a raw token",
						read: func() []byte {
							cont, found, err := d.QueueTask("t2")
							if err != nil || !found {
								t.Fatalf("QueueTask t2: found=%v err=%v", found, err)
							}
							return cont.Payload
						},
					},
				}
			},
		},
		{
			name: "accepted task state",
			setup: func(t *testing.T, d *DB) []redactionSource {
				ctx := context.Background()
				secret := []byte("token " + secretToken)
				if err := d.AcceptTask(ctx, "task-wal-4", secret, func() error { return nil }); err != nil {
					t.Fatalf("AcceptTask: %v", err)
				}
				return []redactionSource{{
					prefix: "stored state kept a raw token",
					read: func() []byte {
						s, found, err := d.AcceptedTask("task-wal-4")
						if err != nil || !found {
							t.Fatalf("AcceptedTask: found=%v err=%v; want found", found, err)
						}
						return s.Payload
					},
				}}
			},
		},
		{
			name: "operation receipt result",
			setup: func(t *testing.T, d *DB) []redactionSource {
				secret := []byte("token=ghp_" + strings.Repeat("a", 36))
				apply := func(tx *sql.Tx) ([]byte, error) { return secret, nil }
				first, err := d.ExecuteOperation(domain.OperationID("op-redact"), []byte(`{}`), apply)
				if err != nil || !first.Created {
					t.Fatalf("ExecuteOperation = %+v, %v; want Created", first, err)
				}
				other, err := d.ExecuteOperation(domain.OperationID("op-other"), []byte(`{}`), apply)
				if err != nil || !other.Created || other.Receipt.OperationID != domain.OperationID("op-other") {
					t.Fatalf("other operation = %+v, %v; want separate Created", other, err)
				}
				if got, err := d.OperationCount(); err != nil || got != 2 {
					t.Fatalf("OperationCount = %d, %v; want 2", got, err)
				}
				return []redactionSource{{
					prefix:     "result not redacted",
					quoted:     true,
					fatal:      true,
					wantMarker: []byte("[REDACTED]"),
					read:       func() []byte { return first.Receipt.Result },
				}}
			},
		},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			d := newOperationStore(t)
			assertRedacted(t, tc.setup(t, d))
		})
	}
}
