package store

import (
	"context"
	"fmt"
	"sort"
)

// Migration is one named schema change. Name is the stable key written to the
// ledger and Body is the SQL the port applies. The NNNN_name convention keeps
// the sorted order equal to the intended order.
type Migration struct {
	Name string
	Body string
}

// applicationsMigration creates the applications table. It is the first
// migration of the server state store. Later work appends new entries and
// leaves this one unchanged, so a database from an older binary keeps its rows.
var applicationsMigration = Migration{
	Name: "0001_applications",
	Body: `CREATE TABLE IF NOT EXISTS applications (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
}

// migrations is the fixed corpus applied in order. Appending to the slice is
// how a schema change ships; editing an applied entry is not.
var migrations = []Migration{applicationsMigration}

// ledgerSQL creates the table that records applied migration names. Running it
// on every open is safe on a fresh and on an existing store.
const ledgerSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY)`

// migrate brings the store up to date. It creates the ledger, reads the names
// already recorded once, then applies the migrations whose name is absent. The
// port commits a body together with its name, so a name in the ledger means
// the body ran. A second call sees the same names and applies nothing. Sorting
// the corpus makes the outcome independent of the order of the entries.
func migrate(ctx context.Context, port Port) error {
	if err := port.Exec(ctx, ledgerSQL); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	applied, err := port.Applied(ctx)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	done := make(map[string]bool, len(applied))
	for _, name := range applied {
		done[name] = true
	}

	ordered := make([]Migration, len(migrations))
	copy(ordered, migrations)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	for _, m := range ordered {
		if done[m.Name] {
			continue
		}
		if err := port.Apply(ctx, m.Name, m.Body); err != nil {
			return fmt.Errorf("apply %s: %w", m.Name, err)
		}
	}
	return nil
}
