package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newWatchBindingTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func watchBindingEntry(watchID string, revision uint64, token int64, configID string) WatchBindingEntry {
	return WatchBindingEntry{
		ProjectKey:   "project-a",
		WatchID:      watchID,
		Revision:     revision,
		AgentID:      "session-1",
		ConfigID:     configID,
		FencingToken: token,
		SubscriberID: "T5.25",
	}
}

func watchBindingRowCount(t *testing.T, d *DB, watchID string) int {
	t.Helper()
	var n int
	if err := d.readDB.QueryRow(
		`SELECT count(*) FROM watch_bindings WHERE watch_id = ?`, watchID,
	).Scan(&n); err != nil {
		t.Fatalf("count watch bindings: %v", err)
	}
	return n
}

// TestRegisterWatchExecutorWritesBinding proves the watch_pr store adapter
// writes the executor row and its Watch binding with the configId. A repeat of
// the same registration leaves one binding row.
func TestRegisterWatchExecutorWritesBinding(t *testing.T) {
	d := newWatchBindingTestDB(t)
	const (
		projectKey = "project-a"
		watchID    = "watch-61"
		sessionID  = "ses_61"
		configID   = "cfg-61"
	)
	for i := 0; i < 2; i++ {
		if err := d.RegisterWatchExecutor(projectKey, watchID, sessionID,
			"executor", "subagent", "opencode", sessionID, configID, 3); err != nil {
			t.Fatalf("RegisterWatchExecutor(%d): %v", i, err)
		}
	}

	binding, found, err := d.WatchBinding(watchID, 3)
	if err != nil {
		t.Fatalf("WatchBinding: %v", err)
	}
	if !found {
		t.Fatal("watch binding was not written")
	}
	if binding.ProjectKey != projectKey || binding.AgentID != sessionID || binding.ConfigID != configID {
		t.Errorf("binding = %+v, want project %s, agent %s, config %s",
			binding, projectKey, sessionID, configID)
	}
	if got := watchBindingRowCount(t, d, watchID); got != 1 {
		t.Errorf("binding rows = %d, want 1", got)
	}

	executor, found, err := d.Executor(projectKey, sessionID)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	if !found {
		t.Fatal("executor row was not written")
	}
	if executor.ConfigID != configID || executor.SessionID != sessionID {
		t.Errorf("executor = %+v, want config %s and session %s", executor, configID, sessionID)
	}
}

