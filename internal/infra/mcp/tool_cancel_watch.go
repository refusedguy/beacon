// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_cancel_watch (WORKER-PROMPT §4.1): a destructive,
// idempotent cancel that closes a whole Watch or removes one subscriber while
// the event history is kept. A cancelled Watch never becomes ready: closed is
// terminal in the domain state machine (§4.2 non-invariant 4, ADR 0019).
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// CancelScopeAll closes the Watch for every subscriber. An omitted scope
	// means all, as defined by WORKER-PROMPT §4.1.
	CancelScopeAll = "all"
	// CancelScopeSubscriber removes only the calling subscriber. The caller is
	// identified by its opaque sessionID; the core never interprets it.
	CancelScopeSubscriber = "subscriber"

	cancelWatchMetaKey       = "ai.opencode/sessionID"
	cancelWatchEventKind     = "cancel"
	cancelWatchSchemaVersion = 2
	cancelSubscriberReason   = "subscriber_cancelled"
)

// CancelWatchInput is the argument object of beacon_cancel_watch. Scope is
// optional and defaults to "all" when empty.
type CancelWatchInput struct {
	WatchID string `json:"watchID" jsonschema:"identifier of the Watch to cancel"`
	Scope   string `json:"scope,omitempty" jsonschema:"subscriber or all; all is the default"`
}

// CancelWatchOutput is the ok-only response of beacon_cancel_watch. The JSON
// shape matches the shared OKOutput used by beacon_ack (WORKER-PROMPT §4.1).
type CancelWatchOutput struct {
	OK bool `json:"ok"`
}

// CancelAuditEvent is one append-only row in the Watch history. Earlier rows
// are never removed by a cancel, so the history survives the operation.
type CancelAuditEvent struct {
	Kind         string    `json:"kind"`
	WatchID      string    `json:"watchID"`
	Scope        string    `json:"scope"`
	SubscriberID string    `json:"subscriberID"`
	Revision     uint64    `json:"revision"`
	CloseReason  string    `json:"closeReason,omitempty"`
	At           time.Time `json:"at"`
}

// CancelEventLog is the append-only history port used by CancelWatcher.
type CancelEventLog interface {
	Append(ctx context.Context, e CancelAuditEvent) error
}

// cancelWatchStore is the part of domain.WatchStore that cancellation needs.
type cancelWatchStore interface {
	Watch(id string) (domain.Watch, error)
	SaveWatch(w domain.Watch) error
}

// cancelTxWriter is the single-writer port exposed by the store. It lets the
// event log write through the same connection as every other mutation without
// this package importing the store implementation.
type cancelTxWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

type sqlCancelEventLog struct {
	w cancelTxWriter
}

// NewSQLCancelEventLog builds the production history writer. w is normally the
// store DB, whose Write method serializes every mutation on one connection.
func NewSQLCancelEventLog(w cancelTxWriter) CancelEventLog {
	return &sqlCancelEventLog{w: w}
}

