// Package store: append-once journal of GitHub webhook deliveries (ADR 0150).
//
// GitHub sends X-GitHub-Delivery as a GUID that is unique to one event and is
// repeated byte-for-byte when the delivery is redelivered. Recording that key
// once, in the same transaction as the row, makes redelivery a no-op: a repeat
// inserts nothing, leaves the first row's status and outcome intact and
// returns the stored outcome (ADR 0015).
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

// WebhookDeliveryKey builds the durable idempotency key for one GitHub
// delivery. Redelivering the same X-GitHub-Delivery yields the same key, so
// RecordDelivery cannot write a second row for it. The key is distinct from a
// task start, a client request and a completion notice with the same raw value.
func WebhookDeliveryKey(deliveryID string) string {
	return domain.IdempotencyKey(domain.GitHubDeliveryNamespace, deliveryID)
}

// WebhookDelivery is one stored GitHub webhook delivery. DeliveryID is the
// namespaced idempotency key from WebhookDeliveryKey, not the raw header value.
type WebhookDelivery struct {
	DeliveryID string
	Event      string
	Action     string
	Repo       string
	PR         int
	HeadSHA    string
	WatchID    string
	Revision   int64
	Status     string
	ReceivedAt int64
	Outcome    string
}

// RecordDeliveryResult reports what RecordDelivery did. Created marks the
// first write. Duplicate marks a repeat: nothing was written and the first row
// is returned. Conflict marks a repeat whose body differs from the first; the
// stored row still wins, so a redelivery cannot roll the status back.
type RecordDeliveryResult struct {
	Delivery  WebhookDelivery
	Created   bool
	Duplicate bool
	Conflict  bool
}

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, so a delivery can be
// loaded inside the writer transaction or from the separate read pool with
// the same scan code.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// loadWebhookDelivery reads one journal row by its namespaced key.
func loadWebhookDelivery(q rowQuerier, key string) (WebhookDelivery, error) {
	var d WebhookDelivery
	err := q.QueryRow(`
		SELECT delivery_id, event, action, repo, pr, head_sha, watch_id,
		       revision, status, received_at, outcome
		FROM webhook_events WHERE delivery_id = ?`, key,
	).Scan(&d.DeliveryID, &d.Event, &d.Action, &d.Repo, &d.PR, &d.HeadSHA,
		&d.WatchID, &d.Revision, &d.Status, &d.ReceivedAt, &d.Outcome)
	return d, err
}

// webhookDeliveryBodyDiffers reports whether a repeat carries a different
// body. ReceivedAt is excluded: a redelivery naturally arrives later and that
// alone is not a conflict. DeliveryID is excluded because both sides carry the
// same namespaced key.
func webhookDeliveryBodyDiffers(stored WebhookDelivery, in WebhookDelivery) bool {
	return stored.Event != in.Event ||
		stored.Action != in.Action ||
		stored.Repo != in.Repo ||
		stored.PR != in.PR ||
		stored.HeadSHA != in.HeadSHA ||
		stored.WatchID != in.WatchID ||
		stored.Revision != in.Revision ||
		stored.Status != in.Status ||
		stored.Outcome != in.Outcome
}

// RecordDelivery records one GitHub delivery at most once. It namespaces the
// raw delivery ID, creates the table on first use and inserts in one
// transaction (ADR 0015). A repeat hits the primary key, inserts nothing and
// returns the first row, so neither the row count nor the stored status changes.
// A repeat with a different body is reported as a Conflict and still returns
// the stored outcome instead of overwriting it.
func (d *DB) RecordDelivery(in WebhookDelivery) (RecordDeliveryResult, error) {
	if strings.TrimSpace(in.DeliveryID) == "" {
		return RecordDeliveryResult{}, domain.NewAppError(domain.CodeInvalidInput, "store: webhook delivery: empty delivery id")
	}
	key := WebhookDeliveryKey(in.DeliveryID)
	at := in.ReceivedAt
	if at == 0 {
		at = time.Now().UTC().UnixNano()
	}
	var out RecordDeliveryResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createWebhookEventsSQL); err != nil {
			return fmt.Errorf("store: webhook delivery: create table: %w", err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO webhook_events
			  (delivery_id, event, action, repo, pr, head_sha, watch_id,
			   revision, status, received_at, outcome)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key, in.Event, in.Action, in.Repo, in.PR, in.HeadSHA,
			in.WatchID, in.Revision, in.Status, at, in.Outcome)
		if err != nil {
			return fmt.Errorf("store: webhook delivery: insert: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: webhook delivery: rows affected: %w", err)
		}
		if n == 1 {
			out = RecordDeliveryResult{
				Delivery: WebhookDelivery{
					DeliveryID: key, Event: in.Event, Action: in.Action,
					Repo: in.Repo, PR: in.PR, HeadSHA: in.HeadSHA,
					WatchID: in.WatchID, Revision: in.Revision, Status: in.Status,
					ReceivedAt: at, Outcome: in.Outcome,
				},
				Created: true,
			}
			return nil
		}
		stored, err := loadWebhookDelivery(tx, key)
		if err != nil {
			return fmt.Errorf("store: webhook delivery: load existing: %w", err)
		}
		out = RecordDeliveryResult{
			Delivery:  stored,
			Duplicate: true,
			Conflict:  webhookDeliveryBodyDiffers(stored, in),
		}
		return nil
	})
	if err != nil {
		return RecordDeliveryResult{}, err
	}
	return out, nil
}

// DeliverySeen reports whether a delivery was already recorded and returns its
// stored row. Reads use the separate read pool, so a process that reopens the
// same file after a restart sees the committed row without waiting on a writer.
func (d *DB) DeliverySeen(deliveryID string) (WebhookDelivery, bool, error) {
	if strings.TrimSpace(deliveryID) == "" {
		return WebhookDelivery{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: webhook delivery: empty delivery id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "webhook_events")
	if err != nil || !ok {
		return WebhookDelivery{}, false, err
	}
	delivery, err := loadWebhookDelivery(d.readDB, WebhookDeliveryKey(deliveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookDelivery{}, false, nil
	}
	if err != nil {
		return WebhookDelivery{}, false, err
	}
	return delivery, true, nil
}

// WebhookDeliveryCount returns the number of journal rows. A redelivery must
// not increase it. It is also the check that an empty file has no phantom row.
func (d *DB) WebhookDeliveryCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "webhook_events")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM webhook_events`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
