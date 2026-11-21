// Package store: one active link between a task card and the Watch and PR
// that work on it.
//
// The card_id primary key gives one row per card. A repeated Link cannot add
// a second row, even when callers race: every write goes through the single
// writer and INSERT OR IGNORE collapses the repeats. Unlink removes the row
// and does nothing when the card has no link. The table is created on first
// use, so the feature needs no migration and no change to db.go.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

const createCardLinksTableSQL = `
CREATE TABLE IF NOT EXISTS card_links (
  card_id  TEXT NOT NULL PRIMARY KEY,
  watch_id TEXT NOT NULL,
  pr       INTEGER NOT NULL
);
`

// CardLink is one stored link from a card to the Watch and PR working on it.
type CardLink struct {
	CardID  string
	WatchID string
	PR      int
}

// LinkResult reports what Link did. Created marks the first write. Duplicate
// marks a repeat: nothing was written and Link holds the stored row, which may
// carry a different Watch or PR than the caller passed.
type LinkResult struct {
	Link      CardLink
	Created   bool
	Duplicate bool
}

func ensureCardLinksTable(tx *sql.Tx) error {
	if _, err := tx.Exec(createCardLinksTableSQL); err != nil {
		return fmt.Errorf("store: card link: create table: %w", err)
	}
	return nil
}

// rowQuerier is already declared in webhook_events.go for both *sql.DB and
// *sql.Tx, so loadCardLink can read inside the write transaction.
func loadCardLink(q rowQuerier, cardID string) (CardLink, error) {
	var link CardLink
	err := q.QueryRow(`
		SELECT card_id, watch_id, pr FROM card_links WHERE card_id = ?`, cardID,
	).Scan(&link.CardID, &link.WatchID, &link.PR)
	return link, err
}

func validateCardLinkID(cardID string) error {
	if strings.TrimSpace(cardID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: card link: empty card id")
	}
	return nil
}

// Link stores the Watch and PR for one card. It is idempotent by card id: a
// repeat returns the stored row and leaves the table unchanged. The table
// check, insert and read of an existing row share one write transaction, so
// concurrent callers get exactly one Created result and one row.
func (d *DB) Link(cardID, watchID string, pr int) (LinkResult, error) {
	if err := validateCardLinkID(cardID); err != nil {
		return LinkResult{}, err
	}
	if strings.TrimSpace(watchID) == "" {
		return LinkResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: card link: empty watch id")
	}
	link := CardLink{CardID: cardID, WatchID: watchID, PR: pr}
	var out LinkResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureCardLinksTable(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO card_links (card_id, watch_id, pr)
			VALUES (?, ?, ?)`, cardID, watchID, pr)
		if err != nil {
			return fmt.Errorf("store: card link: insert: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: card link: rows affected: %w", err)
		}
		if n == 1 {
			out = LinkResult{Link: link, Created: true}
			return nil
		}
		stored, err := loadCardLink(tx, cardID)
		if err != nil {
			return fmt.Errorf("store: card link: load existing: %w", err)
		}
		out = LinkResult{Link: stored, Duplicate: true}
		return nil
	})
	if err != nil {
		return LinkResult{}, err
	}
	return out, nil
}

// Unlink removes the link for one card. A card with no link is a no-op. The
// delete goes through the single writer, so a concurrent Link either happens
// first and is removed, or happens after and remains as the only row.
func (d *DB) Unlink(cardID string) (bool, error) {
	if err := validateCardLinkID(cardID); err != nil {
		return false, err
	}
	var removed bool
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureCardLinksTable(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM card_links WHERE card_id = ?`, cardID)
		if err != nil {
			return fmt.Errorf("store: card link: delete: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: card link: rows affected: %w", err)
		}
		removed = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}
