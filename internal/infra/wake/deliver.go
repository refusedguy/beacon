// Package wake adapts Beacon's wake pointer to the host harness through the
// OpenCode HTTP API. The same `opencode api` path the CLI and TUI use drives
// both parking a task in the inbox and turning it into a steer wake.
//
// T3.204 adds the negative control: a parked task with delivery queue must not
// start execution. Only the PATCH that switches delivery to steer may produce
// session.execution.started, followed by session.idle.
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

// Host event types observed on the session event bus.
const (
	EventInboxEnqueued    = "session.inbox.enqueued"
	EventDeliveryChanged  = "session.inbox.delivery.changed"
	EventExecutionStarted = "session.execution.started"
	EventIdle             = "session.idle"
)

// Pointer carries only identifiers. No check body, PR title or other
// untrusted text is ever put on the wire (T3.200).
type Pointer struct {
	WatchID      string
	ObligationID string
	Revision     int
	Verdict      string
}

// API runs `opencode api` commands. Runner is replaceable in tests; nil means
// exec.CommandContext. One seam covers prompt, inbox, interrupt and active.
type API struct {
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

func (a *API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a != nil && a.Runner != nil {
		return a.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// Envelope renders the pointer as the host's wake text. Every field is
// whitelisted, so the envelope cannot carry an instruction or a leaked fact.
func Envelope(p Pointer) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"watchID":      p.WatchID,
		"obligationID": p.ObligationID,
		"revision":     p.Revision,
		"verdict":      p.Verdict,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}

// Prompt posts a message to a session with the given delivery mode and returns
// data.id. The same id is the inbox item id used by the PATCH below.
func (a *API) Prompt(ctx context.Context, sessionID string, p Pointer, delivery string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	if delivery != deliverySteer && delivery != deliveryQueue {
		return "", fmt.Errorf("wake: invalid delivery %q", delivery)
	}
	body := map[string]any{
		"text":     Envelope(p),
		"delivery": delivery,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/prompt"
	out, err := a.run(ctx, "api", "POST", path, "--data", string(raw))
	if err != nil {
		return "", fmt.Errorf("wake: opencode api POST prompt: %w", err)
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return "", fmt.Errorf("wake: decode prompt response: %w", err)
	}
	if ack.Data.ID == "" {
		return "", fmt.Errorf("wake: prompt response has no admitted message id")
	}
	return ack.Data.ID, nil
}

// Deliver posts a steer message into an existing session (T3.200).
func (a *API) Deliver(ctx context.Context, sessionID string, p Pointer) error {
	_, err := a.Prompt(ctx, sessionID, p, deliverySteer)
	return err
}

// Park posts a queue message and returns its inbox id. A parked task stays
// idle until a delivery switch wakes it (T3.201).
func (a *API) Park(ctx context.Context, sessionID string, p Pointer) (string, error) {
	return a.Prompt(ctx, sessionID, p, deliveryQueue)
}

// InboxItem is one undelivered message from GET /api/session/{id}/inbox.
// Text is kept so a delivery switch can be checked against the original task.
type InboxItem struct {
	ID       string `json:"id"`
	Delivery string `json:"delivery"`
	Text     string `json:"text"`
}

// Inbox reads GET /api/session/{id}/inbox. Both {"data":[...]} and a bare
// array are accepted.
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

// FindInboxItem returns the item with the given id, or nil when it is absent.
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

// SetDelivery patches a pending inbox item. PATCH returns 204 with no body.
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

// SteerPending switches a parked item to steer after confirming it is still
// in the inbox. The task text is never sent again.
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

// QueuePending switches a steered item back to queue without losing its text.
func (a *API) QueuePending(ctx context.Context, sessionID, inboxID string) error {
	return a.SetDelivery(ctx, sessionID, inboxID, deliveryQueue)
}

// Interrupt POSTs to the session interrupt route and reports the host's
// interrupted flag. An idle session answers interrupted=false.
func (a *API) Interrupt(ctx context.Context, sessionID string) (bool, error) {
	if err := validateSessionID(sessionID); err != nil {
		return false, err
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/interrupt"
	out, err := a.run(ctx, "api", "POST", path)
	if err != nil {
		return false, fmt.Errorf("wake: opencode api POST interrupt: %w", err)
	}
	var ack struct {
		Interrupted bool `json:"interrupted"`
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return false, fmt.Errorf("wake: decode interrupt response: %w", err)
	}
	return ack.Interrupted, nil
}

// Active returns session ids from GET /api/session/active. An idle session is
// absent from this list.
func (a *API) Active(ctx context.Context) ([]string, error) {
	out, err := a.run(ctx, "api", "GET", "/api/session/active")
	if err != nil {
		return nil, fmt.Errorf("wake: opencode api GET active: %w", err)
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("wake: decode active sessions: %w", err)
	}
	ids := make([]string, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

// SessionEvent is one event observed on the host event bus.
type SessionEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
}

// VerifySteerCausality proves that execution for sessionID was caused by the
// steer switch and not by unrelated activity. It requires a delivery.changed
// event, no execution.started before it, and an execution.started followed by
// session.idle afterwards. A queue-only session fails every requirement.
func VerifySteerCausality(events []SessionEvent, sessionID string) error {
	steer := -1
	for i, event := range events {
		if event.SessionID == sessionID && event.Type == EventDeliveryChanged {
			steer = i
			break
		}
	}
	if steer < 0 {
		return fmt.Errorf("wake: no %s for %s", EventDeliveryChanged, sessionID)
	}
	for _, event := range events[:steer] {
		if event.SessionID == sessionID && event.Type == EventExecutionStarted {
			return fmt.Errorf("wake: %s before steer for %s", EventExecutionStarted, sessionID)
		}
	}
	started := -1
	for i, event := range events[steer+1:] {
		if event.SessionID == sessionID && event.Type == EventExecutionStarted {
			started = steer + 1 + i
			break
		}
	}
	if started < 0 {
		return fmt.Errorf("wake: no %s after steer for %s", EventExecutionStarted, sessionID)
	}
	for _, event := range events[started+1:] {
		if event.SessionID == sessionID && event.Type == EventIdle {
			return nil
		}
	}
	return fmt.Errorf("wake: no %s after %s for %s", EventIdle, EventExecutionStarted, sessionID)
}

func validateSessionID(sessionID string) error {
	if !strings.HasPrefix(sessionID, "ses_") || len(sessionID) == len("ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	return nil
}
