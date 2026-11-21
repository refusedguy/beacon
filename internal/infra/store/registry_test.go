package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func newRegistryTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func registryExecutor(project, agent string) ExecutorRecord {
	return ExecutorRecord{
		ProjectKey:    project,
		AgentID:       agent,
		Role:          "executor",
		SessionID:     agent,
		NotifyAdapter: "opencode",
		NotifyTarget:  "T5.22",
		Capabilities:  []string{"go", "ci-retry"},
		ConfigID:      "ci=stub",
	}
}

func registryBinding(watch string, revision uint64, project, agent string) WatchBinding {
	return WatchBinding{
		WatchID:    watch,
		Revision:   revision,
		ProjectKey: project,
		AgentID:    agent,
		ConfigID:   "ci=stub",
	}
}

func TestUpsertExecutorIdempotent(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")

	first, err := d.UpsertExecutor(e)
	if err != nil {
		t.Fatalf("first UpsertExecutor: %v", err)
	}
	if !first.Created {
		t.Fatal("first upsert must report Created=true")
	}

	e.NotifyTarget = "T5.22-updated"
	e.Capabilities = []string{"go", "review"}
	second, err := d.UpsertExecutor(e)
	if err != nil {
		t.Fatalf("second UpsertExecutor: %v", err)
	}
	if second.Created {
		t.Fatal("repeat upsert must report Created=false")
	}
	if second.Executor.CreatedAt != first.Executor.CreatedAt {
		t.Fatalf("created_at moved on repeat: %d -> %d",
			first.Executor.CreatedAt, second.Executor.CreatedAt)
	}

	n, err := d.ExecutorCount("project-a")
	if err != nil {
		t.Fatalf("ExecutorCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 executor row, got %d", n)
	}

	stored, found, err := d.Executor("project-a", "session-1")
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	if !found {
		t.Fatal("executor not found after upsert")
	}
	if stored.NotifyTarget != "T5.22-updated" {
		t.Fatalf("mutable field not refreshed: %q", stored.NotifyTarget)
	}
	if stored.Status != RegistryStatusActive {
		t.Fatalf("default status = %q, want %q", stored.Status, RegistryStatusActive)
	}
	if !reflect.DeepEqual(stored.Capabilities, []string{"go", "review"}) {
		t.Fatalf("capabilities = %v", stored.Capabilities)
	}
}

func TestExecutorRuntimeRoundTrip(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")
	e.Runtime = "subagent"
	if _, err := d.UpsertExecutor(e); err != nil {
		t.Fatalf("UpsertExecutor with runtime: %v", err)
	}
	stored, found, err := d.Executor("project-a", "session-1")
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	if !found || stored.Runtime != "subagent" {
		t.Fatalf("runtime = %q, found=%v, want %q", stored.Runtime, found, "subagent")
	}

	// A repeat upsert refreshes the runtime along with the other mutable fields.
	e.Runtime = "opencode"
	if _, err := d.UpsertExecutor(e); err != nil {
		t.Fatalf("repeat UpsertExecutor: %v", err)
	}
	stored, found, err = d.Executor("project-a", "session-1")
	if err != nil {
		t.Fatalf("Executor after repeat: %v", err)
	}
	if !found || stored.Runtime != "opencode" {
		t.Fatalf("runtime after repeat = %q, found=%v, want %q", stored.Runtime, found, "opencode")
	}

	// An omitted runtime is valid and reads back as the empty string.
	e.Runtime = ""
	if _, err := d.UpsertExecutor(e); err != nil {
		t.Fatalf("UpsertExecutor with empty runtime: %v", err)
	}
	stored, found, err = d.Executor("project-a", "session-1")
	if err != nil {
		t.Fatalf("Executor after empty runtime: %v", err)
	}
	if !found || stored.Runtime != "" {
		t.Fatalf("empty runtime = %q, found=%v", stored.Runtime, found)
	}
}

func TestUpsertExecutorSeparateKeys(t *testing.T) {
	d := newRegistryTestDB(t)

	for _, e := range []ExecutorRecord{
		registryExecutor("project-a", "session-1"),
		registryExecutor("project-a", "session-2"),
		registryExecutor("project-b", "session-1"),
	} {
		if _, err := d.UpsertExecutor(e); err != nil {
			t.Fatalf("UpsertExecutor(%s, %s): %v", e.ProjectKey, e.AgentID, err)
		}
	}

	for project, want := range map[string]int{"project-a": 2, "project-b": 1} {
		n, err := d.ExecutorCount(project)
		if err != nil {
			t.Fatalf("ExecutorCount(%s): %v", project, err)
		}
		if n != want {
			t.Fatalf("%s: want %d rows, got %d", project, want, n)
		}
	}
}

