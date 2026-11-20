package store

import (
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
