package sqlite

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestSyncTasksPersistsParent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-EPIC", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
		{ID: "T-CHILD", Parent: "T-EPIC", Status: core.TaskAvailable},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 {
		t.Fatalf("ready tasks = %+v", ready)
	}
	byID := make(map[core.TaskID]core.Task, len(ready))
	for _, task := range ready {
		byID[task.ID] = task
	}
	if got := byID["T-CHILD"].Parent; got != "T-EPIC" {
		t.Fatalf("child parent = %q, want T-EPIC", got)
	}
	if got := byID["T-EPIC"].Parent; got != "" {
		t.Fatalf("epic parent = %q, want empty", got)
	}
}

func TestSyncTasksKeepsDoneCardThroughPruneAndStaleSource(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.SyncTasks(ctx, []core.Task{
		{ID: "T-DONE", Status: core.TaskAvailable},
		{ID: "T-OPEN", Status: core.TaskAvailable},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-DONE", string(core.TaskDone)); err != nil {
		t.Fatal(err)
	}

	// The source lists only available cards, so T-DONE is absent. The prune
	// must keep its done row instead of deleting it.
	if err := store.SyncTasks(ctx, []core.Task{{ID: "T-OPEN", Status: core.TaskAvailable}}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, store, "T-DONE"); got != string(core.TaskDone) {
		t.Fatalf("status after prune sync = %q, want %q", got, core.TaskDone)
	}

	// A later sync where a stale frontmatter lists T-DONE as available must
	// not revive it into the ready set, however often that sync runs.
	for i := 0; i < 2; i++ {
		if err := store.SyncTasks(ctx, []core.Task{
			{ID: "T-DONE", Status: core.TaskAvailable},
			{ID: "T-OPEN", Status: core.TaskAvailable},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := statusOf(t, store, "T-DONE"); got != string(core.TaskDone) {
		t.Fatalf("status after stale sync = %q, want %q", got, core.TaskDone)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "T-OPEN" {
		t.Fatalf("ready tasks = %+v, want only T-OPEN", ready)
	}
}

func TestSyncTasksRejectsBadGraphBeforeWriting(t *testing.T) {
	cases := []struct {
		name  string
		tasks []core.Task
	}{
		{
			name: "self dependency",
			tasks: []core.Task{
				{ID: "T-A", DependsOn: []core.TaskID{"T-A"}, Status: core.TaskAvailable},
			},
		},
		{
			name: "cycle",
			tasks: []core.Task{
				{ID: "T-A", DependsOn: []core.TaskID{"T-B"}, Status: core.TaskAvailable},
				{ID: "T-B", DependsOn: []core.TaskID{"T-A"}, Status: core.TaskAvailable},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()

			if err := store.SyncTasks(ctx, []core.Task{{ID: "T-A", Status: core.TaskAvailable}}); err != nil {
				t.Fatal(err)
			}
			err = store.SyncTasks(ctx, tc.tasks)
			if core.CodeOf(err) != core.CodeInvalidInput {
				t.Fatalf("error = %v, want CodeInvalidInput", err)
			}
			ready, err := store.ListReadyTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(ready) != 1 || ready[0].ID != "T-A" {
				t.Fatalf("stored graph changed after a rejected sync: %+v", ready)
			}
		})
	}
}

// storedFingerprint reads the fingerprint from the database, as a second
// process would see it. The bytes are store state, not a local cache.
func storedFingerprint(t *testing.T, store *Store, ctx context.Context) []byte {
	t.Helper()
	var fp []byte
	if err := store.db.QueryRowContext(ctx, `SELECT fingerprint FROM task_sync_state WHERE id = 1`).Scan(&fp); err != nil {
		t.Fatal(err)
	}
	return fp
}

// taskRowsSignature records the stored graph with the timestamps a rewrite
// would refresh. Two stores on one file must produce the same signature when
// only one of them writes.
func taskRowsSignature(t *testing.T, store *Store, ctx context.Context) string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `SELECT id, status, updated_at FROM tasks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, status string
		var updated int64
		if err := rows.Scan(&id, &status, &updated); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s:%s:%d;", id, status, updated)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSyncTasksFingerprintIsSharedAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()

	tasks := []core.Task{{ID: "T-1", Status: core.TaskAvailable}}
	if err := writer.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	want, err := syncFingerprint(tasks)
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]*Store{"writer": writer, "other": other} {
		if got := storedFingerprint(t, store, ctx); !bytes.Equal(got, want) {
			t.Fatalf("%s fingerprint = %x, want %x", name, got, want)
		}
	}
}

func TestSyncTasksSecondStoreDoesNotRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-1", Status: core.TaskAvailable},
		{ID: "T-2", DependsOn: []core.TaskID{"T-1"}, Status: core.TaskAvailable},
	}
	if err := first.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	before := taskRowsSignature(t, second, ctx)

	// A second opener has no local memory of the first sync. The fingerprint
	// in the database must still stop it from rewriting the same graph.
	time.Sleep(2 * time.Millisecond)
	if err := second.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if after := taskRowsSignature(t, second, ctx); after != before {
		t.Fatalf("second store rewrote the graph\nbefore %s\nafter  %s", before, after)
	}
}

func TestListReadyTasksDoesNotMutateCards(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.SyncTasks(ctx, []core.Task{{ID: "T-1", Status: core.TaskAvailable}}); err != nil {
		t.Fatal(err)
	}
	before := taskRowsSignature(t, store, ctx)
	fp := storedFingerprint(t, store, ctx)
	for i := 0; i < 2; i++ {
		if _, err := store.ListReadyTasks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if after := taskRowsSignature(t, store, ctx); after != before {
		t.Fatalf("a read changed the cards\nbefore %s\nafter  %s", before, after)
	}
	if got := storedFingerprint(t, store, ctx); !bytes.Equal(got, fp) {
		t.Fatalf("a read changed the fingerprint: %x, want %x", got, fp)
	}
}

func TestSetTaskStatusInvalidatesFingerprintInEveryStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()

	tasks := []core.Task{{ID: "T-1", Status: core.TaskAvailable}}
	if err := writer.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if err := other.SetTaskStatus(ctx, "T-1", string(core.TaskClaimed)); err != nil {
		t.Fatal(err)
	}
	if fp := storedFingerprint(t, writer, ctx); fp != nil {
		t.Fatalf("fingerprint survived a status write in another store: %x", fp)
	}
	if err := writer.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, writer, "T-1"); got != string(core.TaskClaimed) {
		t.Fatalf("status after a re-sync = %q, want %q", got, core.TaskClaimed)
	}
}

func TestSyncTasksConcurrentStoresWriteOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	stores := make([]*Store, 4)
	for i := range stores {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		stores[i] = store
	}
	ctx := context.Background()
	tasks := []core.Task{{ID: "T-1", Status: core.TaskAvailable}}

	// Prime the schema and store a fingerprint before installing the counter.
	if err := stores[0].SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].db.ExecContext(ctx, `CREATE TABLE task_write_count(n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].db.ExecContext(ctx, `INSERT INTO task_write_count VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	// A rewrite of an existing card is an UPSERT, so it fires UPDATE triggers.
	if _, err := stores[0].db.ExecContext(ctx, `CREATE TRIGGER task_write_counter AFTER UPDATE ON tasks
BEGIN UPDATE task_write_count SET n = n + 1; END`); err != nil {
		t.Fatal(err)
	}
	// Make every opener see a stale graph, then let them race in parallel.
	if _, err := stores[0].db.ExecContext(ctx, `UPDATE task_sync_state SET fingerprint = NULL`); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(stores))
	start := make(chan struct{})
	for _, store := range stores {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			if err := s.SyncTasks(ctx, tasks); err != nil {
				errs <- err
			}
		}(store)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var writes int
	if err := stores[0].db.QueryRowContext(ctx, `SELECT n FROM task_write_count`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("card rewrites across concurrent syncs = %d, want 1", writes)
	}
}
