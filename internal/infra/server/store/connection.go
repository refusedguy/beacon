// Package store owns the server's connection to its state store. The logic in
// this package decides when a store opens and which migrations run. A small
// Port interface hides the file and the SQL driver, so the connection and the
// migration runner hold no heavy I/O of their own and tests drive them with an
// in-memory port.
package store

import (
	"context"
	"errors"
	"fmt"
)

// ErrEmptyStateDir reports an empty state directory path. Open refuses the
// path before any port call: a store location is never optional.
var ErrEmptyStateDir = errors.New("store: empty state directory")

// Port is the I/O boundary of the state store. An adapter opens a real file
// with a SQL driver; tests pass an in-memory implementation. Nothing in the
// connection or migration logic names a driver, a file, or a directory call.
type Port interface {
	// Open makes the store at path ready. path is a directory and the adapter
	// creates it when it is missing. The adapter reports its errors unchanged.
	Open(ctx context.Context, path string) error
	// Exec runs one statement that returns no rows, such as a PRAGMA or the
	// creation of the migration ledger.
	Exec(ctx context.Context, statement string) error
	// Applied returns the names of migrations already recorded in the store.
	Applied(ctx context.Context) ([]string, error)
	// Apply runs body and records name in one transaction. The adapter makes a
	// repeated name a no-op, so the pair commits together or not at all.
	Apply(ctx context.Context, name, body string) error
	// Close releases the store. The adapter makes repeated calls safe.
	Close() error
}

// Connection is an open handle to the state store. It keeps the path and the
// port; all work goes through the port.
type Connection struct {
	port Port
	path string
}

// pragmas are applied on every open, in this order. WAL keeps readers from
// waiting on the writer and foreign_keys makes referential actions hold.
var pragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA foreign_keys = ON",
}

// Open validates path, opens the store through the port, sets the PRAGMAs and
// runs pending migrations. An empty path fails before any port call. When a
// PRAGMA or a migration fails, Open closes the port and returns no Connection.
func Open(ctx context.Context, path string, port Port) (*Connection, error) {
	if path == "" {
		return nil, ErrEmptyStateDir
	}
	if port == nil {
		return nil, errors.New("store: nil port")
	}
	if err := port.Open(ctx, path); err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	c := &Connection{port: port, path: path}
	for _, pragma := range pragmas {
		if err := port.Exec(ctx, pragma); err != nil {
			port.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", pragma, err)
		}
	}
	if err := migrate(ctx, port); err != nil {
		port.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return c, nil
}

// Path returns the directory the connection was opened with.
func (c *Connection) Path() string { return c.path }

// Close releases the store through the port.
func (c *Connection) Close() error { return c.port.Close() }
