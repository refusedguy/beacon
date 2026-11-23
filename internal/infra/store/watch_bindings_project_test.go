package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestWriteWatchBindingKeepsTwoProjectsAtSameRevision pins the BCX-34 bug.
// The old (watch_id, revision) key made the second project's write look like
// a repeat, so the row was dropped before it reached SQLite.
func TestWriteWatchBindingKeepsTwoProjectsAtSameRevision(t *testing.T) {
	d := openTestDB(t, "project-key.db")
	const watchID = "shared-watch"
	for _, project := range []string{"project-a", "project-b"} {
		e := WatchBindingEntry{
			ProjectKey:   project,
			WatchID:      watchID,
			Revision:     1,
			AgentID:      project + "-session",
			ConfigID:     project + "-cfg",
			FencingToken: 1,
			SubscriberID: project + "-sub",
		}
		w, err := d.WriteWatchBinding(e)
		if err != nil {
			t.Fatalf("WriteWatchBinding(%s): %v", project, err)
		}
		if !w.Created {
			t.Fatalf("WriteWatchBinding(%s) reported Created=false", project)
		}
		if w.Entry.ProjectKey != project {
			t.Fatalf("stored project = %q, want %q", w.Entry.ProjectKey, project)
		}
	}
	if got := watchBindingRowCount(t, d, watchID); got != 2 {
		t.Fatalf("binding rows = %d, want 2", got)
	}
	for _, project := range []string{"project-a", "project-b"} {
		r := scopedRegistry(t, d, project)
		binding, found, err := r.WatchBinding(watchID, 1)
		if err != nil {
			t.Fatalf("scoped WatchBinding(%s): %v", project, err)
		}
		if !found {
			t.Fatalf("scoped WatchBinding(%s) found no row", project)
		}
		if binding.ProjectKey != project || binding.AgentID != project+"-session" {
			t.Fatalf("scoped WatchBinding(%s) = %+v", project, binding)
		}
		if n, err := r.WatchBindingCount(watchID); err != nil || n != 1 {
			t.Fatalf("WatchBindingCount(%s) = %d, %v; want 1, nil", project, n, err)
		}
	}
}

// TestBindWatchKeepsTwoProjectsAtSameRevision covers the other insert path in
// registry.go, which also used INSERT OR IGNORE with the watch-only key.
func TestBindWatchKeepsTwoProjectsAtSameRevision(t *testing.T) {
	d := openTestDB(t, "project-key.db")
	const watchID = "shared-watch"
	for _, project := range []string{"project-a", "project-b"} {
		b := WatchBinding{
			WatchID:      watchID,
			Revision:     1,
			ProjectKey:   project,
			AgentID:      project + "-session",
			ConfigID:     project + "-cfg",
			FencingToken: "1",
		}
		res, err := d.BindWatch(b)
		if err != nil {
			t.Fatalf("BindWatch(%s): %v", project, err)
		}
		if !res.Created {
			t.Fatalf("BindWatch(%s) reported Created=false", project)
		}
	}
	if got := watchBindingRowCount(t, d, watchID); got != 2 {
		t.Fatalf("binding rows = %d, want 2", got)
	}
}

// TestWatchBindingProjectKeyRebuildsLegacyTable proves that a database created
// with the old (watch_id, revision) primary key is rebuilt on the next write
// and keeps the rows already stored.
func TestWatchBindingProjectKeyRebuildsLegacyTable(t *testing.T) {
	d := openTestDB(t, "project-key.db")
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DROP TABLE watch_bindings`); err != nil {
			return err
		}
		_, err := tx.Exec(`CREATE TABLE watch_bindings (
			project_key   TEXT NOT NULL,
			watch_id      TEXT NOT NULL,
			revision      INTEGER NOT NULL,
			agent_id      TEXT NOT NULL,
			config_id     TEXT NOT NULL,
			fencing_token INTEGER NOT NULL,
			subscriber_id TEXT NOT NULL,
			created_at    INTEGER NOT NULL,
			PRIMARY KEY (watch_id, revision))`)
		return err
	})
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	const watchID = "legacy-watch"
	// Store one row with raw SQL so the next write has to migrate real data.
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO watch_bindings
			(project_key, watch_id, revision, agent_id, config_id,
			 fencing_token, subscriber_id, created_at)
			VALUES ('project-b', ?, 1, 'b-session', 'b-cfg', 1, 'b-sub', 1)`, watchID)
		return err
	}); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if _, err := d.WriteWatchBinding(WatchBindingEntry{
		ProjectKey: "project-a", WatchID: watchID, Revision: 1,
		AgentID: "a-session", ConfigID: "a-cfg", FencingToken: 1,
	}); err != nil {
		t.Fatalf("write after rebuild: %v", err)
	}
	if got := watchBindingRowCount(t, d, watchID); got != 2 {
		t.Fatalf("binding rows = %d, want 2", got)
	}
	pk, err := watchBindingPKColumns(d.readDB)
	if err != nil {
		t.Fatalf("watchBindingPKColumns: %v", err)
	}
	want := []string{"project_key", "watch_id", "revision"}
	if len(pk) != len(want) {
		t.Fatalf("primary key = %v, want %v", pk, want)
	}
	for i := range want {
		if !strings.EqualFold(pk[i], want[i]) {
			t.Fatalf("primary key = %v, want %v", pk, want)
		}
	}
}