func (l *sqlCancelEventLog) Append(ctx context.Context, e CancelAuditEvent) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil || l.w == nil {
		return domain.NewAppError(domain.CodePersistFailed, "cancel_watch: event writer is nil")
	}
	return l.w.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO events (watch_id, schema_version, payload, created_at)
			VALUES (?, ?, ?, ?)`,
			e.WatchID, cancelWatchSchemaVersion, payload, e.At.UnixNano())
		return err
	})
}

// CancelWatcher applies destructive, idempotent cancellation to Watches. It
// uses the domain state machine for every state change, so a cancelled Watch
// cannot transition to ready.
type CancelWatcher struct {
	store  cancelWatchStore
	events CancelEventLog
	clock  domain.Clock
}

// NewCancelWatcher builds a CancelWatcher over the given ports. A nil clock
// falls back to the wall clock.
func NewCancelWatcher(store cancelWatchStore, events CancelEventLog, clock domain.Clock) *CancelWatcher {
	if clock == nil {
		clock = cancelWallClock{}
	}
	return &CancelWatcher{store: store, events: events, clock: clock}
}

type cancelWallClock struct{}

func (cancelWallClock) Now() time.Time { return time.Now() }

// Cancel closes watchID when scope is all, or removes the calling subscriber
// when scope is subscriber. Repeating either call is a no-op that returns ok:
// an already closed Watch stays closed and an already removed subscriber is
// simply absent. A state change appends one audit event and persists the Watch
// before returning; no history row is ever deleted.
func (c *CancelWatcher) Cancel(ctx context.Context, watchID, scope, subscriberID string) error {
	if watchID == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "cancel_watch: watchID is required")
	}
	switch scope {
	case "", CancelScopeAll:
		scope = CancelScopeAll
	case CancelScopeSubscriber:
		if subscriberID == "" {
			return domain.NewAppError(domain.CodeInvalidInput, "cancel_watch: subscriber scope needs a caller sessionID")
		}
	default:
		return domain.NewAppError(domain.CodeInvalidInput, "cancel_watch: scope must be subscriber or all")
	}
	if c == nil || c.store == nil {
		return domain.NewAppError(domain.CodePersistFailed, "cancel_watch: store is nil")
	}

	w, err := c.store.Watch(watchID)
	if err != nil {
		return err
	}
	if w.Status == domain.StatusClosed {
		// Terminal state. Idempotent repeat: no write and no second event.
		return nil
	}

	now := c.clock.Now()
	if scope == CancelScopeAll {
		return c.closeAndRecord(ctx, w, now, CancelAuditEvent{
			Kind:     cancelWatchEventKind,
			WatchID:  w.ID,
			Scope:    CancelScopeAll,
			Revision: w.Revision,
			At:       now,
		}, domain.CloseReasonCancelled)
	}

	remaining, found := removeCancelSubscriber(w.Subscribers, subscriberID)
	if !found {
		// The subscriber already cancelled. Idempotent repeat.
		return nil
	}

	next := w
	next.Subscribers = remaining
	if len(remaining) == 0 {
		return c.closeAndRecord(ctx, next, now, CancelAuditEvent{
			Kind:         cancelWatchEventKind,
			WatchID:      next.ID,
			Scope:        CancelScopeSubscriber,
			SubscriberID: subscriberID,
			Revision:     next.Revision,
			At:           now,
		}, domain.CloseReasonCancelled)
	}

	if err := c.store.SaveWatch(next); err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return c.events.Append(ctx, CancelAuditEvent{
		Kind:         cancelWatchEventKind,
		WatchID:      next.ID,
		Scope:        CancelScopeSubscriber,
		SubscriberID: subscriberID,
		Revision:     next.Revision,
		CloseReason:  cancelSubscriberReason,
		At:           now,
	})
}

// closeAndRecord applies the terminal cancel transition through the domain and
// records one audit row. The Watch is persisted before the event is appended,
// so the caller is only answered after both mutations happened.
func (c *CancelWatcher) closeAndRecord(ctx context.Context, w domain.Watch, now time.Time, ev CancelAuditEvent, reason domain.CloseReason) error {
	next, _ := domain.Transition(w, domain.CancelEvent(now))
	if next.Status != domain.StatusClosed || next.CloseReason != string(reason) {
		return domain.NewAppError(domain.CodeInvalidInput, "cancel_watch: Watch could not be closed")
	}
	if err := c.store.SaveWatch(next); err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	ev.CloseReason = string(reason)
	if c.events == nil {
		return domain.NewAppError(domain.CodePersistFailed, "cancel_watch: event log is nil")
	}
	return c.events.Append(ctx, ev)
}

// removeCancelSubscriber returns the subscriber list without id. The boolean
// reports whether id was present, which drives idempotent repeats.
func removeCancelSubscriber(subscribers []domain.Subscriber, id string) ([]domain.Subscriber, bool) {
	kept := make([]domain.Subscriber, 0, len(subscribers))
	found := false
	for _, s := range subscribers {
		if s.ID == id {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return subscribers, false
	}
	return kept, true
}

// cancelWatchCallerSessionID reads the opaque sessionID from _meta. The core
// never parses it and never uses it for access (WORKER-PROMPT §2, ADR 0001).
func cancelWatchCallerSessionID(req *mcpsdk.CallToolRequest) string {
	if req == nil || req.Params == nil || req.Params.Meta == nil {
		return ""
	}
	v, ok := req.Params.Meta[cancelWatchMetaKey]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// CancelWatchHandler adapts CancelWatcher to a typed MCP tool handler. The
// caller's subscriber id is the opaque sessionID from _meta.
func CancelWatchHandler(svc *CancelWatcher) mcpsdk.ToolHandlerFor[CancelWatchInput, CancelWatchOutput] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in CancelWatchInput) (*mcpsdk.CallToolResult, CancelWatchOutput, error) {
		var subscriberID string
		if in.Scope == CancelScopeSubscriber {
			subscriberID = cancelWatchCallerSessionID(req)
		}
		if err := svc.Cancel(ctx, in.WatchID, in.Scope, subscriberID); err != nil {
			return &mcpsdk.CallToolResult{}, CancelWatchOutput{}, err
		}
		return &mcpsdk.CallToolResult{}, CancelWatchOutput{OK: true}, nil
	}
}

// RegisterCancelWatch registers the tool with honest destructive and
// idempotent annotations. The server registers a stub at startup; calling this
// helper replaces that stub with the real handler.
func RegisterCancelWatch(s *mcpsdk.Server, svc *CancelWatcher) {
	destructive := true
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "beacon_cancel_watch",
		Description: "Cancel a Watch or one subscriber; idempotent, history is kept.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &destructive,
			IdempotentHint:  true,
		},
	}, CancelWatchHandler(svc))
}
