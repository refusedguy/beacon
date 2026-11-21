// Package store is the SQLite implementation of domain.Store.
//
// This file owns the failure_excerpts cache table. The MCP handler calls it
// through a narrow port and never imports this package (ADR 0005).
package store

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"
)

// FailureExcerptLimit is the byte ceiling of a cached excerpt. The MCP
// boundary applies the same limit before it writes a value here.
const FailureExcerptLimit = 2048

const failureExcerptsDDL = `CREATE TABLE IF NOT EXISTS failure_excerpts (
	watch_id   TEXT NOT NULL,
	head_sha   TEXT NOT NULL,
	check_name TEXT NOT NULL,
	excerpt    TEXT NOT NULL,
	PRIMARY KEY (watch_id, head_sha, check_name)
)`

// FailureExcerptCache stores one redacted excerpt per failed check. The key
// includes the head SHA so a new push starts with fresh annotations.
type FailureExcerptCache struct {
	db *DB
}

// NewFailureExcerptCache builds a cache over the store's single writer.
func NewFailureExcerptCache(db *DB) *FailureExcerptCache {
	if db == nil {
		return nil
	}
	return &FailureExcerptCache{db: db}
}

// Get reads one cached excerpt. A miss returns found false with a nil error,
// which tells the caller to fetch annotations from GitHub.
func (c *FailureExcerptCache) Get(ctx context.Context, watchID, headSHA, checkName string) (string, bool, error) {
	if c == nil || c.db == nil {
		return "", false, nil
	}
	var (
		excerpt string
		found   bool
	)
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(failureExcerptsDDL); err != nil {
			return err
		}
		err := tx.QueryRow(`SELECT excerpt FROM failure_excerpts
			WHERE watch_id = ? AND head_sha = ? AND check_name = ?`,
			watchID, headSHA, checkName).Scan(&excerpt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return excerpt, found, err
}

// Put stores one excerpt. An empty excerpt is stored too, so a check without
// annotations is not fetched again on the next poll. A conflicting row keeps
// the first answer.
func (c *FailureExcerptCache) Put(ctx context.Context, watchID, headSHA, checkName, excerpt string) error {
	if c == nil || c.db == nil {
		return nil
	}
	excerpt = boundExcerpt(excerpt)
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(failureExcerptsDDL); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO failure_excerpts
			(watch_id, head_sha, check_name, excerpt) VALUES (?, ?, ?, ?)
			ON CONFLICT(watch_id, head_sha, check_name) DO NOTHING`,
			watchID, headSHA, checkName, excerpt)
		return err
	})
}

// FailureExcerptGet exposes the cache on DB so an MCP handler can detect the
// capability without importing this package (ADR 0005).
func (d *DB) FailureExcerptGet(ctx context.Context, watchID, headSHA, checkName string) (string, bool, error) {
	return NewFailureExcerptCache(d).Get(ctx, watchID, headSHA, checkName)
}

// FailureExcerptPut exposes the write side of the same cache on DB.
func (d *DB) FailureExcerptPut(ctx context.Context, watchID, headSHA, checkName, excerpt string) error {
	return NewFailureExcerptCache(d).Put(ctx, watchID, headSHA, checkName, excerpt)
}

// boundExcerpt cuts at a UTF-8 boundary so a multi-byte character is never
// split in half.
func boundExcerpt(s string) string {
	if len(s) <= FailureExcerptLimit {
		return s
	}
	cut := s[:FailureExcerptLimit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
