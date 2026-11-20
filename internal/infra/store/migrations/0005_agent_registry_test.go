package migrations_test

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/store"
	_ "modernc.org/sqlite"
)

//go:embed *.sql
var migrationFiles embed.FS

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func execFile(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	body, err := migrationFiles.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if _, err := db.ExecContext(context.Background(), string(body)); err != nil {
		t.Fatalf("exec %s: %v", name, err)
	}
}

func objectCount(t *testing.T, db *sql.DB, typ string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = ?`, typ,
	).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", typ, err)
	}
	return n
}

func requireObject(t *testing.T, db *sql.DB, typ, name string) {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, typ, name,
	).Scan(&n); err != nil {
		t.Fatalf("check %s %s: %v", typ, name, err)
	}
	if n != 1 {
		t.Errorf("%s %s missing: count = %d", typ, name, n)
	}
}

func migrationNames(t *testing.T, upTo int) []string {
	t.Helper()
	entries, err := migrationFiles.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".sql" {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(name, "%04d_", &version); err != nil {
			continue
		}
		if version <= upTo {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// makeV4 builds an existing database at user_version 4 using the real SQL
// corpus, so migration 0005 is proven to apply on top of a live store.
func makeV4(t *testing.T, path string) {
	t.Helper()
	db := rawDB(t, path)
	for _, name := range migrationNames(t, 4) {
		execFile(t, db, name)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA user_version = 4"); err != nil {
		t.Fatalf("set user_version 4: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v4 db: %v", err)
	}
}

func checkSchema0005(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"agent_registry", "agent_capabilities", "watch_bindings",
	} {
		requireObject(t, db, "table", table)
	}
	// Индекс по project_key и status из карточки T5.21.
	requireObject(t, db, "index", "agent_registry_scope")
}

func TestMigration0005AppliesToExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	makeV4(t, path)

	d, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open v4 db: %v", err)
	}
	defer d.Close()
	if got := d.Version(); got != 5 {
		t.Fatalf("Version() = %d, want 5", got)
	}

	db := rawDB(t, path)
	checkSchema0005(t, db)

	// Таблицы принимают строки и соблюдают ключи идемпотентности (T5.22, T5.25).
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_registry
			(project_key, agent_id, role, runtime, created_at, updated_at)
		VALUES ('proj-a', 'ses-1', 'executor', 'subagent', 1, 1)`,
	); err != nil {
		t.Fatalf("insert agent_registry: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_capabilities (project_key, capability, agent_id)
		VALUES ('proj-a', 'watch', 'ses-1')`,
	); err != nil {
		t.Fatalf("insert agent_capabilities: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO watch_bindings
			(project_key, watch_id, revision, agent_id, config_id,
			 fencing_token, subscriber_id, created_at)
		VALUES ('proj-a', 'watch-1', 7, 'ses-1', 'cfg-1', 7, 'ses-1', 1)`,
	); err != nil {
		t.Fatalf("insert watch_bindings: %v", err)
	}

	// Пустой project_key отклоняется границей схемы (T5.28).
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_registry
			(project_key, agent_id, role, runtime, created_at, updated_at)
		VALUES ('', 'ses-2', 'executor', 'subagent', 1, 1)`,
	); err == nil {
		t.Error("empty project_key accepted")
	}
}

func TestMigration0005RepeatChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	makeV4(t, path)

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if got := first.Version(); got != 5 {
		t.Fatalf("first Version() = %d, want 5", got)
	}
	db := rawDB(t, path)
	beforeTables := objectCount(t, db, "table")
	beforeIndexes := objectCount(t, db, "index")
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()
	if got := second.Version(); got != 5 {
		t.Fatalf("second Version() = %d, want 5", got)
	}
	checkSchema0005(t, db)
	if got := objectCount(t, db, "table"); got != beforeTables {
		t.Errorf("tables after reopen = %d, want %d", got, beforeTables)
	}
	if got := objectCount(t, db, "index"); got != beforeIndexes {
		t.Errorf("indexes after reopen = %d, want %d", got, beforeIndexes)
	}

	// Прямой повтор SQL тоже не меняет схему: CREATE ... IF NOT EXISTS.
	execFile(t, db, "0005_agent_registry.sql")
	if got := objectCount(t, db, "table"); got != beforeTables {
		t.Errorf("tables after SQL replay = %d, want %d", got, beforeTables)
	}
	if got := objectCount(t, db, "index"); got != beforeIndexes {
		t.Errorf("indexes after SQL replay = %d, want %d", got, beforeIndexes)
	}
}
