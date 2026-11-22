package handoff_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/app/handoff"
)

func TestTwoAgentsWithDifferentWorktreesReadOneRecord(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	worktreeA := filepath.Join(base, "worktree-a")
	worktreeB := filepath.Join(base, "worktree-b")
	for _, dir := range []string{worktreeA, worktreeB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	agentA, err := handoff.NewFileSharedFolder(state)
	if err != nil {
		t.Fatalf("agent A store: %v", err)
	}
	agentB, err := handoff.NewFileSharedFolder(state)
	if err != nil {
		t.Fatalf("agent B store: %v", err)
	}

	key := handoff.Key{WorkstreamID: "ws-42", Kind: handoff.KindHandoff, Name: "fix-droid"}
	written, err := agentA.Put(context.Background(), key, []byte("read the listener settings"))
	if err != nil {
		t.Fatalf("agent A Put: %v", err)
	}
	got, err := agentB.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("agent B Get: %v", err)
	}

	if got.Key() != written.Key() {
		t.Fatalf("key = %+v, want %+v", got.Key(), written.Key())
	}
	if !bytes.Equal(got.Body, written.Body) {
		t.Fatalf("body = %q, want %q", got.Body, written.Body)
	}
	if got.Version != written.Version || !got.WrittenAt.Equal(written.WrittenAt) {
		t.Fatalf("manifest = (%d, %s), want (%d, %s)",
			got.Version, got.WrittenAt, written.Version, written.WrittenAt)
	}

	// The document lives in the state dir, never inside an agent worktree.
	for _, worktree := range []string{worktreeA, worktreeB} {
		if _, err := os.Stat(filepath.Join(worktree, "shared")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("state leaked into worktree %s: stat err = %v", worktree, err)
		}
	}
}

func TestPutIncrementsVersionAndRefreshesTimestamp(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	agentA, err := handoff.NewFileSharedFolder(state)
	if err != nil {
		t.Fatalf("agent A store: %v", err)
	}
	agentB, err := handoff.NewFileSharedFolder(state)
	if err != nil {
		t.Fatalf("agent B store: %v", err)
	}

	key := handoff.Key{WorkstreamID: "ws-1", Kind: handoff.KindFinding, Name: "race"}
	first, err := agentA.Put(context.Background(), key, []byte("first"))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second, err := agentB.Put(context.Background(), key, []byte("second"))
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	if second.Version != first.Version+1 {
		t.Fatalf("version = %d, want %d", second.Version, first.Version+1)
	}
	if second.WrittenAt.Before(first.WrittenAt) {
		t.Fatalf("WrittenAt went backwards: %s before %s", second.WrittenAt, first.WrittenAt)
	}
	got, err := agentA.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Body) != "second" {
		t.Fatalf("body = %q, want %q", got.Body, "second")
	}
}

func TestUnsafeKeysRejected(t *testing.T) {
	store, err := handoff.NewFileSharedFolder(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name string
		key  handoff.Key
	}{
		{"absolute name", handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: "/etc/passwd"}},
		{"windows absolute name", handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: `C:\temp\plan.md`}},
		{"parent name", handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: ".."}},
		{"parent path", handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: "../secret"}},
		{"nested name", handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: "a/b"}},
		{"empty name", handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: ""}},
		{"parent workstream", handoff.Key{WorkstreamID: "../other", Kind: handoff.KindPlan, Name: "plan"}},
		{"absolute workstream", handoff.Key{WorkstreamID: "/tmp", Kind: handoff.KindPlan, Name: "plan"}},
		{"unknown kind", handoff.Key{WorkstreamID: "ws", Kind: handoff.Kind("bogus"), Name: "plan"}},
		{"empty kind", handoff.Key{WorkstreamID: "ws", Kind: handoff.Kind(""), Name: "plan"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Put(ctx, tc.key, []byte("x")); !errors.Is(err, handoff.ErrInvalidKey) {
				t.Fatalf("Put err = %v, want ErrInvalidKey", err)
			}
			if _, err := store.Get(ctx, tc.key); !errors.Is(err, handoff.ErrInvalidKey) {
				t.Fatalf("Get err = %v, want ErrInvalidKey", err)
			}
		})
	}

	if _, err := store.List(ctx, "../ws", handoff.KindPlan); !errors.Is(err, handoff.ErrInvalidKey) {
		t.Fatalf("List workstream err = %v, want ErrInvalidKey", err)
	}
	if _, err := store.List(ctx, "ws", handoff.Kind("bogus")); !errors.Is(err, handoff.ErrInvalidKey) {
		t.Fatalf("List kind err = %v, want ErrInvalidKey", err)
	}
}

