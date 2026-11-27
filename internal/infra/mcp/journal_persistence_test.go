package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

func journalTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open(state.db): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func operationResultFields(t *testing.T, result any) map[string]any {
	t.Helper()
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result): %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", wire, err)
	}
	return fields
}

func TestOpJournalPersistsResultAcrossRestart(t *testing.T) {
	store := journalTestStore(t)
	calls := 0
	fn := func(context.Context) (any, error) {
		calls++
		return app.DeleteResult{Pushed: true, Results: []app.DeletedTask{{ID: "T-A", Status: "deleted"}}}, nil
	}

	firstJournal := newOpJournal(store)
	first, err := firstJournal.run(context.Background(), "op-restart", fn)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if fields := operationResultFields(t, first); fields["status"] != "applied" {
		t.Fatalf("first status = %v", fields["status"])
	}

	// A new process gets a fresh in-memory journal but reads the same database.
	secondJournal := newOpJournal(store)
	second, err := secondJournal.run(context.Background(), "op-restart", fn)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	fields := operationResultFields(t, second)
	if fields["status"] != "duplicate" || fields["operation_id"] != "op-restart" {
		t.Fatalf("replayed result = %+v", fields)
	}
	if calls != 1 {
		t.Fatalf("mutation ran %d times, want 1", calls)
	}
}

func TestOpJournalResultSurvivesEviction(t *testing.T) {
	store := journalTestStore(t)
	journal := newOpJournal(store)
	calls := 0
	keep := func(context.Context) (any, error) {
		calls++
		return app.DeleteResult{Pushed: true}, nil
	}
	if _, err := journal.run(context.Background(), "op-keep", keep); err != nil {
		t.Fatalf("run op-keep: %v", err)
	}

	for i := 0; i < operationJournalLimit+10; i++ {
		id := fmt.Sprintf("op-fill-%d", i)
		if _, err := journal.run(context.Background(), id, func(context.Context) (any, error) {
			return app.DeleteResult{Pushed: true}, nil
		}); err != nil {
			t.Fatalf("run %s: %v", id, err)
		}
	}
	if _, ok := journal.cached("op-keep"); ok {
		t.Fatal("op-keep stayed in the in-memory journal")
	}

	again, err := journal.run(context.Background(), "op-keep", keep)
	if err != nil {
		t.Fatalf("run op-keep after eviction: %v", err)
	}
	if fields := operationResultFields(t, again); fields["status"] != "duplicate" {
		t.Fatalf("evicted result = %+v", fields)
	}
	if calls != 1 {
		t.Fatalf("evicted operation ran %d times, want 1", calls)
	}
}

func TestOpJournalPersistsFailureAcrossRestart(t *testing.T) {
	store := journalTestStore(t)
	want := core.Errorf(core.CodeConflict, "card already changed")
	calls := 0
	fn := func(context.Context) (any, error) {
		calls++
		return nil, want
	}

	firstJournal := newOpJournal(store)
	_, err := firstJournal.run(context.Background(), "op-failed", fn)
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("first error = %v, want %v", err, want)
	}

	secondJournal := newOpJournal(store)
	_, err = secondJournal.run(context.Background(), "op-failed", fn)
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("replayed error = %v, want %v", err, want)
	}
	if code := core.CodeOf(err); code != core.CodeConflict {
		t.Fatalf("replayed error code = %q, want %q", code, core.CodeConflict)
	}
	if calls != 1 {
		t.Fatalf("failed mutation ran %d times, want 1", calls)
	}
}

func TestOpJournalConcurrentDuplicateWithStore(t *testing.T) {
	store := journalTestStore(t)
	journal := newOpJournal(store)
	calls := 0
	release := make(chan struct{})
	fn := func(context.Context) (any, error) {
		calls++
		<-release
		return app.DeleteResult{Pushed: true}, nil
	}

	const workers = 32
	results := make([]any, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = journal.run(context.Background(), "op-concurrent-store", fn)
		}(i)
	}
	close(release)
	wg.Wait()

	if calls != 1 {
		t.Fatalf("mutation ran %d times under concurrency, want 1", calls)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		fields := operationResultFields(t, results[i])
		if fields["status"] != "applied" && fields["status"] != "duplicate" {
			t.Fatalf("worker %d status = %v", i, fields["status"])
		}
	}
}

func TestTaskCreateOperationIDPersistsAcrossServerRestart(t *testing.T) {
	dir := t.TempDir()
	store := journalTestStore(t)
	committer := &stubCommitter{}
	creator := &app.TaskCreator{
		Source: tasksource.NewMarkdown(dir), Workspace: committer, Store: store,
		Root: core.Workspace{Path: dir, Branch: "main"},
	}
	args := createBatchArgs()
	args["operation_id"] = "op-persist-create"

	firstServer := deleteClient(t, Services{TaskCreator: creator})
	first, res := callCreateBatch(t, firstServer, args)
	if res.IsError || first.Status != "applied" || len(first.Created) != 2 {
		t.Fatalf("first create = %+v, result = %+v", first, res)
	}
	commits := committer.calls

	secondServer := deleteClient(t, Services{TaskCreator: creator})
	second, res := callCreateBatch(t, secondServer, args)
	if res.IsError || second.Status != "duplicate" {
		t.Fatalf("repeated create = %+v, result = %+v", second, res)
	}
	if len(second.Created) != len(first.Created) {
		t.Fatalf("replayed card count = %d, want %d", len(second.Created), len(first.Created))
	}
	for i := range first.Created {
		if second.Created[i].ID != first.Created[i].ID {
			t.Fatalf("card %d changed: %+v vs %+v", i, second.Created[i], first.Created[i])
		}
	}
	if committer.calls != commits {
		t.Fatalf("replayed create committed %d more times", committer.calls-commits)
	}
}
