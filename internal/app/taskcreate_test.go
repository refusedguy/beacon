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