// TestWriteWatchBindingUsesCanonicalDDL pins the fix for the old mismatch:
// registry.go created fencing_token as TEXT while migration 0005 declared it
// INTEGER. A binding written after the table is dropped must get the migration
// shape, including the project_key CHECK.
func TestWriteWatchBindingUsesCanonicalDDL(t *testing.T) {
	d := newWatchBindingTestDB(t)
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TABLE IF EXISTS watch_bindings`)
		return err
	}); err != nil {
		t.Fatalf("drop watch_bindings: %v", err)
	}
	if _, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 2, 20, "cfg")); err != nil {
		t.Fatalf("WriteWatchBinding after drop: %v", err)
	}
	if got := registryColumnType(t, d, "watch_bindings", "fencing_token"); got != "INTEGER" {
		t.Errorf("fencing_token type = %q, want INTEGER", got)
	}
	body := registryTableSQL(t, d, "watch_bindings")
	if !strings.Contains(body, "CHECK (length(project_key) > 0)") {
		t.Errorf("watch_bindings lost the project_key CHECK: %s", body)
	}
}

func TestWriteWatchBindingCreatesDistinctRows(t *testing.T) {
	d := newWatchBindingTestDB(t)

	first, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 3, 30, "config-v1"))
	if err != nil {
		t.Fatalf("WriteWatchBinding(rev 3): %v", err)
	}
	if !first.Created {
		t.Fatal("first write must report Created=true")
	}
	second, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 4, 40, "config-v2"))
	if err != nil {
		t.Fatalf("WriteWatchBinding(rev 4): %v", err)
	}
	if !second.Created {
		t.Fatal("new revision must report Created=true")
	}
	if n := watchBindingRowCount(t, d, "watch-1"); n != 2 {
		t.Fatalf("one watch with two revisions: want 2 rows, got %d", n)
	}

	stored, found, err := d.LatestWatchBinding("watch-1")
	if err != nil {
		t.Fatalf("LatestWatchBinding: %v", err)
	}
	if !found {
		t.Fatal("latest binding not found")
	}
	if stored.Revision != 4 || stored.ConfigID != "config-v2" || stored.FencingToken != 40 {
		t.Fatalf("latest binding = %+v, want rev 4 config-v2 token 40", stored)
	}
	if stored.SubscriberID != "T5.25" {
		t.Fatalf("subscriber = %q, want T5.25", stored.SubscriberID)
	}
}

func TestWriteWatchBindingOldRevisionDoesNotOverwriteNew(t *testing.T) {
	d := newWatchBindingTestDB(t)

	newer := watchBindingEntry("watch-1", 9, 90, "config-new")
	if _, err := d.WriteWatchBinding(newer); err != nil {
		t.Fatalf("WriteWatchBinding(new): %v", err)
	}

	older := watchBindingEntry("watch-1", 4, 40, "config-old")
	if _, err := d.WriteWatchBinding(older); !errors.Is(err, ErrStaleWatchBinding) {
		t.Fatalf("old revision error = %v, want ErrStaleWatchBinding", err)
	}
	if n := watchBindingRowCount(t, d, "watch-1"); n != 1 {
		t.Fatalf("stale write added a row, want 1 got %d", n)
	}

	stored, found, err := d.LatestWatchBinding("watch-1")
	if err != nil {
		t.Fatalf("LatestWatchBinding: %v", err)
	}
	if !found || stored.ConfigID != "config-new" || stored.Revision != 9 {
		t.Fatalf("stale write overwrote the new row: %+v, found=%v", stored, found)
	}

	repeat, err := d.WriteWatchBinding(newer)
	if err != nil {
		t.Fatalf("repeat WriteWatchBinding: %v", err)
	}
	if repeat.Created {
		t.Fatal("repeat write must report Created=false")
	}
	if repeat.Entry.CreatedAt != stored.CreatedAt || repeat.Entry.ConfigID != "config-new" {
		t.Fatalf("repeat write changed the row: %+v", repeat.Entry)
	}
}

func TestWriteWatchBindingFencingToken(t *testing.T) {
	d := newWatchBindingTestDB(t)

	if _, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 2, 20, "first")); err != nil {
		t.Fatalf("WriteWatchBinding(rev 2, token 20): %v", err)
	}
	if _, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 3, 10, "stale")); !errors.Is(err, ErrStaleWatchBinding) {
		t.Fatalf("stale token error = %v, want ErrStaleWatchBinding", err)
	}
	if _, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 3, 30, "fresh")); err != nil {
		t.Fatalf("WriteWatchBinding(rev 3, token 30): %v", err)
	}

	stored, found, err := d.LatestWatchBinding("watch-1")
	if err != nil {
		t.Fatalf("LatestWatchBinding: %v", err)
	}
	if !found || stored.ConfigID != "fresh" || stored.FencingToken != 30 {
		t.Fatalf("latest binding = %+v, found=%v", stored, found)
	}
	if n := watchBindingRowCount(t, d, "watch-1"); n != 2 {
		t.Fatalf("want 2 rows after one stale rejection, got %d", n)
	}
}

func TestWriteWatchBindingDefaultsTokenAndSubscriber(t *testing.T) {
	d := newWatchBindingTestDB(t)
	e := watchBindingEntry("watch-1", 5, 0, "config")
	e.SubscriberID = ""

	written, err := d.WriteWatchBinding(e)
	if err != nil {
		t.Fatalf("WriteWatchBinding: %v", err)
	}
	if written.Entry.FencingToken != 5 {
		t.Fatalf("default token = %d, want revision 5", written.Entry.FencingToken)
	}
	if written.Entry.SubscriberID != e.AgentID {
		t.Fatalf("default subscriber = %q, want agent id %q", written.Entry.SubscriberID, e.AgentID)
	}
}

func TestWriteWatchBindingRejectsEmptyKeys(t *testing.T) {
	d := newWatchBindingTestDB(t)
	for _, e := range []WatchBindingEntry{
		watchBindingEntry("", 1, 1, "config"),
		func() WatchBindingEntry {
			e := watchBindingEntry("watch-1", 1, 1, "config")
			e.ProjectKey = ""
			return e
		}(),
		func() WatchBindingEntry {
			e := watchBindingEntry("watch-1", 1, 1, "config")
			e.AgentID = ""
			return e
		}(),
	} {
		if _, err := d.WriteWatchBinding(e); err == nil {
			t.Fatalf("WriteWatchBinding(%+v) must fail", e)
		}
	}
	if _, _, err := d.LatestWatchBinding(""); err == nil {
		t.Fatal("LatestWatchBinding with empty watch id must fail")
	}
	if _, found, err := d.LatestWatchBinding("missing"); err != nil || found {
		t.Fatalf("missing watch: found=%v err=%v", found, err)
	}
}

func TestWriteWatchBindingConcurrentSameRevision(t *testing.T) {
	d := newWatchBindingTestDB(t)

	const writers = 16
	var wg sync.WaitGroup
	created := make(chan bool, writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := d.WriteWatchBinding(watchBindingEntry("watch-1", 7, 70, "config"))
			if err != nil {
				errs <- err
				return
			}
			created <- res.Created
		}()
	}
	wg.Wait()
	close(created)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent WriteWatchBinding: %v", err)
	}
	n := 0
	for ok := range created {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one Created=true, got %d", n)
	}
	if rows := watchBindingRowCount(t, d, "watch-1"); rows != 1 {
		t.Fatalf("concurrent same revision: want 1 row, got %d", rows)
	}
}
