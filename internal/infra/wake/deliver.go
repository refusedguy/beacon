// Package wake adapts Beacon's wake pointer to the host harness. It uses the
// OpenCode HTTP API through `opencode api`, the same path the CLI and TUI use,
// so no plugin and no separate HTTP client are needed (ADR 0075, T3.203).
//
// This file covers the deferred-task case (ADR 0076, T3.201): a task already
// parked in the session inbox with delivery queue (or admitted with resume
// false) is turned into a wake by switching its delivery to steer. The task
// text is never sent again, so switching back to queue cannot lose it.
package wake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

const (
	deliverySteer = "steer"
	deliveryQueue = "queue"
)

// API runs `opencode api` commands. Runner is replaceable in tests; nil means
// exec.CommandContext. Keeping one seam lets the same code drive GET and PATCH.
type API struct {
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

func (a *API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a.Runner != nil {
		return a.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// InboxItem is one undelivered message from GET /api/session/{sessionID}/inbox.
// Text is kept so callers can confirm the original task is still present after
// a delivery switch, without Beacon re-sending it.
type InboxItem struct {
	ID       string `json:"id"`
	Delivery string `json:"delivery"`
	Text     string `json:"text"`
}

// Inbox reads GET /api/session/{sessionID}/inbox. The v2 route answers with a
// {"data":[...]} envelope; a bare array is accepted as well.
func (a *API) Inbox(ctx context.Context, sessionID string) ([]InboxItem, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox"
	out, err := a.run(ctx, "api", "GET", path)
	if err != nil {
		return nil, fmt.Errorf("wake: opencode api GET inbox: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "[") {
		var items []InboxItem
		if err := json.Unmarshal(out, &items); err != nil {
			return nil, fmt.Errorf("wake: decode inbox: %w", err)
		}
		return items, nil
	}
	var envelope struct {
		Data []InboxItem `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("wake: decode inbox: %w", err)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("wake: decode inbox: no data field")
	}
	return envelope.Data, nil
}

// FindInboxItem returns a pointer to the item with the given id, or nil when
// the id is not in the inbox. inboxID is the data.id returned by prompt.
func FindInboxItem(items []InboxItem, inboxID string) *InboxItem {
	if inboxID == "" {
		return nil
	}
	for i := range items {
		if items[i].ID == inboxID {
			return &items[i]
		}
	}
	return nil
}

// SetDelivery patches the delivery mode of a pending inbox item. The route is
// PATCH /api/session/{sessionID}/inbox/{inboxID} with {"delivery":...} and
// answers 204. A non-2xx status surfaces as an error from the Runner; an empty
// output on success is the expected 204 body.
func (a *API) SetDelivery(ctx context.Context, sessionID, inboxID, delivery string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if inboxID == "" {
		return fmt.Errorf("wake: empty inbox id")
	}
	if delivery != deliverySteer && delivery != deliveryQueue {
		return fmt.Errorf("wake: invalid delivery %q", delivery)
	}
	payload, err := json.Marshal(map[string]string{"delivery": delivery})
	if err != nil {
		return err
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox/" + url.PathEscape(inboxID)
	if _, err := a.run(ctx, "api", "PATCH", path, "--data", string(payload)); err != nil {
		return fmt.Errorf("wake: opencode api PATCH inbox: %w", err)
	}
	return nil
}

// SteerPending turns an already-deferred task into a wake. It first locates the
// item in the inbox so a stale or wrong id is reported instead of being logged
// as a successful wake. The task text stays untouched.
func (a *API) SteerPending(ctx context.Context, sessionID, inboxID string) error {
	items, err := a.Inbox(ctx, sessionID)
	if err != nil {
		return err
	}
	if FindInboxItem(items, inboxID) == nil {
		return fmt.Errorf("wake: inbox item %q not found in session %q", inboxID, sessionID)
	}
	return a.SetDelivery(ctx, sessionID, inboxID, deliverySteer)
}

// QueuePending returns a steered task to the queue without touching its text,
// so the same call in reverse simply parks it again.
func (a *API) QueuePending(ctx context.Context, sessionID, inboxID string) error {
	return a.SetDelivery(ctx, sessionID, inboxID, deliveryQueue)
}

func validateSessionID(sessionID string) error {
	if !strings.HasPrefix(sessionID, "ses_") || len(sessionID) == len("ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	return nil
}
