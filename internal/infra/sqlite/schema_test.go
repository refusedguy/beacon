package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // register the "sqlite" driver for openDB
)

func TestOpenDBWithReservedCharactersInPath(t *testing.T) {
	for _, name := range []string{
		"state.db",
		"state?branch.db",
		"state#run.db",
		"state?branch#run.db",
		"state with space.db",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			db, err := openDB(path)
			if err != nil {
				t.Fatalf("openDB(%q): %v", path, err)
			}
			defer db.Close()

			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database file %q: %v", path, err)
			}
			_, err = db.ExecContext(context.Background(), `
INSERT INTO runs (id, task_id, state, created_at, updated_at)
VALUES ('run-1', 'T-1', 'pending', 1, 2)`)
			if err != nil {
				t.Fatalf("insert run: %v", err)
			}
			var id string
			if err := db.QueryRow(`SELECT id FROM runs WHERE id = 'run-1'`).Scan(&id); err != nil {
				t.Fatalf("read run: %v", err)
			}
			if id != "run-1" {
				t.Fatalf("read run id = %q, want run-1", id)
			}
		})
	}
}

func TestOpenDBAppliesPragmas(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var foreignKeys, busyTimeout int
	var journalMode string
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}
}
