// Package wake adapts Beacon's wake pointer to the host harness and records
// every delivery under a stable deliveryId. ADR 0081 and card T3.206 require
// that a repeated webhook or a retried steer after a network error never
// starts a second job: the delivery is deduplicated by deliveryId before any
// transport call, and an Idempotency-Key reused with a different body is
// rejected the way Cursor answers 422.
package wake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WakeKind is the shape of a wake, following the primitives from ADR 0085.
// Beacon never invents a verdict from it.
type WakeKind string

const (
	KindNewRun   WakeKind = "newRun"
	KindSteer    WakeKind = "steer"
	KindFollowUp WakeKind = "followUp"
	KindSignal   WakeKind = "signal"
	KindResume   WakeKind = "resume"
	KindNoop     WakeKind = "noop"
)

// DeliveryStatus is the honest outcome of a wake attempt. The vocabulary is
// shared with ADR 0085. duplicate is what Pi reports for a repeated requestId
// and what Kilo collapses into pendingWake.
type DeliveryStatus string

const (
	StatusStarted     DeliveryStatus = "started"
	StatusQueued      DeliveryStatus = "queued"
	StatusDelivered   DeliveryStatus = "delivered"
	StatusDuplicate   DeliveryStatus = "duplicate"
	StatusClosed      DeliveryStatus = "closed"
	StatusUnsupported DeliveryStatus = "unsupported"
)

// Errors returned before the transport runs, so a caller can tell a
// deduplicated delivery from a malformed or conflicting one.
var (
	ErrDeliveryIDRequired  = errors.New("wake: deliveryId is required")
	ErrDeliveryIDReuse     = errors.New("wake: deliveryId reused with a different body")
	ErrIdempotencyConflict = errors.New("wake: idempotency key reused with a different body")
)

// WakeRequest is the unit of delivery. DeliveryID is mandatory: for an
// incoming webhook it is the X-Webhook-ID header; for an outbound steer it is
// generated once and kept on retry. IdempotencyKey is optional and is checked
// against the body hash recorded for the first delivery.
type WakeRequest struct {
	DeliveryID     string    `json:"deliveryId"`
	IdempotencyKey string    `json:"idempotencyKey,omitempty"`
	Target         string    `json:"target"`
	Kind           WakeKind  `json:"kind"`
	Body           []byte    `json:"body,omitempty"`
	At             time.Time `json:"at,omitempty"`
}

// DeliveryResult reports what the deliverer did. Coalesced is true when the
// request matched a journal entry and no second job was started.
type DeliveryResult struct {
	DeliveryID string         `json:"deliveryId"`
	Status     DeliveryStatus `json:"status"`
	Coalesced  bool           `json:"coalesced"`
}

// Transport performs the external side effect. It is a function so the
// journal can be tested without a real webhook or steer channel.
type Transport func(ctx context.Context, req WakeRequest) (DeliveryStatus, error)

const (
	// HeaderWebhookID is Cursor's unique per-delivery identifier.
	HeaderWebhookID = "X-Webhook-ID"
	// HeaderIdempotencyKey is checked against the body of the first delivery.
	HeaderIdempotencyKey = "Idempotency-Key"
)

// WakeRequestFromWebhook lifts deliveryId and the idempotency key from the
// incoming headers. A webhook without X-Webhook-ID is rejected: accepting it
// would let a retry start a second job.
func WakeRequestFromWebhook(h http.Header, target string, kind WakeKind, body []byte) (WakeRequest, error) {
	id := strings.TrimSpace(h.Get(HeaderWebhookID))
	if id == "" {
		return WakeRequest{}, ErrDeliveryIDRequired
	}
	return WakeRequest{
		DeliveryID:     id,
		IdempotencyKey: strings.TrimSpace(h.Get(HeaderIdempotencyKey)),
		Target:         target,
		Kind:           kind,
		Body:           body,
		At:             time.Now().UTC(),
	}, nil
}

