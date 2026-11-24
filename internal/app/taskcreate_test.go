package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestTaskCreatorCopiesChildBodies(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}
	plan := []core.ChildSpec{
		{Title: "First child", Body: "  Do the first thing.  "},
		{Title: "Second child", Body: "Do the second thing."},
	}

	if _, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: plan,
	}); err != nil {
		t.Fatal(err)
	}
	if len(src.created) != 3 {
		t.Fatalf("created cards = %d, want epic plus two children", len(src.created))
	}
	if got := src.created[0].Body; got != "Do the first thing." {
		t.Fatalf("first child body = %q", got)
	}
	if got := src.created[1].Body; got != "Do the second thing." {
		t.Fatalf("second child body = %q", got)
	}
	if got := src.created[2].Body; got != "" {
		t.Fatalf("epic body = %q, want empty", got)
	}
}

func TestTaskCreatorChildBodyDoesNotChangeIDs(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}
	in := CreateTaskInput{Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic,
		Plan: []core.ChildSpec{{Title: "Child", Body: "First instruction."}}}

	first, err := creator.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Plan[0].Body = "Changed instruction."
	second, err := creator.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != first.ID || len(second.IDs) != len(first.IDs) || len(second.Children) != len(first.Children) {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
	for i := range first.IDs {
		if second.IDs[i] != first.IDs[i] {
			t.Fatalf("ids changed: %v vs %v", first.IDs, second.IDs)
		}
	}
	if len(src.created) != 2 || len(ws.commits) != 1 {
		t.Fatalf("body change wrote %d cards and %d commits", len(src.created), len(ws.commits))
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

// fileSource writes real card files so a rollback can be observed on disk.
type fileSource struct {
	*fakeSource
	dir string
}

func (f *fileSource) Create(ctx context.Context, task core.Task) (core.Task, error) {
	path := filepath.Join(f.dir, string(task.ID)+".md")
	if err := os.WriteFile(path, []byte("id: "+string(task.ID)+"\n"), 0o644); err != nil {
		return core.Task{}, err
	}
	created := task
	created.SourceRef = path
	f.log.add("create:" + string(task.ID))
	return created, nil
}

func (f *fileSource) Remove(ctx context.Context, id core.TaskID) error {
	if err := os.Remove(filepath.Join(f.dir, string(id)+".md")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return f.fakeSource.Remove(ctx, id)
}

// interruptibleCommitter cancels the caller at the commit boundary and still
// pushes, modelling a harness abort that arrives after the commit is durable.
type interruptibleCommitter struct {
	commits int
	pushes  int
	cancel  context.CancelFunc
}

func (c *interruptibleCommitter) CommitPath(context.Context, core.Workspace, string, string) error {
	c.commits++
	c.cancel()
	c.pushes++
	return nil
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

func TestTaskCreatorCancelBeforeCommitLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	src := &fileSource{fakeSource: &fakeSource{log: &callLog{}}, dir: dir}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := creator.CreateBatch(ctx, []CreateTaskInput{
		{ID: "T-CANCEL-A", Title: "A"},
		{ID: "T-CANCEL-B", Title: "B"},
	}); err == nil {
		t.Fatal("cancelled batch did not surface an error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cancelled batch left files: %v", entries)
	}
	if len(src.removed) != 1 || src.removed[0] != "T-CANCEL-A" {
		t.Fatalf("removed = %v, want the one card written before commit", src.removed)
	}
	if len(ws.commits) != 0 {
		t.Fatalf("commits = %d, want none", len(ws.commits))
	}
}

func TestTaskCreatorFinishesPushWhenCancelledAfterCommit(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws := &interruptibleCommitter{cancel: cancel}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	got, err := creator.Create(ctx, CreateTaskInput{ID: "T-INTERRUPT", Title: "Card"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed || ws.commits != 1 || ws.pushes != 1 {
		t.Fatalf("result = %+v, commits = %d, pushes = %d", got, ws.commits, ws.pushes)
	}
	if ctx.Err() == nil {
		t.Fatal("test did not cancel the context at the commit boundary")
	}
	if len(src.removed) != 0 {
		t.Fatalf("cancelled create removed cards: %v", src.removed)
	}
}

func TestTaskCreatorCreateBatchRejectsInvalidIDWritesNothing(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &fakeWorkspace{log: &callLog{}}
	creator := &TaskCreator{Source: src, Workspace: ws, Root: core.Workspace{Path: "/root", Branch: "main"}}

	_, err := creator.CreateBatch(context.Background(), []CreateTaskInput{
		{ID: "T-GOOD", Title: "A"},
		{ID: "../escape", Title: "B"},
	})
	if core.CodeOf(err) != core.CodeInvalidInput {
		t.Fatalf("error = %v", err)
	}
	if len(src.log.items) != 0 || len(ws.commits) != 0 {
		t.Fatalf("invalid id wrote cards = %v, commits = %d", src.log.items, len(ws.commits))
	}
}

// branchWorkspace records branch publication alongside the shared fake's
// commits, so tests can prove the branch is pushed before any card is written.
type branchWorkspace struct {
	*fakeWorkspace
	branches     []string
	baseBranches []string
	branchErr    error
}

func (b *branchWorkspace) EnsureBranch(_ context.Context, branch, base string) error {
	if b.branchErr != nil {
		return b.branchErr
	}
	b.branches = append(b.branches, branch)
	b.baseBranches = append(b.baseBranches, base)
	b.log.add("branch:" + branch)
	return nil
}

func (b *branchWorkspace) Create(ctx context.Context, t core.Task) (core.Workspace, error) {
	b.log.add("create:" + string(t.ID))
	return b.fakeWorkspace.Create(ctx, t)
}

func TestTaskCreatorPublishesEpicBranchBeforeChildren(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}}
	creator := &TaskCreator{Source: src, Workspace: ws,
		Root: core.Workspace{Path: "/root", Branch: "main"}}

	got, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.branches) != 1 || ws.branches[0] != "beacon/T-EPIC" {
		t.Fatalf("branches = %v, want beacon/T-EPIC", ws.branches)
	}
	if len(ws.baseBranches) != 1 || ws.baseBranches[0] != "main" {
		t.Fatalf("base branches = %v, want main", ws.baseBranches)
	}
	if ws.log.items[0] != "branch:beacon/T-EPIC" {
		t.Fatalf("call order = %v, want branch before cards", ws.log.items)
	}
	if len(src.created) != 3 {
		t.Fatalf("created cards = %d, want epic plus two children", len(src.created))
	}
	for _, task := range src.created[:2] {
		if task.BaseBranch != "beacon/T-EPIC" {
			t.Fatalf("child %s base branch = %q", task.ID, task.BaseBranch)
		}
	}
	if got.ID != "T-EPIC" || !got.Pushed {
		t.Fatalf("result = %+v", got)
	}
}

func TestTaskCreatorDoesNotRepublishEpicBranch(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}}
	creator := &TaskCreator{Source: src, Workspace: ws,
		Root: core.Workspace{Path: "/root", Branch: "main"}}
	in := CreateTaskInput{Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan()}

	for i := 0; i < 2; i++ {
		if _, err := creator.Create(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if len(ws.branches) != 1 || len(ws.commits) != 1 {
		t.Fatalf("branches = %v, commits = %d, want one each", ws.branches, len(ws.commits))
	}
}

func TestTaskCreatorBranchFailureWritesNoCards(t *testing.T) {
	want := errors.New("push rejected")
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}, branchErr: want}
	creator := &TaskCreator{Source: src, Workspace: ws,
		Root: core.Workspace{Path: "/root", Branch: "main"}}

	_, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Epic", ID: "T-EPIC", Kind: core.TaskKindEpic, Plan: epicPlan(),
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if len(src.log.items) != 0 || len(ws.commits) != 0 || ws.creates != 0 {
		t.Fatalf("failed branch left cards = %v, commits = %d, creates = %d",
			src.log.items, len(ws.commits), ws.creates)
	}
}

func TestTaskCreatorIgnoresNonEpicBaseBranch(t *testing.T) {
	src := &recordingEpicSource{fakeSource: &fakeSource{log: &callLog{}}}
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}}
	creator := &TaskCreator{Source: src, Workspace: ws,
		Root: core.Workspace{Path: "/root", Branch: "main"}}

	if _, err := creator.Create(context.Background(), CreateTaskInput{
		Title: "Card", ID: "T-CARD", BaseBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	if len(ws.branches) != 0 || len(ws.commits) != 1 {
		t.Fatalf("branches = %v, commits = %d, want no branch work", ws.branches, len(ws.commits))
	}
}
