package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// writeCardWithMarker creates a card corpus with one unresolved conflict
// marker. It returns the marker's path and one-based line number so the tests
// assert the location the caller must see in the error.
func writeCardWithMarker(t *testing.T, root string) (string, int) {
	t.Helper()
	dir := filepath.Join(root, cardsPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf(`os.MkdirAll(%q): %v`, dir, err)
	}
	path := filepath.Join(dir, "T-marked.md")
	content := "---\n" +
		"id: T-marked\n" +
		"status: in_progress\n" +
		"---\n" +
		"# T-marked. Broken card\n" +
		"\n" +
		"<<<<<<< HEAD\n" +
		"Scope: a.go\n" +
		"=======\n" +
		"Scope: b.go\n" +
		">>>>>>> other\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf(`os.WriteFile(%q): %v`, path, err)
	}
	return path, 7
}

func assertMarkerLocation(t *testing.T, err error, path string, line int) {
	t.Helper()
	if err == nil {
		t.Fatal("operation with a marked card returned no error")
	}
	if want := path + ":" + strconv.Itoa(line); !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name %s", err, want)
	}
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error code = %q, want %q", core.CodeOf(err), core.CodeConflict)
	}
}

func TestMarkedCardBlocksPublishBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	path, line := writeCardWithMarker(t, dir)
	ws := &fakeWorkspace{log: &callLog{}}
	pub := pathCardPublisher{ws: ws, root: core.Workspace{Path: dir, Branch: "main"}}

	err := pub.Publish(context.Background(), cardsPath, "beacon: create task T-marked")

	assertMarkerLocation(t, err, path, line)
	if len(ws.commits) != 0 || ws.pushes != 0 {
		t.Fatalf("marked card was committed or pushed: commits=%+v pushes=%d", ws.commits, ws.pushes)
	}
}

func TestMarkedCardBlocksMergeBeforeCommitAndPush(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, line := writeCardWithMarker(t, dir)
	r, store, scm, _, src := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	run := store.runs["r1"]
	run.WorkspacePath = dir
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}
	log := &callLog{}
	scm.log = log
	src.log = log
	ws := &fakeWorkspace{log: log}
	r.Workspace = ws

	_, err := r.Reconcile(ctx, 42)

	assertMarkerLocation(t, err, path, line)
	if scm.merges != 0 || len(ws.commits) != 0 || ws.pushes != 0 {
		t.Fatalf("marked card was committed, pushed or merged: merges=%d commits=%+v pushes=%d",
			scm.merges, ws.commits, ws.pushes)
	}
	for _, entry := range log.entries() {
		if entry == "merge" || strings.HasPrefix(entry, "commit:") {
			t.Fatalf("marked card reached merge or commit: %v", log.entries())
		}
	}
	if got := store.taskStatus["T-1"]; got != "" {
		t.Fatalf("task status = %q, want no status change after a blocked merge", got)
	}
	if got := store.runs["r1"]; got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want waiting_ci after a blocked merge", got.State)
	}
}
