package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestTableColumns(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "columns.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	t.Run("existing table", func(t *testing.T) {
		cols, err := tableColumns(d.readDB, "events")
		if err != nil {
			t.Fatalf("tableColumns: %v", err)
		}
		for _, name := range []string{"seq", "scope", "type", "schema_version", "payload"} {
			if !cols[name] {
				t.Fatalf("events table is missing column %q; got %v", name, cols)
			}
		}
		if cols["no_such_column"] {
			t.Fatalf("tableColumns reported an unknown column: %v", cols)
		}
	})

	t.Run("lowercase keys", func(t *testing.T) {
		if err := d.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`CREATE TABLE mixed_case (MixedCol TEXT, OtherCol INT)`)
			return err
		}); err != nil {
			t.Fatalf("create mixed_case: %v", err)
		}
		cols, err := tableColumns(d.readDB, "mixed_case")
		if err != nil {
			t.Fatalf("tableColumns: %v", err)
		}
		for _, name := range []string{"mixedcol", "othercol"} {
			if !cols[name] {
				t.Fatalf("missing lowercase key %q; got %v", name, cols)
			}
		}
	})
}
