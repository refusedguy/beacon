// Package store: transactional outbox for command intent.
//
// One row in command_intents is the durable record that a command was
// accepted; one row in command_outbox is the effect it queued. Both are
// written through the same single-writer transaction, so a rollback leaves
// neither. The key is unique in both tables, which makes a repeated command
// a no-op. The effect itself is executed outside the transaction by the
// command processor's sink, after Commit returns.
//
// The tables are created on first use, like the other rows in this package,
// so no migration file is needed here. This file does not import the app
// layer: Commit has exactly the signature of command.Outbox, so the
// composition root can pass *OutboxStore where that port is required.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// OutboxIntent is the stored record that a command was accepted.
type OutboxIntent struct {
	Key       string
	Kind      string
	Payload   []byte
	CreatedAt int64
}

// OutboxEntry is one queued effect. Seq is assigned by SQLite and never
// reused, so entries can be drained in commit order.
type OutboxEntry struct {
	Seq       int64
	Key       string
	Kind      string
	Payload   []byte
	CreatedAt int64
}

const createCommandOutboxSQL = `
CREATE TABLE IF NOT EXISTS command_intents (
  key        TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS command_outbox (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  key        TEXT NOT NULL UNIQUE,
  kind       TEXT NOT NULL,
  payload    BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
`

// OutboxStore adapts the state file to the command outbox port. It does not
// import the app layer; the method set is the port.
type OutboxStore struct {
	db *DB

	// probe runs inside the write transaction, after both inserts, before
	// commit. It is nil in production. Tests use it to observe that the
	// effect is not visible to a reader until commit, and to force a
	// rollback by returning an error.
	probe func() error
}

// NewOutboxStore builds the store adapter over db.
func NewOutboxStore(db *DB) *OutboxStore {
	return &OutboxStore{db: db}
}

// Commit records key, kind and payload as both the command intent and one
// outbox entry in one transaction. A repeat with the same key inserts nothing
// and reports created=false. Commit returns after the transaction commits, so
// any effect started after it returns is necessarily after the commit.
func (s *OutboxStore) Commit(ctx context.Context, key, kind string, payload []byte) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("store: outbox: nil db")
	}
	if strings.TrimSpace(key) == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput, "store: outbox: empty command key")
	}
	if strings.TrimSpace(kind) == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput, "store: outbox: empty effect kind")
	}

	if payload == nil {
		payload = []byte{}
	}
	clean := Redact(payload)
	now := time.Now().UTC().UnixNano()
	created := false
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createCommandOutboxSQL); err != nil {
			return fmt.Errorf("store: outbox: create tables: %w", err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO command_intents (key, kind, payload, created_at)
			VALUES (?, ?, ?, ?)`, key, kind, clean, now)
		if err != nil {
			return fmt.Errorf("store: outbox: insert intent: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: outbox: intent rows affected: %w", err)
		}
		if n == 0 {
			created = false
			return nil
		}
		if _, err := tx.Exec(`
			INSERT INTO command_outbox (key, kind, payload, created_at)
			VALUES (?, ?, ?, ?)`, key, kind, clean, now); err != nil {
			return fmt.Errorf("store: outbox: insert entry: %w", err)
		}
		created = true
		if s.probe != nil {
			if err := s.probe(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// OutboxEntry returns the committed effect for key.
func (s *OutboxStore) OutboxEntry(key string) (OutboxEntry, bool, error) {
	if s == nil || s.db == nil {
		return OutboxEntry{}, false, errors.New("store: outbox: nil db")
	}
	ok, err := tableExists(context.Background(), s.db.readDB, "command_outbox")
	if err != nil || !ok {
		return OutboxEntry{}, false, err
	}
	var e OutboxEntry
	err = s.db.readDB.QueryRow(`
		SELECT seq, key, kind, payload, created_at FROM command_outbox WHERE key = ?`, key,
	).Scan(&e.Seq, &e.Key, &e.Kind, &e.Payload, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxEntry{}, false, nil
	}
	if err != nil {
		return OutboxEntry{}, false, err
	}
	return e, true, nil
}

// Intent returns the committed intent for key.
func (s *OutboxStore) Intent(key string) (OutboxIntent, bool, error) {
	if s == nil || s.db == nil {
		return OutboxIntent{}, false, errors.New("store: outbox: nil db")
	}
	ok, err := tableExists(context.Background(), s.db.readDB, "command_intents")
	if err != nil || !ok {
		return OutboxIntent{}, false, err
	}
	var i OutboxIntent
	err = s.db.readDB.QueryRow(`
		SELECT key, kind, payload, created_at FROM command_intents WHERE key = ?`, key,
	).Scan(&i.Key, &i.Kind, &i.Payload, &i.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxIntent{}, false, nil
	}
	if err != nil {
		return OutboxIntent{}, false, err
	}
	return i, true, nil
}

// Counts returns the number of committed intents and outbox entries. A repeat
// command must not raise either count.
func (s *OutboxStore) Counts() (intents, outbox int, err error) {
	if s == nil || s.db == nil {
		return 0, 0, errors.New("store: outbox: nil db")
	}
	for _, table := range []struct {
		name string
		dst  *int
	}{{"command_intents", &intents}, {"command_outbox", &outbox}} {
		ok, existsErr := tableExists(context.Background(), s.db.readDB, table.name)
		if existsErr != nil {
			return 0, 0, existsErr
		}
		if !ok {
			continue
		}
		if err := s.db.readDB.QueryRow(`SELECT count(*) FROM ` + table.name).Scan(table.dst); err != nil {
			return 0, 0, err
		}
	}
	return intents, outbox, nil
}