type deliveryRecord struct {
	deliveryID     string
	bodyHash       string
	idempotencyKey string
	status         DeliveryStatus
	seenAt         time.Time
}

// DeliveryJournal is the bounded ledger of delivered IDs. A real adapter can
// back it with durable storage; the contract is the same: one deliveryId maps
// to one recorded body hash and one outcome.
type DeliveryJournal struct {
	mu    sync.Mutex
	byID  map[string]deliveryRecord
	byKey map[string]string
}

func NewDeliveryJournal() *DeliveryJournal {
	return &DeliveryJournal{
		byID:  make(map[string]deliveryRecord),
		byKey: make(map[string]string),
	}
}

// Deliverer wraps a transport with the delivery journal. It never calls the
// transport twice for the same deliveryId, and it records the key before the
// external call so a concurrent retry sees the first attempt already staged.
type Deliverer struct {
	Journal   *DeliveryJournal
	Transport Transport
}

func (d *Deliverer) Deliver(ctx context.Context, req WakeRequest) (DeliveryResult, error) {
	if d == nil || d.Journal == nil {
		return DeliveryResult{}, errors.New("wake: deliverer has no journal")
	}
	if strings.TrimSpace(req.DeliveryID) == "" {
		return DeliveryResult{}, ErrDeliveryIDRequired
	}
	if d.Transport == nil {
		return DeliveryResult{}, errors.New("wake: deliverer has no transport")
	}

	hash := bodyHash(req.Body)
	j := d.Journal
	j.mu.Lock()

	if rec, ok := j.byID[req.DeliveryID]; ok {
		j.mu.Unlock()
		if rec.bodyHash != hash {
			return DeliveryResult{}, fmt.Errorf("%w: %s", ErrDeliveryIDReuse, req.DeliveryID)
		}
		return DeliveryResult{DeliveryID: rec.deliveryID, Status: StatusDuplicate, Coalesced: true}, nil
	}
	if req.IdempotencyKey != "" {
		if id, ok := j.byKey[req.IdempotencyKey]; ok {
			rec := j.byID[id]
			j.mu.Unlock()
			if rec.bodyHash != hash {
				return DeliveryResult{}, fmt.Errorf("%w: %s", ErrIdempotencyConflict, req.IdempotencyKey)
			}
			return DeliveryResult{DeliveryID: rec.deliveryID, Status: StatusDuplicate, Coalesced: true}, nil
		}
	}

	// Fix the key before the external call, as api.memo in Pi before park.
	now := time.Now().UTC()
	j.byID[req.DeliveryID] = deliveryRecord{
		deliveryID:     req.DeliveryID,
		bodyHash:       hash,
		idempotencyKey: req.IdempotencyKey,
		status:         StatusQueued,
		seenAt:         now,
	}
	if req.IdempotencyKey != "" {
		j.byKey[req.IdempotencyKey] = req.DeliveryID
	}
	j.mu.Unlock()

	status, err := d.Transport(ctx, req)
	if err != nil {
		// The staged entry stays in the journal: a retry after a network
		// error is a duplicate, never a second job.
		return DeliveryResult{DeliveryID: req.DeliveryID}, err
	}

	j.mu.Lock()
	rec := j.byID[req.DeliveryID]
	rec.status = status
	j.byID[req.DeliveryID] = rec
	j.mu.Unlock()

	return DeliveryResult{DeliveryID: req.DeliveryID, Status: status}, nil
}

// Get returns the journaled outcome for a deliveryId without attempting a
// delivery. It is the read side used by observability and by tests.
func (j *DeliveryJournal) Get(deliveryID string) (DeliveryResult, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.byID[deliveryID]
	if !ok {
		return DeliveryResult{}, false
	}
	return DeliveryResult{DeliveryID: rec.deliveryID, Status: rec.status}, true
}

// Len reports how many distinct deliveries the journal has recorded.
func (j *DeliveryJournal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.byID)
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
