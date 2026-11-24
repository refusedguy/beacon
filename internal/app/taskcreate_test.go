package app

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// core.TaskSource now requires Create, so the shared fake defines it here.
func (f *fakeSource) Create(_ context.Context, task core.Task) (core.Task, error) {
	created := task
	created.SourceRef = "docs/tasks/" + string(task.ID) + ".md"
	f.log.add("create:" + string(task.ID))
	return created, nil
}

type recordingSource struct {
	*fakeSource
	created core.Task
}

func (r *recordingSource) Create(ctx context.Context, task core.Task) (core.Task, error) {
	created, err := r.fakeSource.Create(ctx, task)
	r.created = created
	return created, err
}

type failingSource struct {
	*fakeSource
	err error
}

func (f *failingSource) Create(context.Context, core.Task) (core.Task, error) {
	return core.Task{}, f.err
}

func TestTaskCreatorCreatesAndCommits(t *testing.T) {
	src := &recordingSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	got, err := creator.Create(context.Background(), CreateTaskInput{
		Title:    " Add orchestrator tool ",
		Body:     "Create cards in one call.",
		Scope:    []string{"internal/app/", "internal/**/*.go", ""},
		Depends:  []string{" E2E-03 ", ""},
		Priority: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^T-[0-9]{8}-[0-9a-f]{8}$`).MatchString(got.ID) {
		t.Fatalf("id = %q, want date plus short hex", got.ID)
	}
	if got.Path != "docs/tasks/"+got.ID+".md" || !got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if src.created.Title != "Add orchestrator tool" || src.created.Priority != 11 {
		t.Fatalf("created task = %+v", src.created)
	}
	if src.created.Status != core.TaskAvailable {
		t.Fatalf("status = %q", src.created.Status)
	}
	if len(src.created.Scope) != 2 || !src.created.Scope[0].CoversPath("internal/app/taskcreate.go") {
		t.Fatalf("scope = %v", src.created.Scope)
	}
	if !src.created.Scope[1].CoversPath("internal/infra/mcp/server.go") {
		t.Fatalf("glob scope = %v", src.created.Scope)
	}
	if len(src.created.DependsOn) != 1 || src.created.DependsOn[0] != "E2E-03" {
		t.Fatalf("depends = %v", src.created.DependsOn)
	}
	if len(ws.commits) != 1 || ws.commits[0].pathspec != "docs/tasks" ||
		ws.commits[0].message != "beacon: create task "+got.ID {
		t.Fatalf("commits = %+v", ws.commits)
	}
	if len(src.log.items) != 1 || src.log.items[0] != "create:"+got.ID {
		t.Fatalf("source log = %v", src.log.items)
	}
}

func TestTaskCreatorRebasesBeforePublishing(t *testing.T) {
	src := &recordingSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{Title: "Card"}); err != nil {
		t.Fatal(err)
	}
	if len(ws.rebases) != 1 || ws.rebases[0] != "main" {
		t.Fatalf("rebases = %v, want one rebase onto main", ws.rebases)
	}
	if len(src.log.items) != 1 || src.log.items[0] != "create:"+string(src.created.ID) {
		t.Fatalf("source log = %v", src.log.items)
	}
	if len(ws.log.items) != 2 || ws.log.items[0] != "rebase:main" || ws.log.items[1] != "commit:docs/tasks" {
		t.Fatalf("workspace call order = %v", ws.log.items)
	}
}

func TestTaskCreatorRetriesPushAfterNonFastForward(t *testing.T) {
	src := &recordingSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}, rejectFirst: true}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Base: "main", Branch: "work"}}

	got, err := creator.Create(context.Background(), CreateTaskInput{Title: "Retry card"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed {
		t.Fatalf("result = %+v, want pushed", got)
	}
	if ws.pushes != 2 {
		t.Fatalf("push attempts = %d, want 2", ws.pushes)
	}
	if len(ws.rebases) != 2 || ws.rebases[0] != "main" || ws.rebases[1] != "main" {
		t.Fatalf("rebases = %v, want a pre-push and a retry rebase onto main", ws.rebases)
	}
}

func TestTaskCreatorStopsWhenRebaseFails(t *testing.T) {
	src := &recordingSource{fakeSource: &fakeSource{log: &callLog{}}}
	want := errors.New("fetch failed")
	ws := &fakeWorkspace{log: &callLog{}, rebaseBaseErr: want}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{Title: "Card"}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if ws.pushes != 0 {
		t.Fatalf("push attempts = %d, want 0 after a failed rebase", ws.pushes)
	}
}

func TestTaskCreatorRejectsEmptyTitle(t *testing.T) {
	src := &recordingSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws}

	_, err := creator.Create(context.Background(), CreateTaskInput{Title: "  "})
	if core.CodeOf(err) != core.CodeInvalidInput {
		t.Fatalf("error = %v", err)
	}
	if len(src.log.items) != 0 || len(ws.commits) != 0 {
		t.Fatal("empty title still wrote a card or commit")
	}
}

func TestTaskCreatorStopsOnSourceError(t *testing.T) {
	want := errors.New("source down")
	src := &failingSource{fakeSource: &fakeSource{log: &callLog{}}, err: want}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws}

	if _, err := creator.Create(context.Background(), CreateTaskInput{Title: "Card"}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if len(ws.commits) != 0 {
		t.Fatal("source failure still committed")
	}
}

type recordingEpicSource struct {
	*fakeSource
	created []core.Task
	failID  core.TaskID
}

func (r *recordingEpicSource) Create(ctx context.Context, task core.Task) (core.Task, error) {
	if task.ID == r.failID {
		return core.Task{}, errors.New("write failed")
	}
	created, err := r.fakeSource.Create(ctx, task)
	if err == nil {
		r.created = append(r.created, created)
	}
	return created, err
}

type batchEpicSource struct {
	*recordingEpicSource
	calls int
}

func (b *batchEpicSource) CreateMany(ctx context.Context, tasks []core.Task) ([]core.Task, error) {
	b.calls++
	out := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		created, err := b.recordingEpicSource.Create(ctx, task)
		if err != nil {
			return out, err
		}
		out = append(out, created)
	}
	return out, nil
}

type graphStore struct {
	calls int
	tasks []core.Task
	err   error
}

func (g *graphStore) SyncTasks(_ context.Context, tasks []core.Task) error {
	g.calls++
	if g.err != nil {
		return g.err
	}
	g.tasks = tasks
	return nil
}

func epicPlan() []core.ChildSpec {
	return []core.ChildSpec{
		{Title: "Second child", Scope: []core.ScopePath{core.ScopeFile("b.go")}, Depends: []core.TaskID{"EXT-1"}, Priority: 5},
		{Title: "First child", Scope: []core.ScopePath{core.ScopeFile("a.go")}, Priority: 9},
	}
}

func TestTaskCreatorExpandsEpic(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	store := &graphStore{}
	creator := &TaskCreator{Source: src, Workspace: ws, Store: store,
		Root: core.Workspace{Path: "/root", Branch: "main"}}

	got, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic,
		RequireReview: core.ReviewModeHuman, Plan: epicPlan(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.created) != 3 {
		t.Fatalf("created cards = %d, want epic plus two children", len(src.created))
	}
	children, epic := src.created[:2], src.created[2]
	if children[0].ID != "T-EPIC-01" || children[0].Title != "Second child" ||
		children[1].ID != "T-EPIC-02" || children[1].Title != "First child" {
		t.Fatalf("children = %+v", children)
	}
	if children[0].BaseBranch != "beacon/T-EPIC" || children[1].BaseBranch != "beacon/T-EPIC" {
		t.Fatalf("child base branches = %q, %q", children[0].BaseBranch, children[1].BaseBranch)
	}
	if len(children[0].DependsOn) != 1 || children[0].DependsOn[0] != "EXT-1" {
		t.Fatalf("child depends = %v", children[0].DependsOn)
	}
	if !epic.DependsOnID("T-EPIC-01") || !epic.DependsOnID("T-EPIC-02") {
		t.Fatalf("epic depends = %v", epic.DependsOn)
	}
	if epic.Kind != core.TaskKindEpic || epic.RequireReview != core.ReviewModeHuman {
		t.Fatalf("epic = %+v", epic)
	}
	if got.ID != "T-EPIC" || len(got.Children) != 2 || len(got.IDs) != 3 || !got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if store.calls != 1 || len(store.tasks) != 3 {
		t.Fatalf("sync calls = %d, tasks = %d", store.calls, len(store.tasks))
	}
	if len(ws.commits) != 1 {
		t.Fatalf("commits = %d, want one for the whole graph", len(ws.commits))
	}
}

func TestTaskCreatorSyncsFullAvailableSet(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{
		log: &callLog{},
		tasks: []core.Task{
			{ID: "T-OLD", Title: "Existing card", Status: core.TaskAvailable},
		},
	}}
	ws := &fakeWorkspace{log: &callLog{}}
	store := &graphStore{}
	creator := &TaskCreator{Source: src, Workspace: ws, Store: store,
		Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{Title: "New card"}); err != nil {
		t.Fatal(err)
	}
	seen := make(map[core.TaskID]bool, len(store.tasks))
	for _, task := range store.tasks {
		seen[task.ID] = true
	}
	if store.calls != 1 || len(seen) != 2 || !seen["T-OLD"] {
		t.Fatalf("sync calls = %d, synced = %+v, want the old card plus the new one",
			store.calls, store.tasks)
	}
}

func TestTaskCreatorEpicIsIdempotent(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}
	in := CreateTaskInput{Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan()}

	first, err := creator.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := creator.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.created) != 3 {
		t.Fatalf("created cards = %d, want 3 after a repeated create", len(src.created))
	}
	if second.ID != first.ID || len(second.IDs) != len(first.IDs) || len(second.Children) != len(first.Children) {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
	for i := range first.IDs {
		if second.IDs[i] != first.IDs[i] {
			t.Fatalf("ids changed: %v vs %v", first.IDs, second.IDs)
		}
	}
	if len(ws.commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(ws.commits))
	}
}

func TestTaskCreatorFindsExistingEpicFromSource(t *testing.T) {
	plan := epicPlan()
	src := &recordingEpicSource{fakeSource: &fakeSource{
		log: &callLog{},
		tasks: []core.Task{{
			ID: "T-EPIC", Title: "Epic", Kind: core.TaskKindEpic,
			Plan: plan, Status: core.TaskAvailable,
		}},
	}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	got, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.created) != 0 || len(ws.commits) != 0 {
		t.Fatal("an existing epic still wrote cards or committed")
	}
	if got.ID != "T-EPIC" || len(got.IDs) != 3 {
		t.Fatalf("result = %+v", got)
	}
}

func TestTaskCreatorRollsBackEpicOnWriteFailure(t *testing.T) {
	src := &recordingEpicSource{
		fakeSource: &fakeSource{log: &callLog{}},
		failID:     "T-EPIC-02",
	}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan(),
	}); err == nil {
		t.Fatal("write failure did not surface")
	}
	if len(src.removed) != 1 || src.removed[0] != "T-EPIC-01" {
		t.Fatalf("removed = %v, want the child written before the failure", src.removed)
	}
	if len(ws.commits) != 0 {
		t.Fatal("write failure still committed")
	}
}

func TestTaskCreatorRollsBackEpicOnSyncFailure(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	want := errors.New("sync failed")
	creator := &TaskCreator{Source: src, Workspace: ws, Store: &graphStore{err: want},
		Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan(),
	}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if len(src.removed) != 3 || len(ws.commits) != 0 {
		t.Fatalf("removed = %v, commits = %d", src.removed, len(ws.commits))
	}
}

func TestTaskCreatorCreatesChildrenInOneOperation(t *testing.T) {
	src := &batchEpicSource{recordingEpicSource: &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan(),
	}); err != nil {
		t.Fatal(err)
	}
	if src.calls != 1 {
		t.Fatalf("batch calls = %d, want one call for the whole graph", src.calls)
	}
}

func TestTaskCreatorDerivesStableEpicID(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}
	in := CreateTaskInput{Title: "Epic", Kind: core.TaskKindEpic, Plan: epicPlan()}

	first, err := creator.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	src2 := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	creator2 := &TaskCreator{Source: src2, Workspace: &fakeWorkspace{log: &callLog{}},
		Root: core.Workspace{Path: "/root", Branch: "main"}}
	second, err := creator2.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !regexp.MustCompile(`^T-[0-9]{8}-[0-9a-f]{8}$`).MatchString(first.ID) {
		t.Fatalf("first = %q, second = %q", first.ID, second.ID)
	}
}

func TestTaskCreatorCreateBatch(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	got, err := creator.CreateBatch(context.Background(), []CreateTaskInput{
		{Title: "First", Body: "One", Scope: []string{"a.go"}, Priority: 3},
		{Title: "Second", Depends: []string{"@1", "EXT-9"}},
		{Title: "Third", Depends: []string{"@1", "@2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("results = %d, want 3", len(got))
	}
	if len(src.created) != 3 || len(ws.commits) != 3 {
		t.Fatalf("created = %d, commits = %d", len(src.created), len(ws.commits))
	}
	for i, item := range got {
		if !regexp.MustCompile(`^T-[0-9]{8}-[0-9a-f]{8}$`).MatchString(item.ID) ||
			item.Path != "docs/tasks/"+item.ID+".md" || !item.Pushed {
			t.Fatalf("result %d = %+v", i, item)
		}
		if string(src.created[i].ID) != item.ID {
			t.Fatalf("created %d = %q, result = %q", i, src.created[i].ID, item.ID)
		}
	}
	if !src.created[1].DependsOnID(core.TaskID(got[0].ID)) ||
		!src.created[1].DependsOnID("EXT-9") {
		t.Fatalf("second depends = %v", src.created[1].DependsOn)
	}
	if !src.created[2].DependsOnID(core.TaskID(got[0].ID)) ||
		!src.created[2].DependsOnID(core.TaskID(got[1].ID)) {
		t.Fatalf("third depends = %v", src.created[2].DependsOn)
	}
}

func TestTaskCreatorCreateBatchIsIdempotent(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}
	in := []CreateTaskInput{
		{Title: "Alpha", Body: "One"},
		{Title: "Beta", Depends: []string{"@1"}},
		{Title: "Gamma"},
	}

	first, err := creator.CreateBatch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := creator.CreateBatch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Fatalf("second = %d cards, first = %d", len(second), len(first))
	}
	for i := range first {
		if second[i].ID != first[i].ID || second[i].Path != first[i].Path {
			t.Fatalf("card %d changed: %+v vs %+v", i, first[i], second[i])
		}
	}
	if len(src.created) != 3 || len(src.log.items) != 3 || len(ws.commits) != 3 {
		t.Fatalf("repeated batch wrote cards = %d, creates = %d, commits = %d",
			len(src.created), len(src.log.items), len(ws.commits))
	}
}

func TestTaskCreatorCreateBatchRejectsForwardPlaceholder(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	_, err := creator.CreateBatch(context.Background(), []CreateTaskInput{
		{Title: "First", Depends: []string{"@2"}},
		{Title: "Second"},
	})
	if core.CodeOf(err) != core.CodeInvalidInput {
		t.Fatalf("error = %v", err)
	}
	if len(src.log.items) != 0 || len(ws.commits) != 0 {
		t.Fatal("invalid placeholder still wrote a card or committed")
	}
}

type failOnIDSource struct {
	*fakeSource
	failID core.TaskID
}

func (f *failOnIDSource) Create(ctx context.Context, task core.Task) (core.Task, error) {
	if task.ID == f.failID {
		return core.Task{}, errors.New("write failed")
	}
	return f.fakeSource.Create(ctx, task)
}

func TestTaskCreatorCreateBatchRollsBackOnFailure(t *testing.T) {
	src := &failOnIDSource{fakeSource: &fakeSource{log: &callLog{}}, failID: "T-BATCH-B"}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	_, err := creator.CreateBatch(context.Background(), []CreateTaskInput{
		{ID: "T-BATCH-A", Title: "A"},
		{ID: "T-BATCH-B", Title: "B"},
		{ID: "T-BATCH-C", Title: "C"},
	})
	if err == nil {
		t.Fatal("write failure did not surface")
	}
	if len(src.removed) != 1 || src.removed[0] != "T-BATCH-A" {
		t.Fatalf("removed = %v, want the card written before the failure", src.removed)
	}
	// The first card publishes before the later card fails, so one commit is
	// expected. Rollback removes its file but cannot undo that commit.
	if len(ws.commits) != 1 {
		t.Fatalf("commits = %d, want the first card's publish only", len(ws.commits))
	}
}
