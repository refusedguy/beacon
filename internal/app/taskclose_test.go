package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const closeCardBody = `---
id: T-A
title: Flat perf run
status: available
priority: 3
---
# T-A. Card

Body line.
`

func newCloser(dir string, store ActiveRunFinder, ws PathCommitter) *TaskCloser {
	return &TaskCloser{Store: store, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}
}

func TestTaskCloserWritesDoneStatusAndReason(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", closeCardBody)
	ws := &fakeWorkspace{log: &callLog{}}
	closer := newCloser(dir, newFakeStore(), ws)

	got, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-A", Status: core.TaskDone, Reason: "merged in PR 2861"},
	})
	if err != nil {
		t.Fatalf("closer.Close(...): %v", err)
	}
	if !got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "closed" {
		t.Fatalf("result = %+v", got)
	}
	content := readUpdateCard(t, path)
	if !strings.Contains(content, "status: done") ||
		!strings.Contains(content, "status_reason: merged in PR 2861") {
		t.Fatalf("card missing status or reason: %s", content)
	}
	if !strings.Contains(content, "title: Flat perf run") || !strings.Contains(content, "Body line.") {
		t.Fatalf("close changed unrelated content: %s", content)
	}
	if len(ws.commits) != 1 || ws.commits[0].pathspec != cardsPath ||
		ws.commits[0].message != "beacon: close tasks" {
		t.Fatalf("commits = %+v", ws.commits)
	}
}

func TestTaskCloserWritesDroppedStatus(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", closeCardBody)
	closer := newCloser(dir, newFakeStore(), &fakeWorkspace{log: &callLog{}})

	got, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-A", Status: core.TaskDropped, Reason: "every advisory row was flat"},
	})
	if err != nil {
		t.Fatalf("closer.Close(...): %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].Status != "closed" {
		t.Fatalf("result = %+v", got)
	}
	content := readUpdateCard(t, path)
	if !strings.Contains(content, "status: dropped") ||
		!strings.Contains(content, "status_reason: every advisory row was flat") {
		t.Fatalf("card missing dropped status or reason: %s", content)
	}
}

func TestTaskCloserSyncsStatusToGraph(t *testing.T) {
	dir := t.TempDir()
	writeUpdateCard(t, dir, "T-A", closeCardBody)
	source := &cardSource{tasks: []core.Task{{ID: "T-A", Status: core.TaskAvailable}}}
	store := &syncStore{fakeStore: newFakeStore()}
	closer := &TaskCloser{
		Store: store, Source: source, Workspace: &fakeWorkspace{log: &callLog{}},
		Root: core.Workspace{Path: dir, Branch: "main"},
	}

	if _, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-A", Status: core.TaskDropped, Reason: "cancelled"},
	}); err != nil {
		t.Fatalf("closer.Close(...): %v", err)
	}
	if len(store.tasks) != 1 || store.tasks[0].Status != core.TaskDropped {
		t.Fatalf("synced graph = %+v", store.tasks)
	}
}

func TestTaskCloserRefusesBatchWithActiveRun(t *testing.T) {
	dir := t.TempDir()
	a := writeUpdateCard(t, dir, "T-A", closeCardBody)
	b := writeUpdateCard(t, dir, "T-B", "---\nid: T-B\nstatus: available\n---\nbody\n")
	store := newFakeStore()
	if err := store.Save(context.Background(), core.NewRun("run-1", "T-B", time.Now().UTC())); err != nil {
		t.Fatalf("store.Save(...): %v", err)
	}
	ws := &fakeWorkspace{log: &callLog{}}
	closer := newCloser(dir, store, ws)

	got, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-A", Status: core.TaskDone, Reason: "done"},
		{ID: "T-B", Status: core.TaskDropped, Reason: "cancelled"},
	})
	if err != nil {
		t.Fatalf("closer.Close(...): %v", err)
	}
	if len(got.Refused) != 1 || got.Refused[0] != "T-B" || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if readUpdateCard(t, a) != closeCardBody || !strings.Contains(readUpdateCard(t, b), "status: available") {
		t.Fatal("refusal touched the tree")
	}
	if len(ws.commits) != 0 {
		t.Fatal("refusal still committed")
	}
}

func TestTaskCloserRejectsInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", closeCardBody)
	ws := &fakeWorkspace{log: &callLog{}}
	closer := newCloser(dir, newFakeStore(), ws)

	_, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-A", Status: core.TaskClaimed, Reason: "claimed by a run"},
	})
	if err == nil || !strings.Contains(err.Error(), "must be done or dropped") {
		t.Fatalf("error = %v, want invalid status error", err)
	}
	if appErr, ok := err.(*core.AppError); !ok || appErr.Code != core.CodeInvalidInput {
		t.Fatalf("error type = %T, want CodeInvalidInput", err)
	}
	if readUpdateCard(t, path) != closeCardBody || len(ws.commits) != 0 {
		t.Fatal("invalid status changed the tree or committed")
	}
}

func TestTaskCloserRequiresReason(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", closeCardBody)
	ws := &fakeWorkspace{log: &callLog{}}
	closer := newCloser(dir, newFakeStore(), ws)

	_, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-A", Status: core.TaskDropped, Reason: "  "},
	})
	if err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("error = %v, want missing reason error", err)
	}
	if readUpdateCard(t, path) != closeCardBody || len(ws.commits) != 0 {
		t.Fatal("missing reason changed the tree or committed")
	}
}

func TestTaskCloserIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", closeCardBody)
	ws := &fakeWorkspace{log: &callLog{}}
	closer := newCloser(dir, newFakeStore(), ws)
	input := []CloseInput{{ID: "T-A", Status: core.TaskDone, Reason: "merged"}}
	ctx := context.Background()

	first, err := closer.Close(ctx, input)
	if err != nil {
		t.Fatalf("first close: %v", err)
	}
	if len(first.Results) != 1 || first.Results[0].Status != "closed" || !first.Pushed {
		t.Fatalf("first = %+v", first)
	}
	afterFirst := readUpdateCard(t, path)
	second, err := closer.Close(ctx, input)
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
	if len(second.Results) != 1 || second.Results[0].Status != "unchanged" || second.Pushed {
		t.Fatalf("second = %+v", second)
	}
	if readUpdateCard(t, path) != afterFirst || len(ws.commits) != 1 {
		t.Fatal("retry changed the card or committed again")
	}
}

func TestTaskCloserReportsNotFound(t *testing.T) {
	dir := t.TempDir()
	ws := &fakeWorkspace{log: &callLog{}}
	closer := newCloser(dir, newFakeStore(), ws)

	got, err := closer.Close(context.Background(), []CloseInput{
		{ID: "T-MISSING", Status: core.TaskDone, Reason: "gone"},
	})
	if err != nil {
		t.Fatalf("closer.Close(...): %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].Status != string(core.CodeNotFound) || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if len(ws.commits) != 0 {
		t.Fatal("not_found close committed")
	}
}