func TestInterruptedWriteKeepsPreviousContent(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	store, err := handoff.NewFileSharedFolder(state)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	key := handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: "cve"}
	first, err := store.Put(ctx, key, []byte("original"))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	// A crash before the rename leaves a partial temp file beside the document.
	dir := filepath.Join(state, "shared", key.WorkstreamID, string(key.Kind))
	tmp, err := os.CreateTemp(dir, ".shared-*.tmp")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(`{"body":"partial"`); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after interrupted write: %v", err)
	}
	if !bytes.Equal(got.Body, first.Body) || got.Version != first.Version {
		t.Fatalf("document changed: got body %q version %d, want %q version %d",
			got.Body, got.Version, first.Body, first.Version)
	}

	docs, err := store.List(ctx, key.WorkstreamID, key.Kind)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("List returned %d documents, want 1", len(docs))
	}

	next, err := store.Put(ctx, key, []byte("updated"))
	if err != nil {
		t.Fatalf("Put after interrupted write: %v", err)
	}
	if next.Version != first.Version+1 {
		t.Fatalf("version = %d, want %d", next.Version, first.Version+1)
	}
}

func TestConcurrentPutsProduceUniqueVersions(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	key := handoff.Key{WorkstreamID: "ws", Kind: handoff.KindNote, Name: "shared"}
	const writers = 8

	var wg sync.WaitGroup
	versions := make(chan int64, writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store, err := handoff.NewFileSharedFolder(state)
			if err != nil {
				errs <- err
				return
			}
			doc, err := store.Put(context.Background(), key, []byte(strconv.Itoa(i)))
			if err != nil {
				errs <- err
				return
			}
			versions <- doc.Version
		}(i)
	}
	wg.Wait()
	close(errs)
	close(versions)
	for err := range errs {
		t.Fatalf("concurrent Put: %v", err)
	}

	seen := make(map[int64]bool)
	for version := range versions {
		if seen[version] {
			t.Fatalf("duplicate version %d", version)
		}
		seen[version] = true
	}
	for version := int64(1); version <= writers; version++ {
		if !seen[version] {
			t.Fatalf("missing version %d", version)
		}
	}
}

func TestGetMissingAndListByKind(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	store, err := handoff.NewFileSharedFolder(state)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()

	missing := handoff.Key{WorkstreamID: "ws", Kind: handoff.KindHandoff, Name: "none"}
	if _, err := store.Get(ctx, missing); !errors.Is(err, handoff.ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
	empty, err := store.List(ctx, "ws", handoff.KindHandoff)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("List returned %d documents, want 0", len(empty))
	}

	if _, err := store.Put(ctx, handoff.Key{WorkstreamID: "ws", Kind: handoff.KindPlan, Name: "plan"}, []byte("p")); err != nil {
		t.Fatalf("Put plan: %v", err)
	}
	if _, err := store.Put(ctx, handoff.Key{WorkstreamID: "ws", Kind: handoff.KindDeliverable, Name: "result"}, []byte("r")); err != nil {
		t.Fatalf("Put deliverable: %v", err)
	}
	plans, err := store.List(ctx, "ws", handoff.KindPlan)
	if err != nil {
		t.Fatalf("List plan: %v", err)
	}
	if len(plans) != 1 || plans[0].Name != "plan" {
		t.Fatalf("plans = %+v, want one document named plan", plans)
	}
	results, err := store.List(ctx, "ws", handoff.KindDeliverable)
	if err != nil {
		t.Fatalf("List deliverable: %v", err)
	}
	if len(results) != 1 || results[0].Name != "result" {
		t.Fatalf("results = %+v, want one document named result", results)
	}
}

func TestNewFileSharedFolderRequiresAbsoluteRoot(t *testing.T) {
	if _, err := handoff.NewFileSharedFolder("relative/state"); err == nil {
		t.Fatal("relative state dir accepted, want error")
	}
}