func TestUpsertExecutorRejectsEmptyKeys(t *testing.T) {
	d := newRegistryTestDB(t)

	for _, e := range []ExecutorRecord{
		registryExecutor("", "session-1"),
		registryExecutor("project-a", ""),
	} {
		if _, err := d.UpsertExecutor(e); err == nil {
			t.Fatalf("UpsertExecutor(%q, %q) must fail", e.ProjectKey, e.AgentID)
		}
	}
}

func TestBindWatchIdempotent(t *testing.T) {
	d := newRegistryTestDB(t)
	b := registryBinding("watch-1", 7, "project-a", "session-1")

	first, err := d.BindWatch(b)
	if err != nil {
		t.Fatalf("first BindWatch: %v", err)
	}
	if !first.Created {
		t.Fatal("first bind must report Created=true")
	}
	second, err := d.BindWatch(b)
	if err != nil {
		t.Fatalf("second BindWatch: %v", err)
	}
	if second.Created {
		t.Fatal("repeat bind must report Created=false")
	}

	n, err := d.WatchBindingCount("watch-1")
	if err != nil {
		t.Fatalf("WatchBindingCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 binding row, got %d", n)
	}

	newRev := registryBinding("watch-1", 8, "project-a", "session-1")
	if _, err := d.BindWatch(newRev); err != nil {
		t.Fatalf("BindWatch(new revision): %v", err)
	}
	n, err = d.WatchBindingCount("watch-1")
	if err != nil {
		t.Fatalf("WatchBindingCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("new revision must add a row, want 2 got %d", n)
	}
}

func TestBindWatchRejectsEmptyKeys(t *testing.T) {
	d := newRegistryTestDB(t)

	for _, b := range []WatchBinding{
		registryBinding("watch-1", 1, "", "session-1"),
		registryBinding("watch-1", 1, "project-a", ""),
		registryBinding("", 1, "project-a", "session-1"),
	} {
		if _, err := d.BindWatch(b); err == nil {
			t.Fatalf("BindWatch(%+v) must fail", b)
		}
	}
}

func TestRegisterExecutorIdempotent(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")
	b := registryBinding("watch-1", 3, "project-a", "session-1")

	first, err := d.RegisterExecutor(e, b)
	if err != nil {
		t.Fatalf("first RegisterExecutor: %v", err)
	}
	if !first.ExecutorNew || !first.BindingNew {
		t.Fatal("first register must create both rows")
	}
	second, err := d.RegisterExecutor(e, b)
	if err != nil {
		t.Fatalf("second RegisterExecutor: %v", err)
	}
	if second.ExecutorNew || second.BindingNew {
		t.Fatal("repeat register must not create rows")
	}
	if second.Executor.UpdatedAt < first.Executor.UpdatedAt {
		t.Fatal("updated_at moved backwards")
	}

	executors, err := d.ExecutorCount("project-a")
	if err != nil {
		t.Fatalf("ExecutorCount: %v", err)
	}
	bindings, err := d.WatchBindingCount("watch-1")
	if err != nil {
		t.Fatalf("WatchBindingCount: %v", err)
	}
	if executors != 1 || bindings != 1 {
		t.Fatalf("repeat register: want 1 executor and 1 binding, got %d and %d",
			executors, bindings)
	}

	stored, found, err := d.WatchBinding("watch-1", 3)
	if err != nil {
		t.Fatalf("WatchBinding: %v", err)
	}
	if !found || stored.Revision != 3 {
		t.Fatalf("binding = %+v, found=%v", stored, found)
	}
}

func TestRegisterExecutorDifferentSessionAddsRow(t *testing.T) {
	d := newRegistryTestDB(t)

	for _, agent := range []string{"session-1", "session-1", "session-2"} {
		e := registryExecutor("project-a", agent)
		b := registryBinding("watch-1", 3, "project-a", agent)
		if _, err := d.RegisterExecutor(e, b); err != nil {
			t.Fatalf("RegisterExecutor(%s): %v", agent, err)
		}
	}

	n, err := d.ExecutorCount("project-a")
	if err != nil {
		t.Fatalf("ExecutorCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("different sessionID must add a row, want 2 got %d", n)
	}
	list, err := d.Executors("project-a")
	if err != nil {
		t.Fatalf("Executors: %v", err)
	}
	if len(list) != 2 || list[0].AgentID != "session-1" || list[1].AgentID != "session-2" {
		t.Fatalf("unexpected executor list: %+v", list)
	}
}

func TestRegisterExecutorRejectsMismatchedBinding(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")
	b := registryBinding("watch-1", 1, "project-b", "session-1")
	if _, err := d.RegisterExecutor(e, b); err == nil {
		t.Fatal("binding from another project must be rejected")
	}
}

func TestRegisterExecutorConcurrent(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")
	b := registryBinding("watch-1", 5, "project-a", "session-1")

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.RegisterExecutor(e, b); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RegisterExecutor: %v", err)
	}

	executors, err := d.ExecutorCount("project-a")
	if err != nil {
		t.Fatalf("ExecutorCount: %v", err)
	}
	bindings, err := d.WatchBindingCount("watch-1")
	if err != nil {
		t.Fatalf("WatchBindingCount: %v", err)
	}
	if executors != 1 || bindings != 1 {
		t.Fatalf("concurrent calls broke uniqueness: %d executors, %d bindings",
			executors, bindings)
	}
}

func TestProjectIsolation(t *testing.T) {
	d := newRegistryTestDB(t)
	for _, project := range []string{"project-a", "project-b"} {
		e := registryExecutor(project, "session-1")
		b := registryBinding("watch-1", 1, project, "session-1")
		if _, err := d.RegisterExecutor(e, b); err != nil {
			t.Fatalf("RegisterExecutor(%s): %v", project, err)
		}
	}

	listA, err := d.Executors("project-a")
	if err != nil {
		t.Fatalf("Executors(project-a): %v", err)
	}
	if len(listA) != 1 || listA[0].ProjectKey != "project-a" {
		t.Fatalf("project-a sees foreign rows: %+v", listA)
	}
	if _, err := d.Executors(""); err == nil {
		t.Fatal("empty project key must be rejected")
	}
	if _, _, err := d.Executor("", "session-1"); err == nil {
		t.Fatal("empty project key must be rejected on single read")
	}
}

func TestCapabilitiesEmptyRoundTrip(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")
	e.Capabilities = nil
	if _, err := d.UpsertExecutor(e); err != nil {
		t.Fatalf("UpsertExecutor: %v", err)
	}
	stored, found, err := d.Executor("project-a", "session-1")
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	if !found || len(stored.Capabilities) != 0 {
		t.Fatalf("empty capabilities = %v, found=%v", stored.Capabilities, found)
	}
}

func TestRegistryReopenKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := registryExecutor("project-a", "session-1")
	b := registryBinding("watch-1", 2, "project-a", "session-1")
	if _, err := d.RegisterExecutor(e, b); err != nil {
		t.Fatalf("RegisterExecutor: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	stored, found, err := reopened.Executor("project-a", "session-1")
	if err != nil {
		t.Fatalf("Executor after reopen: %v", err)
	}
	if !found || stored.AgentID != "session-1" {
		t.Fatalf("row lost after reopen: %+v, found=%v", stored, found)
	}
	if _, found, err := reopened.WatchBinding("watch-1", 2); err != nil || !found {
		t.Fatalf("WatchBinding after reopen: found=%v, err=%v", found, err)
	}
}

func TestRegisterResultIsUseful(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "session-1")
	b := registryBinding("watch-1", 9, "project-a", "session-1")
	res, err := d.RegisterExecutor(e, b)
	if err != nil {
		t.Fatalf("RegisterExecutor: %v", err)
	}
	if res.Executor.SessionID != "session-1" || res.Binding.WatchID != "watch-1" {
		t.Fatalf("unexpected result: %+v", fmt.Sprintf("%v", res))
	}
	if res.Executor.CreatedAt == 0 || res.Binding.CreatedAt == 0 {
		t.Fatal("timestamps must be populated")
	}
}

func TestRegistrySchemaHasSyncColumns(t *testing.T) {
	d := newRegistryTestDB(t)
	// A write runs the on-first-use migration; migration 0005 declares the
	// same columns for a store created from the SQL corpus.
	if _, err := d.UpsertExecutor(registryExecutor("project-a", "session-1")); err != nil {
		t.Fatalf("UpsertExecutor: %v", err)
	}
	for _, name := range []string{"last_seen", "worktree"} {
		has, err := registryHasColumn(d.readDB, name)
		if err != nil {
			t.Fatalf("registryHasColumn(%s): %v", name, err)
		}
		if !has {
			t.Errorf("agent_registry has no %s column", name)
		}
	}
}

func TestEnsureRegistrySchemaAddsSyncColumnsToOldTable(t *testing.T) {
	d := newRegistryTestDB(t)
	// Recreate the migration 0005 shape without the sync columns.
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DROP TABLE agent_registry`); err != nil {
			return err
		}
		_, err := tx.Exec(`
			CREATE TABLE agent_registry (
			  project_key TEXT NOT NULL,
			  agent_id TEXT NOT NULL,
			  role TEXT NOT NULL,
			  runtime TEXT NOT NULL DEFAULT '',
			  session_id TEXT NOT NULL DEFAULT '',
			  status TEXT NOT NULL DEFAULT 'active',
			  created_at INTEGER NOT NULL,
			  updated_at INTEGER NOT NULL,
			  PRIMARY KEY (project_key, agent_id)
			)`)
		return err
	})
	if err != nil {
		t.Fatalf("recreate old table: %v", err)
	}
	for _, name := range []string{"last_seen", "worktree"} {
		has, err := registryHasColumn(d.readDB, name)
		if err != nil {
			t.Fatalf("registryHasColumn(%s): %v", name, err)
		}
		if has {
			t.Fatalf("old table already has %s", name)
		}
	}

	if err := d.EnsureRegistrySchema(); err != nil {
		t.Fatalf("EnsureRegistrySchema: %v", err)
	}
	// A second call must not try to add the columns again.
	if err := d.EnsureRegistrySchema(); err != nil {
		t.Fatalf("second EnsureRegistrySchema: %v", err)
	}
	for _, name := range []string{"last_seen", "worktree"} {
		has, err := registryHasColumn(d.readDB, name)
		if err != nil {
			t.Fatalf("registryHasColumn(%s) after migration: %v", name, err)
		}
		if !has {
			t.Errorf("EnsureRegistrySchema did not add %s", name)
		}
	}
}

func TestSyncExecutorWritesObservationAndStatus(t *testing.T) {
	d := newRegistryTestDB(t)
	if _, err := d.UpsertExecutor(registryExecutor("project-a", "ses-live")); err != nil {
		t.Fatalf("UpsertExecutor: %v", err)
	}

	firstSeen := int64(1_700_000_000_000_000_000)
	if err := d.SyncExecutor("project-a", "ses-live", RegistryStatusStale, "/work/t5.55", firstSeen); err != nil {
		t.Fatalf("SyncExecutor stale: %v", err)
	}
	stored, found, err := d.Executor("project-a", "ses-live")
	if err != nil {
		t.Fatalf("Executor after stale sync: %v", err)
	}
	if !found {
		t.Fatal("executor lost after sync")
	}
	if stored.Status != RegistryStatusStale || stored.LastSeen != firstSeen || stored.Worktree != "/work/t5.55" {
		t.Fatalf("stale row = %+v", stored)
	}

	secondSeen := firstSeen + 60_000_000_000
	if err := d.SyncExecutor("project-a", "ses-live", RegistryStatusActive, "/work/t5.55b", secondSeen); err != nil {
		t.Fatalf("SyncExecutor active: %v", err)
	}
	stored, found, err = d.Executor("project-a", "ses-live")
	if err != nil {
		t.Fatalf("Executor after active sync: %v", err)
	}
	if !found {
		t.Fatal("executor lost after second sync")
	}
	if stored.Status != RegistryStatusActive || stored.LastSeen != secondSeen || stored.Worktree != "/work/t5.55b" {
		t.Fatalf("active row = %+v", stored)
	}
}

func TestSyncExecutorKeepsObservationOnRegister(t *testing.T) {
	d := newRegistryTestDB(t)
	e := registryExecutor("project-a", "ses-live")
	if _, err := d.UpsertExecutor(e); err != nil {
		t.Fatalf("UpsertExecutor: %v", err)
	}

	seen := int64(1_700_000_100_000_000_000)
	if err := d.SyncExecutor("project-a", "ses-live", RegistryStatusActive, "/work/t5.55", seen); err != nil {
		t.Fatalf("SyncExecutor: %v", err)
	}
	e.NotifyTarget = "T5.55-updated"
	if _, err := d.UpsertExecutor(e); err != nil {
		t.Fatalf("repeat UpsertExecutor: %v", err)
	}
	stored, found, err := d.Executor("project-a", "ses-live")
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	if !found {
		t.Fatal("executor lost after register")
	}
	if stored.LastSeen != seen || stored.Worktree != "/work/t5.55" {
		t.Fatalf("register erased sync observation: %+v", stored)
	}
	if stored.NotifyTarget != "T5.55-updated" {
		t.Fatalf("mutable registration field not refreshed: %q", stored.NotifyTarget)
	}
}

func TestSyncExecutorRejectsEmptyKeys(t *testing.T) {
	d := newRegistryTestDB(t)
	for _, tc := range []struct{ project, agent string }{
		{"", "ses-1"},
		{"project-a", ""},
	} {
		if err := d.SyncExecutor(tc.project, tc.agent, RegistryStatusStale, "", 1); err == nil {
			t.Errorf("SyncExecutor(%q, %q): want an error", tc.project, tc.agent)
		}
	}
}
