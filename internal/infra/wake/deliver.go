// Package wake adapts Beacon's wake pointer to the host harness.
// Consolidated from PRs #281, #286, #282, #287, #285, #290, #291, #288, #283, #284, #289, #276.
package wake

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// ===== база wake (#281) =====
// Package wake adapts Beacon's wake pointer to the host harness. The default
// adapter uses the OpenCode HTTP API through `opencode api`, the same path the
// CLI and TUI use. It never invents a verdict and never merges a PR.

// OpenCodeDeliverer posts a steer message to an existing OpenCode session.
// The sessionID comes from the MCP caller's _meta, so Beacon already knows
// which agent to wake without a second registry.
type OpenCodeDeliverer struct {
	// Runner is replaceable in tests. Nil means exec.CommandContext.
	Runner func(ctx context.Context, args ...string) ([]byte, error)

	mu   sync.Mutex
	seen map[wakeDeliveryKey]struct{}
}

// wakeDeliveryKey is the pointer a wake carries. A repeated pointer must not
// start a second prompt in the same session (ADR 0028, T4.1).
type wakeDeliveryKey struct {
	watchID      string
	obligationID string
	revision     uint64
}

var _ domain.WakeDeliverer = (*OpenCodeDeliverer)(nil)

// Deliver emits a <beacon-event> envelope with only identifiers. The host's
// prompt admission treats the text as untrusted input, never as a command.
func (d *OpenCodeDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	// The obligation id is domain.ObligationKey(watchID, revision,
	// subscriberID), and the subscriber id is the session that registered the
	// watch. A pointer addressed to another subscriber must never wake this
	// session, even when the caller knows a valid session id.
	if want := domain.ObligationKey(p.WatchID, p.Revision, sessionID); p.ObligationID != want {
		return fmt.Errorf("wake: session %q is not the subscriber of obligation %q", sessionID, p.ObligationID)
	}
	key := wakeDeliveryKey{watchID: p.WatchID, obligationID: p.ObligationID, revision: p.Revision}
	d.mu.Lock()
	if d.seen == nil {
		d.seen = make(map[wakeDeliveryKey]struct{})
	}
	if _, duplicate := d.seen[key]; duplicate {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	body := map[string]any{
		"text":     Envelope(p),
		"delivery": "steer",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	args := []string{"api", "POST", "/api/session/" + sessionID + "/prompt", "--data", string(raw)}
	var out []byte
	if d.Runner != nil {
		out, err = d.Runner(ctx, args...)
	} else {
		out, err = exec.CommandContext(ctx, "opencode", args...).Output()
	}
	if err != nil {
		return fmt.Errorf("wake: opencode api: %w", err)
	}
	if err := verifyAck(out); err != nil {
		return err
	}
	d.mu.Lock()
	d.seen[key] = struct{}{}
	d.mu.Unlock()
	return nil
}

// Envelope renders the pointer into the host's wake text. Every field is
// whitelisted: no check body or PR title is included.
func Envelope(p domain.WakePayload) string {
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

// verifyAck checks that the API admitted the prompt. An error response from
// opencode api is already an exec error; this covers a 2xx with an unexpected
// shape so a silently dropped wake is not reported as delivered.
func verifyAck(out []byte) error {
	if len(out) == 0 {
		return fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return fmt.Errorf("wake: api response has no admitted message id")
	}
	return nil
}

// ===== T3.200 прямой POST session.prompt (#286) =====
// Package wake delivers a wake prompt to a parked opencode session through the
// HTTP API, with no plugin and no second protocol (ADR 0075). It owns the narrow
// POST /api/session/{sessionID}/prompt seam so callers only name a session, a
// text and an idempotency key. The package keeps no agent names and starts no
// server (ADR 0024, ADR 0029).

const (
	T200authUser = "opencode"

	// T200maxBodyBytes bounds the response body we are willing to read (ADR 0060 п6).
	T200maxBodyBytes = 1 << 20
)

// T200Delivery is the admission mode of one prompt. steer schedules the agent loop
// right away; queue only stores the input (ADR 0077). A wake defaults to steer
// because a wake must start execution, not park the session again.
type T200Delivery string

const (
	T200DeliverySteer T200Delivery = "steer"
	T200DeliveryQueue T200Delivery = "queue"
)

// T200Request is one wake delivery.
type T200Request struct {
	// SessionID is the watched session, used in the URL path.
	SessionID string
	// Text carries the new fact and the obligation id. It is sent verbatim.
	Text string
	// T200Delivery is steer or queue. An empty value means steer.
	T200Delivery T200Delivery
	// DeliveryID is the stable id of this wake. A repeat with the same id
	// returns the original admission and creates no second task. An empty id is
	// derived from SessionID and Text so identical calls collapse as well.
	DeliveryID string
}

// T200Result is the honest outcome of one admitted prompt.
type T200Result struct {
	// ID is data.id of the admission, the inbox id of the queued input.
	ID string
	// T200Delivery is the mode the session admitted, echoed from data.delivery.
	T200Delivery T200Delivery
	// Duplicate is true when the same DeliveryID was already admitted. The
	// first T200Result is returned unchanged, so no second task is created.
	Duplicate bool
}

// T200Doer is the single HTTP seam. *http.Client implements it, and tests pass a
// fake so no real server is needed.
type T200Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// T200Deliverer posts wake prompts to one opencode server and keeps a small
// in-memory journal of admissions by delivery id. The journal makes a repeated
// wake with the same id return the first result without a second POST.
type T200Deliverer struct {
	baseURL  string
	password string
	doer     T200Doer

	mu       sync.Mutex
	admitted map[string]T200Result
}

// T200New builds a T200Deliverer for the opencode server at baseURL. A nil doer means a
// real *http.Client. The address is validated like the thin client: only http
// and https with a host are accepted.
func T200New(baseURL, password string, doer T200Doer) (*T200Deliverer, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return nil, fmt.Errorf("wake: пустой url сервиса opencode")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("wake: неверный url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("wake: неверная схема url %q", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("wake: url без хоста %q", raw)
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &T200Deliverer{
		baseURL:  strings.TrimRight(raw, "/"),
		password: password,
		doer:     doer,
		admitted: make(map[string]T200Result),
	}, nil
}

// T200normalizeDelivery gives an absent mode its wake meaning.
func T200normalizeDelivery(d T200Delivery) T200Delivery {
	if d == "" {
		return T200DeliverySteer
	}
	return d
}

// T200validDelivery reports whether d is one of the two defined modes.
func T200validDelivery(d T200Delivery) bool {
	return d == T200DeliverySteer || d == T200DeliveryQueue
}

// T200idempotencyKey returns the caller's id or derives a stable one from the
// session and the text. The derivation keeps repeats without an explicit id
// from creating a second task.
func T200idempotencyKey(req T200Request) string {
	if req.DeliveryID != "" {
		return req.DeliveryID
	}
	sum := sha256.Sum256([]byte(req.SessionID + "\x00" + req.Text))
	return hex.EncodeToString(sum[:])
}

// Deliver admits one wake prompt and returns its inbox id. A repeat with the
// same idempotency key returns the first admission with Duplicate=true and
// sends no second request, so the session starts at most one task.
func (d *T200Deliverer) Deliver(ctx context.Context, req T200Request) (T200Result, error) {
	mode := T200normalizeDelivery(req.T200Delivery)
	if !T200validDelivery(mode) {
		return T200Result{}, fmt.Errorf("wake: неизвестный delivery %q", string(req.T200Delivery))
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return T200Result{}, fmt.Errorf("wake: пустой sessionID")
	}
	if req.Text == "" {
		return T200Result{}, fmt.Errorf("wake: пустой text")
	}
	if d == nil || d.doer == nil {
		return T200Result{}, fmt.Errorf("wake: не настроен HTTP-клиент")
	}
	key := T200idempotencyKey(req)

	d.mu.Lock()
	prior, seen := d.admitted[key]
	d.mu.Unlock()
	if seen {
		prior.Duplicate = true
		return prior, nil
	}

	payload := struct {
		Text     string   `json:"text"`
		T200Delivery T200Delivery `json:"delivery"`
	}{Text: req.Text, T200Delivery: mode}
	body, err := json.Marshal(payload)
	if err != nil {
		return T200Result{}, fmt.Errorf("wake: кодирование запроса: %v", err)
	}
	path := "/api/session/" + url.PathEscape(req.SessionID) + "/prompt"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return T200Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", key)
	httpReq.SetBasicAuth(T200authUser, d.password)

	resp, err := d.doer.Do(httpReq)
	if err != nil {
		return T200Result{}, err
	}
	if resp == nil {
		return T200Result{}, fmt.Errorf("wake: пустой ответ сервера")
	}

	result, duplicate, err := T200readAdmission(resp, mode)
	if err != nil {
		return T200Result{}, err
	}
	if duplicate {
		result.Duplicate = true
	}
	d.mu.Lock()
	d.admitted[key] = result
	d.mu.Unlock()
	return result, nil
}

// T200readAdmission parses one prompt response. It returns duplicate=true for a
// 409 duplicate id, taking the original admission from the body when present.
func T200readAdmission(resp *http.Response, mode T200Delivery) (T200Result, bool, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, T200maxBodyBytes))
	if err != nil {
		return T200Result{}, false, fmt.Errorf("wake: чтение ответа: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusConflict {
			// ADR 0077: 409 is a duplicate message id, not a busy session.
			original := T200parseAdmission(raw)
			if original.T200Delivery == "" {
				original.T200Delivery = mode
			}
			return original, true, nil
		}
		return T200Result{}, false, fmt.Errorf("wake POST: %s: %s", resp.Status, msg)
	}

	admission := T200parseAdmission(raw)
	if admission.ID == "" {
		return T200Result{}, false, fmt.Errorf("wake POST: в ответе нет data.id")
	}
	if admission.T200Delivery != mode {
		return T200Result{}, false, fmt.Errorf("wake POST: data.delivery = %q, ожидалось %q", string(admission.T200Delivery), string(mode))
	}
	return admission, false, nil
}

// T200parseAdmission reads {"data":{"id":...,"delivery":...}} and also accepts a
// bare object for handlers that omit the envelope.
func T200parseAdmission(raw []byte) T200Result {
	var envelope struct {
		Data struct {
			ID       string   `json:"id"`
			T200Delivery T200Delivery `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Data.ID != "" {
		return T200Result{ID: envelope.Data.ID, T200Delivery: envelope.Data.T200Delivery}
	}
	var bare struct {
		ID       string   `json:"id"`
		T200Delivery T200Delivery `json:"delivery"`
	}
	if err := json.Unmarshal(raw, &bare); err == nil {
		return T200Result{ID: bare.ID, T200Delivery: bare.T200Delivery}
	}
	return T200Result{}
}

// ===== T3.201 PATCH inbox для отложенной задачи (#282) =====
// Package wake adapts Beacon's wake pointer to the host harness. It uses the
// OpenCode HTTP T201API through `opencode api`, the same path the CLI and TUI use,
// so no plugin and no separate HTTP client are needed (ADR 0075, T3.203).
//
// This file covers the deferred-task case (ADR 0076, T3.201): a task already
// parked in the session inbox with delivery queue (or admitted with resume
// false) is turned into a wake by switching its delivery to steer. The task
// text is never sent again, so switching back to queue cannot lose it.

const (
	T201deliverySteer = "steer"
	T201deliveryQueue = "queue"
)

// T201API runs `opencode api` commands. Runner is replaceable in tests; nil means
// exec.CommandContext. Keeping one seam lets the same code drive GET and PATCH.
type T201API struct {
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

func (a *T201API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a.Runner != nil {
		return a.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// T201InboxItem is one undelivered message from GET /api/session/{sessionID}/inbox.
// Text is kept so callers can confirm the original task is still present after
// a delivery switch, without Beacon re-sending it.
type T201InboxItem struct {
	ID       string `json:"id"`
	Delivery string `json:"delivery"`
	Text     string `json:"text"`
}

// Inbox reads GET /api/session/{sessionID}/inbox. The v2 route answers with a
// {"data":[...]} envelope; a bare array is accepted as well.
func (a *T201API) Inbox(ctx context.Context, sessionID string) ([]T201InboxItem, error) {
	if err := T201validateSessionID(sessionID); err != nil {
		return nil, err
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox"
	out, err := a.run(ctx, "api", "GET", path)
	if err != nil {
		return nil, fmt.Errorf("wake: opencode api GET inbox: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "[") {
		var items []T201InboxItem
		if err := json.Unmarshal(out, &items); err != nil {
			return nil, fmt.Errorf("wake: decode inbox: %w", err)
		}
		return items, nil
	}
	var envelope struct {
		Data []T201InboxItem `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("wake: decode inbox: %w", err)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("wake: decode inbox: no data field")
	}
	return envelope.Data, nil
}

// T201FindInboxItem returns a pointer to the item with the given id, or nil when
// the id is not in the inbox. inboxID is the data.id returned by prompt.
func T201FindInboxItem(items []T201InboxItem, inboxID string) *T201InboxItem {
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
func (a *T201API) SetDelivery(ctx context.Context, sessionID, inboxID, delivery string) error {
	if err := T201validateSessionID(sessionID); err != nil {
		return err
	}
	if inboxID == "" {
		return fmt.Errorf("wake: empty inbox id")
	}
	if delivery != T201deliverySteer && delivery != T201deliveryQueue {
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
func (a *T201API) SteerPending(ctx context.Context, sessionID, inboxID string) error {
	items, err := a.Inbox(ctx, sessionID)
	if err != nil {
		return err
	}
	if T201FindInboxItem(items, inboxID) == nil {
		return fmt.Errorf("wake: inbox item %q not found in session %q", inboxID, sessionID)
	}
	return a.SetDelivery(ctx, sessionID, inboxID, T201deliverySteer)
}

// QueuePending returns a steered task to the queue without touching its text,
// so the same call in reverse simply parks it again.
func (a *T201API) QueuePending(ctx context.Context, sessionID, inboxID string) error {
	return a.SetDelivery(ctx, sessionID, inboxID, T201deliveryQueue)
}

func T201validateSessionID(sessionID string) error {
	if !strings.HasPrefix(sessionID, "ses_") || len(sessionID) == len("ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	return nil
}

// ===== T3.202 steer/queue и interrupt (#287) =====
// Package wake encodes the delivery rules for waking a parked OpenCode
// session. It chooses steer or queue at admission, plans how an input is
// consumed at the active or idle boundary, and issues the three calls the host
// T202API exposes: prompt, interrupt and inbox patch. It never invents a verdict
// and never merges a PR (ADR 0074, 0075, 0076, 0077, 0078, 0079).

// T202DeliveryMode is the ADR 0077 choice made once at prompt admission.
type T202DeliveryMode string

const (
	T202DeliverySteer T202DeliveryMode = "steer"
	T202DeliveryQueue T202DeliveryMode = "queue"
)

// T202ExecutionState says whether the target session is running a step.
type T202ExecutionState string

const (
	T202ExecIdle   T202ExecutionState = "idle"
	T202ExecActive T202ExecutionState = "active"
)

// T202InputKind separates inputs a prompt hook may rewrite from inputs it never
// sees. ADR 0077: no hook for synthetic, shell, compaction or move.
type T202InputKind string

const (
	T202InputPrompt    T202InputKind = "prompt"
	T202InputSynthetic T202InputKind = "synthetic"
	T202InputShell     T202InputKind = "shell"
	T202InputCompact   T202InputKind = "compact"
	T202InputMove      T202InputKind = "move"
)

// T202HooksApply reports whether a prompt hook may change the delivery mode. A
// hook fires once at admission and never for a synthetic or system input.
func T202HooksApply(k T202InputKind) bool { return k == T202InputPrompt }

// T202DefaultDelivery is the admission default. Prompt, synthetic and compact
// start as steer; a hook may demote an eligible input to queue once.
func T202DefaultDelivery(k T202InputKind) T202DeliveryMode {
	if k == T202InputPrompt || k == T202InputSynthetic || k == T202InputCompact {
		return T202DeliverySteer
	}
	return T202DeliveryQueue
}

// T202Admission is where an admitted input is consumed.
type T202Admission struct {
	Mode           T202DeliveryMode
	SchedulesLoop  bool
	AtStepBoundary bool
	IdlePriority   bool
	Parked         bool
}

// T202PlanAdmission applies ADR 0077 to a delivery mode and an execution state.
// steer schedules the loop, is consumed at the next safe step boundary while
// the loop is active, and wins at idle. queue never schedules: it stays parked
// in enqueue order.
func T202PlanAdmission(mode T202DeliveryMode, state T202ExecutionState) T202Admission {
	switch mode {
	case T202DeliverySteer:
		a := T202Admission{Mode: T202DeliverySteer, SchedulesLoop: true, IdlePriority: true}
		if state == T202ExecActive {
			a.AtStepBoundary = true
		}
		return a
	case T202DeliveryQueue:
		return T202Admission{Mode: T202DeliveryQueue, Parked: true}
	default:
		return T202Admission{Mode: mode}
	}
}

// T202AfterInterrupt returns an input's placement after POST interrupt. The resume
// flag governs the running loop, never the inbox: a queued input stays parked
// for both resume=true and resume=false (ADR 0077, 0079).
func T202AfterInterrupt(mode T202DeliveryMode, resume bool) T202Admission {
	return T202PlanAdmission(mode, T202ExecActive)
}

// T202PendingInput is one admitted input waiting in the inbox.
type T202PendingInput struct {
	ID   string
	Mode T202DeliveryMode
}

// T202NextInput picks the input consumed when the session reaches idle. Steers win
// over queued items regardless of enqueue order (ADR 0077).
func T202NextInput(pending []T202PendingInput) (T202PendingInput, bool) {
	for _, p := range pending {
		if p.Mode == T202DeliverySteer {
			return p, true
		}
	}
	for _, p := range pending {
		if p.Mode == T202DeliveryQueue {
			return p, true
		}
	}
	return T202PendingInput{}, false
}

// T202AdmissionStatus is the classified outcome of a prompt response.
type T202AdmissionStatus string

const (
	T202StatusAdmitted    T202AdmissionStatus = "admitted"
	T202StatusDuplicateID T202AdmissionStatus = "duplicate-id"
	T202StatusError       T202AdmissionStatus = "error"
)

// T202ClassifyHTTP maps a response code to an admission outcome. ADR 0077: 409 is
// a duplicate message id, not a busy session. There is no busy-session status,
// because an active loop consumes steer at its next step boundary.
func T202ClassifyHTTP(code int) T202AdmissionStatus {
	switch code {
	case 200, 201, 202, 204:
		return T202StatusAdmitted
	case 409:
		return T202StatusDuplicateID
	default:
		return T202StatusError
	}
}

// T202BusySession reports whether a status means the session refused work as
// busy. It never does; the function exists so callers stop reading 409 that
// way (ADR 0077).
func T202BusySession(s T202AdmissionStatus) bool { return false }

// T202Pointer is the identifier-only payload a verdict source hands to a wake
// adapter. It never carries check bodies or PR text (ADR 0074).
type T202Pointer struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
	Verdict      string `json:"verdict"`
}

// T202Envelope renders a pointer as the untrusted wake text admitted to a session.
func T202Envelope(p T202Pointer) string {
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

// T202Runner executes an `opencode api` invocation. Tests replace it.
type T202Runner func(ctx context.Context, args ...string) ([]byte, error)

// T202API is a thin client over the opencode api CLI (ADR 0078).
type T202API struct {
	T202Runner T202Runner
}

func (a *T202API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a.T202Runner != nil {
		return a.T202Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// Prompt admits one session input and returns its message id. mode is the
// explicit delivery choice: steer schedules execution, queue only parks.
func (a *T202API) Prompt(ctx context.Context, sessionID, text string, mode T202DeliveryMode) (string, error) {
	if !strings.HasPrefix(sessionID, "ses_") {
		return "", fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	body := map[string]any{"text": text, "delivery": string(mode)}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	args := []string{"api", "POST", "/api/session/" + sessionID + "/prompt", "--data", string(raw)}
	out, err := a.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("wake: opencode api prompt: %w", err)
	}
	return T202messageID(out)
}

// Park admits a queued input that does not schedule execution (ADR 0076).
func (a *T202API) Park(ctx context.Context, sessionID, text string) (string, error) {
	return a.Prompt(ctx, sessionID, text, T202DeliveryQueue)
}

// Interrupt posts POST /api/session/{id}/interrupt?resume=<bool>. resume=true
// stops the running loop but leaves queued inputs parked; resume=false is the
// admit-only stop (ADR 0079).
func (a *T202API) Interrupt(ctx context.Context, sessionID string, resume bool) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	path := "/api/session/" + sessionID + "/interrupt?resume=" + strconv.FormatBool(resume)
	if _, err := a.run(ctx, "api", "POST", path); err != nil {
		return fmt.Errorf("wake: opencode api interrupt: %w", err)
	}
	return nil
}

// Promote patches a parked inbox item to steer so the session wakes (ADR 0076).
func (a *T202API) Promote(ctx context.Context, sessionID, inboxID string) error {
	if !strings.HasPrefix(sessionID, "ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	path := "/api/session/" + sessionID + "/inbox/" + inboxID
	raw := []byte(`{"delivery":"steer"}`)
	if _, err := a.run(ctx, "api", "PATCH", path, "--data", string(raw)); err != nil {
		return fmt.Errorf("wake: opencode api promote: %w", err)
	}
	return nil
}

// T202messageID reads data.id from a prompt response. An empty id is a silent
// drop and is never reported as admitted.
func T202messageID(out []byte) (string, error) {
	if len(out) == 0 {
		return "", fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return "", fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return "", fmt.Errorf("wake: api response has no admitted message id")
	}
	return ack.Data.ID, nil
}

// ===== T3.203 CLI opencode api (#285) =====
// Package wake delivers Beacon wake pointers to an OpenCode session through
// the `opencode api` T203CLI. The T203CLI reaches the same server and uses the same
// credentials as the TUI, so no plugin and no separate HTTP client are needed.
// The package never invents a verdict and never merges a PR.

const (
	// T203connectedEvent is the first frame a correct SSE stream sends. Anything
	// before it is a replay of past events and must not reach the subscriber.
	T203connectedEvent = "server.connected"

	// T203defaultEventTypes are the facts a wake subscriber cares about.
	T203defaultEventTypes = "session.inbox.delivered,session.execution.started"
)

// T203PromptSyntax selects how session.prompt is addressed on the T203CLI. Both forms
// are accepted by `opencode api` and must produce the same admitted message.
type T203PromptSyntax int

const (
	// T203PromptRPC uses the operation name and a --param for the session id.
	T203PromptRPC T203PromptSyntax = iota
	// T203PromptPath uses the POST /api/session/{id}/prompt path form.
	T203PromptPath
)

// T203CLI runs `opencode api` commands. Server is an optional --server URL; when
// set, every command carries `--server <Server>` right after `api`. Runner is
// replaceable in tests; nil means exec.CommandContext with the real binary.
type T203CLI struct {
	Server string
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

// T203Envelope renders a pointer as untrusted wake text. Only identifiers cross
// the boundary: no check body, PR title, or verdict detail is interpolated.
func T203Envelope(p domain.Pointer) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"watchID":      p.WatchID,
		"obligationID": p.ObligationID,
		"revision":     p.Revision,
	})
	return "<beacon-event>" + strings.TrimSpace(buf.String()) + "</beacon-event>"
}

func T203promptData(text string) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"text": text, "delivery": "steer"})
	return strings.TrimSpace(buf.String())
}

func (c *T203CLI) baseArgs() []string {
	args := []string{"api"}
	if c.Server != "" {
		args = append(args, "--server", c.Server)
	}
	return args
}

// OpenAPIArgs builds the discovery command `opencode api GET /openapi.json`.
func (c *T203CLI) OpenAPIArgs() []string {
	return append(c.baseArgs(), "GET", "/openapi.json")
}

// PromptArgs builds the prompt command in either syntax. The --data body is
// identical for both, so both forms admit the same steer message.
func (c *T203CLI) PromptArgs(syntax T203PromptSyntax, sessionID, text string) []string {
	args := c.baseArgs()
	data := T203promptData(text)
	switch syntax {
	case T203PromptRPC:
		return append(args, "session.prompt", "--param", "sessionID="+sessionID, "--data", data)
	default:
		return append(args, "POST", "/api/session/"+sessionID+"/prompt", "--data", data)
	}
}

// EventArgs builds `opencode api GET /api/event` restricted to the given
// types. With no types it subscribes to the default wake facts.
func (c *T203CLI) EventArgs(types ...string) []string {
	if len(types) == 0 {
		types = strings.Split(T203defaultEventTypes, ",")
	}
	path := "/api/event?types=" + strings.Join(types, ",")
	return append(c.baseArgs(), "GET", path)
}

func (c *T203CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Runner != nil {
		return c.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// OpenAPI fetches the live OpenAPI document. It is the discovery step that
// proves the T203CLI and its authorization are usable before any wake is sent.
func (c *T203CLI) OpenAPI(ctx context.Context) ([]byte, error) {
	return c.run(ctx, c.OpenAPIArgs()...)
}

func T203admittedID(out []byte) (string, error) {
	if len(out) == 0 {
		return "", fmt.Errorf("wake: empty api response")
	}
	var ack struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ack); err != nil {
		return "", fmt.Errorf("wake: decode api response: %w", err)
	}
	if ack.Data.ID == "" {
		return "", fmt.Errorf("wake: api response has no admitted message id")
	}
	return ack.Data.ID, nil
}

// Prompt wakes a session with the chosen syntax and returns the admitted
// message id from data.id. Both syntaxes are accepted so the caller can verify
// they produce the same result.
func (c *T203CLI) Prompt(ctx context.Context, syntax T203PromptSyntax, sessionID, text string) (string, error) {
	if !strings.HasPrefix(sessionID, "ses_") {
		return "", fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	out, err := c.run(ctx, c.PromptArgs(syntax, sessionID, text)...)
	if err != nil {
		return "", fmt.Errorf("wake: opencode api: %w", err)
	}
	return T203admittedID(out)
}

// Deliver is the path-form convenience that wakes a session with a pointer.
func (c *T203CLI) Deliver(ctx context.Context, sessionID string, p domain.Pointer) error {
	_, err := c.Prompt(ctx, T203PromptPath, sessionID, T203Envelope(p))
	return err
}

// T203SSEFrame is one parsed server-sent event.
type T203SSEFrame struct {
	Event string
	Data  string
}

// T203Frames parses raw SSE bytes into frames. Comment and heartbeat lines that
// start with ':' are ignored, and unterminated final frames are still kept.
func T203Frames(out []byte) []T203SSEFrame {
	var frames []T203SSEFrame
	var cur T203SSEFrame
	have := false
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			if have {
				frames = append(frames, cur)
				cur, have = T203SSEFrame{}, false
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field, value = line, ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			cur.Event = value
			have = true
		case "data":
			cur.Data = value
			have = true
		}
	}
	if have {
		frames = append(frames, cur)
	}
	return frames
}

// T203Events drops frames that precede server.connected. A subscription must not
// surface past events that the server may replay before the connection frame.
func T203Events(frames []T203SSEFrame) []T203SSEFrame {
	for i, f := range frames {
		if f.Event == T203connectedEvent {
			return frames[i:]
		}
	}
	return nil
}

// Subscribe opens the event stream for the given types and returns frames
// from the connection point onward. The first returned frame is always
// server.connected; an absent connection frame is an error, not a silent
// empty stream.
func (c *T203CLI) Subscribe(ctx context.Context, types ...string) ([]T203SSEFrame, error) {
	out, err := c.run(ctx, c.EventArgs(types...)...)
	if err != nil {
		return nil, fmt.Errorf("wake: opencode api event: %w", err)
	}
	frames := T203Events(T203Frames(out))
	if len(frames) == 0 || frames[0].Event != T203connectedEvent {
		return nil, fmt.Errorf("wake: event stream must start with %s", T203connectedEvent)
	}
	return frames, nil
}

// ===== T3.204 негативный контроль через steer PATCH (#290) =====
// Package wake adapts Beacon's wake pointer to the host harness through the
// OpenCode HTTP T204API. The same `opencode api` path the CLI and TUI use drives
// both parking a task in the inbox and turning it into a steer wake.
//
// T3.204 adds the negative control: a parked task with delivery queue must not
// start execution. Only the PATCH that switches delivery to steer may produce
// session.execution.started, followed by session.idle.

const (
	T204deliverySteer = "steer"
	T204deliveryQueue = "queue"
)

// Host event types observed on the session event bus.
const (
	T204EventInboxEnqueued    = "session.inbox.enqueued"
	T204EventDeliveryChanged  = "session.inbox.delivery.changed"
	T204EventExecutionStarted = "session.execution.started"
	T204EventIdle             = "session.idle"
)

// T204Pointer carries only identifiers. No check body, PR title or other
// untrusted text is ever put on the wire (T3.200).
type T204Pointer struct {
	WatchID      string
	ObligationID string
	Revision     int
	Verdict      string
}

// T204API runs `opencode api` commands. Runner is replaceable in tests; nil means
// exec.CommandContext. One seam covers prompt, inbox, interrupt and active.
type T204API struct {
	Runner func(ctx context.Context, args ...string) ([]byte, error)
}

func (a *T204API) run(ctx context.Context, args ...string) ([]byte, error) {
	if a != nil && a.Runner != nil {
		return a.Runner(ctx, args...)
	}
	return exec.CommandContext(ctx, "opencode", args...).Output()
}

// T204Envelope renders the pointer as the host's wake text. Every field is
// whitelisted, so the envelope cannot carry an instruction or a leaked fact.
func T204Envelope(p T204Pointer) string {
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
func (a *T204API) Prompt(ctx context.Context, sessionID string, p T204Pointer, delivery string) (string, error) {
	if err := T204validateSessionID(sessionID); err != nil {
		return "", err
	}
	if delivery != T204deliverySteer && delivery != T204deliveryQueue {
		return "", fmt.Errorf("wake: invalid delivery %q", delivery)
	}
	body := map[string]any{
		"text":     T204Envelope(p),
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
func (a *T204API) Deliver(ctx context.Context, sessionID string, p T204Pointer) error {
	_, err := a.Prompt(ctx, sessionID, p, T204deliverySteer)
	return err
}

// Park posts a queue message and returns its inbox id. A parked task stays
// idle until a delivery switch wakes it (T3.201).
func (a *T204API) Park(ctx context.Context, sessionID string, p T204Pointer) (string, error) {
	return a.Prompt(ctx, sessionID, p, T204deliveryQueue)
}

// T204InboxItem is one undelivered message from GET /api/session/{id}/inbox.
// Text is kept so a delivery switch can be checked against the original task.
type T204InboxItem struct {
	ID       string `json:"id"`
	Delivery string `json:"delivery"`
	Text     string `json:"text"`
}

// Inbox reads GET /api/session/{id}/inbox. Both {"data":[...]} and a bare
// array are accepted.
func (a *T204API) Inbox(ctx context.Context, sessionID string) ([]T204InboxItem, error) {
	if err := T204validateSessionID(sessionID); err != nil {
		return nil, err
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox"
	out, err := a.run(ctx, "api", "GET", path)
	if err != nil {
		return nil, fmt.Errorf("wake: opencode api GET inbox: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "[") {
		var items []T204InboxItem
		if err := json.Unmarshal(out, &items); err != nil {
			return nil, fmt.Errorf("wake: decode inbox: %w", err)
		}
		return items, nil
	}
	var envelope struct {
		Data []T204InboxItem `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("wake: decode inbox: %w", err)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("wake: decode inbox: no data field")
	}
	return envelope.Data, nil
}

// T204FindInboxItem returns the item with the given id, or nil when it is absent.
func T204FindInboxItem(items []T204InboxItem, inboxID string) *T204InboxItem {
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
func (a *T204API) SetDelivery(ctx context.Context, sessionID, inboxID, delivery string) error {
	if err := T204validateSessionID(sessionID); err != nil {
		return err
	}
	if inboxID == "" {
		return fmt.Errorf("wake: empty inbox id")
	}
	if delivery != T204deliverySteer && delivery != T204deliveryQueue {
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
func (a *T204API) SteerPending(ctx context.Context, sessionID, inboxID string) error {
	items, err := a.Inbox(ctx, sessionID)
	if err != nil {
		return err
	}
	if T204FindInboxItem(items, inboxID) == nil {
		return fmt.Errorf("wake: inbox item %q not found in session %q", inboxID, sessionID)
	}
	return a.SetDelivery(ctx, sessionID, inboxID, T204deliverySteer)
}

// QueuePending switches a steered item back to queue without losing its text.
func (a *T204API) QueuePending(ctx context.Context, sessionID, inboxID string) error {
	return a.SetDelivery(ctx, sessionID, inboxID, T204deliveryQueue)
}

// Interrupt POSTs to the session interrupt route and reports the host's
// interrupted flag. An idle session answers interrupted=false.
func (a *T204API) Interrupt(ctx context.Context, sessionID string) (bool, error) {
	if err := T204validateSessionID(sessionID); err != nil {
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
func (a *T204API) Active(ctx context.Context) ([]string, error) {
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

// T204SessionEvent is one event observed on the host event bus.
type T204SessionEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
}

// T204VerifySteerCausality proves that execution for sessionID was caused by the
// steer switch and not by unrelated activity. It requires a delivery.changed
// event, no execution.started before it, and an execution.started followed by
// session.idle afterwards. A queue-only session fails every requirement.
func T204VerifySteerCausality(events []T204SessionEvent, sessionID string) error {
	steer := -1
	for i, event := range events {
		if event.SessionID == sessionID && event.Type == T204EventDeliveryChanged {
			steer = i
			break
		}
	}
	if steer < 0 {
		return fmt.Errorf("wake: no %s for %s", T204EventDeliveryChanged, sessionID)
	}
	for _, event := range events[:steer] {
		if event.SessionID == sessionID && event.Type == T204EventExecutionStarted {
			return fmt.Errorf("wake: %s before steer for %s", T204EventExecutionStarted, sessionID)
		}
	}
	started := -1
	for i, event := range events[steer+1:] {
		if event.SessionID == sessionID && event.Type == T204EventExecutionStarted {
			started = steer + 1 + i
			break
		}
	}
	if started < 0 {
		return fmt.Errorf("wake: no %s after steer for %s", T204EventExecutionStarted, sessionID)
	}
	for _, event := range events[started+1:] {
		if event.SessionID == sessionID && event.Type == T204EventIdle {
			return nil
		}
	}
	return fmt.Errorf("wake: no %s after %s for %s", T204EventIdle, T204EventExecutionStarted, sessionID)
}

func T204validateSessionID(sessionID string) error {
	if !strings.HasPrefix(sessionID, "ses_") || len(sessionID) == len("ses_") {
		return fmt.Errorf("wake: invalid session id %q", sessionID)
	}
	return nil
}

// ===== T3.205 сравнение wake-механизмов (#291) =====
// Package wake records the comparison of the Pi, Kilo Code and Cursor wake
// mechanisms and the four common primitives Beacon adopts from them
// (ADR 0077, T3.205).
//
// The comparison is data, not prose. T205Matrix holds one grounded fact per
// mechanism and axis, T205Primitives holds the four recurring primitives, and
// T205SourceAnchors holds the four verifiable facts the card's check requires.
// T205Overview renders the matrix as a table. Tests check that the table is
// complete and that no mechanism outside the three source reports was added.
//
// Every fact names the report it came from: wake-pi.md, wake-kilo-code.md or
// wake-cursor.md, all dated 20 November 2026. Beacon design ideas are kept out.

// T205Mechanism is one wake model described in the source reports.
type T205Mechanism string

const T205MechanismPi T205Mechanism = "pi"
const T205MechanismKilo T205Mechanism = "kilo"
const T205MechanismCursor T205Mechanism = "cursor"

// T205Axis is one comparison dimension required by T3.205.
type T205Axis string

const T205AxisTruth T205Axis = "source of truth"
const T205AxisDedup T205Axis = "deduplication"
const T205AxisBusy T205Axis = "busy behavior"
const T205AxisRestart T205Axis = "restart recovery"
const T205AxisDelivery T205Axis = "result delivery"

// T205Row is one cell of the comparison: what a mechanism does on an axis and
// which report states it. Source is a report filename, never a Beacon-only
// idea presented as a mechanism.
type T205Row struct {
	T205Mechanism T205Mechanism
	T205Axis      T205Axis
	Fact      string
	Source    string
}

func T205row(m T205Mechanism, a T205Axis, fact, source string) T205Row {
	return T205Row{T205Mechanism: m, T205Axis: a, Fact: fact, Source: source}
}

// T205Matrix is the comparison table. Fifteen rows cover three mechanisms on five
// axes. Each T205row is taken from a source report; nothing is added from
// Beacon's own design.
var T205Matrix = []T205Row{
	T205row(T205MechanismPi, T205AxisTruth, "State lives in the pi.inbox document and durable phase checkpoints; the durable since cursor survives a client restart, while Chord events are delivered once and are neither stored nor replayed to late subscribers.", "wake-pi.md"),
	T205row(T205MechanismPi, T205AxisDedup, "A repeated submit with the same requestId returns the same submission; a signal on an occupied key yields duplicate, and the idempotency key is fixed before the external call, as api.memo is fixed before park.", "wake-pi.md"),
	T205row(T205MechanismPi, T205AxisBusy, "Policy submit uses whenBusy steer with whenIdle reject, and busyDelivery is steer or human-first. A steer joins the current run after the tool round; a follow-up starts the next run.", "wake-pi.md"),
	T205row(T205MechanismPi, T205AxisRestart, "The coding-agent core has no sleep scheduler and no built-in background agents. Continuation needs a long-lived RPC or SDK listener, an extension with a watcher or timer, or durable resume(); the process never wakes itself.", "wake-pi.md"),
	T205row(T205MechanismPi, T205AxisDelivery, "Delivery results are delivered, duplicate, invalid, closed and not_found. A new submission continues the work, and agent_end is not final readiness.", "wake-pi.md"),
	T205row(T205MechanismKilo, T205AxisTruth, "Wake is advisory, not the source of truth. Truth lives in the durable record: the wakeup is written to Storage before its timer, and session events carry a monotonic SQLite seq; the signal only suggests re-reading state.", "wake-kilo-code.md"),
	T205row(T205MechanismKilo, T205AxisDedup, "A duplicate signal cannot start a second copy because of the write lock. Repeated wake calls collapse into one pendingWake, and fireNow deletes the durable record under the cross-process EffectFlock before resume.", "wake-kilo-code.md"),
	T205row(T205MechanismKilo, T205AxisBusy, "The coordinator's wake announces new durable work and collapses repeats into one pendingWake; resumeOrQueue places a signal in pending instead of starting a parallel run.", "wake-kilo-code.md"),
	T205row(T205MechanismKilo, T205AxisRestart, "adopt re-reads wakeup records from Storage at instance start and rearms timers, so an overdue record fires at once. A subscriber keeps the last seq and reopens the stream with after.", "wake-kilo-code.md"),
	T205row(T205MechanismKilo, T205AxisDelivery, "A background task injects its result into the parent session as a synthetic text part tagged background:true with state completed or error; drain publishes session.drained, and FINISHED is not final readiness.", "wake-kilo-code.md"),
	T205row(T205MechanismCursor, T205AxisTruth, "The pending-request list is the source of truth and delivery is best-effort. list-then-watch reads the full list first and stores a local streamCursor; a lost event is recovered by the next list, not redelivered.", "wake-cursor.md"),
	T205row(T205MechanismCursor, T205AxisDedup, "X-Webhook-ID is the unique delivery id and Idempotency-Key returns 422 for the same key with a different body and the first run for the same body. A steer retried after a network error can deliver twice, so deliveryId is required.", "wake-cursor.md"),
	T205row(T205MechanismCursor, T205AxisBusy, "delivery steer reports steered or queued, 409 is agent_busy, admission coalesce applies a Slack policy, and preempt interrupts the active turn only with an explicit flag.", "wake-cursor.md"),
	T205row(T205MechanismCursor, T205AxisRestart, "list-then-watch stores the cursor only in the controller. The cursor lives five minutes from the list, heartbeat and reconnect do not extend it, and 410 Gone with cursor_expired or a periodic jittered timer triggers a re-list.", "wake-cursor.md"),
	T205row(T205MechanismCursor, T205AxisDelivery, "A wake call returns 202 and the result arrives through events, callback or tool, with SSE streams scoped to one run or to an agent across runs. FINISHED and IDLE do not mean final readiness.", "wake-cursor.md"),
}

// T205Primitive is one recurring T205primitive chosen for Beacon.
type T205Primitive string

const T205PrimitiveDeliveryID T205Primitive = "deliveryId"
const T205PrimitiveEventsHint T205Primitive = "events as hint"
const T205PrimitiveStateTruth T205Primitive = "state as truth"
const T205PrimitiveLeaseRelease T205Primitive = "lease/release"

// T205PrimitiveDoc explains why a T205primitive was chosen and which reports ground it.
type T205PrimitiveDoc struct {
	T205Primitive T205Primitive
	Summary   string
	Reports   []string
}

func T205primitive(p T205Primitive, summary string, reports ...string) T205PrimitiveDoc {
	return T205PrimitiveDoc{T205Primitive: p, Summary: summary, Reports: reports}
}

// T205Primitives are the four repeating primitives from the reports. No T205primitive
// is invented here: each names at least one source report.
var T205Primitives = []T205PrimitiveDoc{
	T205primitive(T205PrimitiveDeliveryID, "A single delivery id runs from wake to ack. Cursor confirms a follow-up with deliveryId, X-Webhook-ID or a fresh dlv_, and rejects a reused Idempotency-Key with a different body. Pi returns the same submission for a repeated requestId and reports duplicate on an occupied key; Kilo collapses repeated signals into pendingWake. The key is fixed before the external call, as api.memo is fixed before park.", "wake-cursor.md", "wake-pi.md", "wake-kilo-code.md"),
	T205primitive(T205PrimitiveEventsHint, "An event is advice, never the obligation. Cursor says delivery is best-effort, the list is the truth, lost events are not redelivered and the next list fixes divergence. Kilo's wake is advisory with a dirty signal of capacity one over a durable record. Pi Chord events are delivered once, are not folded, stored or sent to late subscribers.", "wake-cursor.md", "wake-kilo-code.md", "wake-pi.md"),
	T205primitive(T205PrimitiveStateTruth, "A full read of authoritative state always wins. Cursor re-lists on 410 and cursor_expired and on a jittered timer; Kilo keeps the wakeup in Storage and re-reads it in adopt with a seq cursor; Pi recovers from the pi.inbox document and durable checkpoints with a durable since cursor.", "wake-cursor.md", "wake-kilo-code.md", "wake-pi.md"),
	T205primitive(T205PrimitiveLeaseRelease, "An obligation is leased with explicit release and fail paths. Cursor's claim reserves a request, rejects a second claim while the lease is live, returns 400 from release while the turn still holds the worker, and carries wakeTimeoutMs in claimed_offline; fail shows the text to the user. Kilo's fireNow removes the durable record under EffectFlock before resume.", "wake-cursor.md", "wake-kilo-code.md"),
}

// T205Anchor is a verifiable fact from a source report, used by the card's check.
type T205Anchor struct {
	ID    string
	Value string
}

func T205anchor(id, value string) T205Anchor {
	return T205Anchor{ID: id, Value: value}
}

// T205SourceAnchors are the four check facts: the dead original Pi URL and its
// live repository, the pinned Kilo commit, and the Cursor v0/v1 split.
var T205SourceAnchors = []T205Anchor{
	T205anchor("pi-original-url-404", "github.com/mariozechner/pi-coding-agent"),
	T205anchor("pi-repository", "https://github.com/earendil-works/pi"),
	T205anchor("kilo-commit", "bd8b98aeab348c08221a836ba091b855a62814e9"),
	T205anchor("cursor-api-split", "v0/v1"),
}

// T205NonMechanisms are behaviours the reports explicitly rule out. They are not
// wake mechanisms and must not be promoted into the matrix.
var T205NonMechanisms = []string{
	"a built-in sleep scheduler in the Pi coding-agent core",
	"automatic events.subscribe reconnect as a recovery path",
	"FINISHED, IDLE or agent_end as final readiness",
}

// T205Overview renders T205Matrix as a markdown table with one T205row per axis and one
// column per mechanism. It is the human-readable form of the comparison.
func T205Overview() string {
	var b strings.Builder
	b.WriteString("| T205Axis | Pi | Kilo Code | Cursor |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, axis := range []T205Axis{T205AxisTruth, T205AxisDedup, T205AxisBusy, T205AxisRestart, T205AxisDelivery} {
		b.WriteString("| " + string(axis) + " |")
		for _, m := range []T205Mechanism{T205MechanismPi, T205MechanismKilo, T205MechanismCursor} {
			b.WriteString(" ")
			b.WriteString(T205FactFor(m, axis))
			b.WriteString(" |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// T205FactFor returns the grounded fact for a mechanism and axis, or an empty
// string when the cell is missing.
func T205FactFor(m T205Mechanism, a T205Axis) string {
	for _, r := range T205Matrix {
		if r.T205Mechanism == m && r.T205Axis == a {
			return r.Fact
		}
	}
	return ""
}

// ===== T3.206 deliveryId и журнал (#288) =====
// Package wake adapts Beacon's wake pointer to the host harness and records
// every delivery under a stable deliveryId. ADR 0081 and card T3.206 require
// that a repeated webhook or a retried steer after a network error never
// starts a second job: the delivery is deduplicated by deliveryId before any
// transport call, and an Idempotency-Key reused with a different body is
// rejected the way Cursor answers 422.

// T206WakeKind is the shape of a wake, following the primitives from ADR 0085.
// Beacon never invents a verdict from it.
type T206WakeKind string

const (
	T206KindNewRun   T206WakeKind = "newRun"
	T206KindSteer    T206WakeKind = "steer"
	T206KindFollowUp T206WakeKind = "followUp"
	T206KindSignal   T206WakeKind = "signal"
	T206KindResume   T206WakeKind = "resume"
	T206KindNoop     T206WakeKind = "noop"
)

// T206DeliveryStatus is the honest outcome of a wake attempt. The vocabulary is
// shared with ADR 0085. duplicate is what Pi reports for a repeated requestId
// and what Kilo collapses into pendingWake.
type T206DeliveryStatus string

const (
	T206StatusStarted     T206DeliveryStatus = "started"
	T206StatusQueued      T206DeliveryStatus = "queued"
	T206StatusDelivered   T206DeliveryStatus = "delivered"
	T206StatusDuplicate   T206DeliveryStatus = "duplicate"
	T206StatusClosed      T206DeliveryStatus = "closed"
	T206StatusUnsupported T206DeliveryStatus = "unsupported"
)

// Errors returned before the transport runs, so a caller can tell a
// deduplicated delivery from a malformed or conflicting one.
var (
	T206ErrDeliveryIDRequired  = errors.New("wake: deliveryId is required")
	T206ErrDeliveryIDReuse     = errors.New("wake: deliveryId reused with a different body")
	T206ErrIdempotencyConflict = errors.New("wake: idempotency key reused with a different body")
)

// T206WakeRequest is the unit of delivery. DeliveryID is mandatory: for an
// incoming webhook it is the X-Webhook-ID header; for an outbound steer it is
// generated once and kept on retry. IdempotencyKey is optional and is checked
// against the body hash recorded for the first delivery.
type T206WakeRequest struct {
	DeliveryID     string    `json:"deliveryId"`
	IdempotencyKey string    `json:"idempotencyKey,omitempty"`
	Target         string    `json:"target"`
	Kind           T206WakeKind  `json:"kind"`
	Body           []byte    `json:"body,omitempty"`
	At             time.Time `json:"at,omitempty"`
}

// T206DeliveryResult reports what the deliverer did. Coalesced is true when the
// request matched a journal entry and no second job was started.
type T206DeliveryResult struct {
	DeliveryID string         `json:"deliveryId"`
	Status     T206DeliveryStatus `json:"status"`
	Coalesced  bool           `json:"coalesced"`
}

// T206Transport performs the external side effect. It is a function so the
// journal can be tested without a real webhook or steer channel.
type T206Transport func(ctx context.Context, req T206WakeRequest) (T206DeliveryStatus, error)

const (
	// T206HeaderWebhookID is Cursor's unique per-delivery identifier.
	T206HeaderWebhookID = "X-Webhook-ID"
	// T206HeaderIdempotencyKey is checked against the body of the first delivery.
	T206HeaderIdempotencyKey = "Idempotency-Key"
)

// T206WakeRequestFromWebhook lifts deliveryId and the idempotency key from the
// incoming headers. A webhook without X-Webhook-ID is rejected: accepting it
// would let a retry start a second job.
func T206WakeRequestFromWebhook(h http.Header, target string, kind T206WakeKind, body []byte) (T206WakeRequest, error) {
	id := strings.TrimSpace(h.Get(T206HeaderWebhookID))
	if id == "" {
		return T206WakeRequest{}, T206ErrDeliveryIDRequired
	}
	return T206WakeRequest{
		DeliveryID:     id,
		IdempotencyKey: strings.TrimSpace(h.Get(T206HeaderIdempotencyKey)),
		Target:         target,
		Kind:           kind,
		Body:           body,
		At:             time.Now().UTC(),
	}, nil
}

type T206deliveryRecord struct {
	deliveryID     string
	T206bodyHash       string
	idempotencyKey string
	status         T206DeliveryStatus
	seenAt         time.Time
}

// T206DeliveryJournal is the bounded ledger of delivered IDs. A real adapter can
// back it with durable storage; the contract is the same: one deliveryId maps
// to one recorded body hash and one outcome.
type T206DeliveryJournal struct {
	mu    sync.Mutex
	byID  map[string]T206deliveryRecord
	byKey map[string]string
}

func T206NewDeliveryJournal() *T206DeliveryJournal {
	return &T206DeliveryJournal{
		byID:  make(map[string]T206deliveryRecord),
		byKey: make(map[string]string),
	}
}

// T206Deliverer wraps a transport with the delivery journal. It never calls the
// transport twice for the same deliveryId, and it records the key before the
// external call so a concurrent retry sees the first attempt already staged.
type T206Deliverer struct {
	Journal   *T206DeliveryJournal
	T206Transport T206Transport
}

func (d *T206Deliverer) Deliver(ctx context.Context, req T206WakeRequest) (T206DeliveryResult, error) {
	if d == nil || d.Journal == nil {
		return T206DeliveryResult{}, errors.New("wake: deliverer has no journal")
	}
	if strings.TrimSpace(req.DeliveryID) == "" {
		return T206DeliveryResult{}, T206ErrDeliveryIDRequired
	}
	if d.T206Transport == nil {
		return T206DeliveryResult{}, errors.New("wake: deliverer has no transport")
	}

	hash := T206bodyHash(req.Body)
	j := d.Journal
	j.mu.Lock()

	if rec, ok := j.byID[req.DeliveryID]; ok {
		j.mu.Unlock()
		if rec.T206bodyHash != hash {
			return T206DeliveryResult{}, fmt.Errorf("%w: %s", T206ErrDeliveryIDReuse, req.DeliveryID)
		}
		return T206DeliveryResult{DeliveryID: rec.deliveryID, Status: T206StatusDuplicate, Coalesced: true}, nil
	}
	if req.IdempotencyKey != "" {
		if id, ok := j.byKey[req.IdempotencyKey]; ok {
			rec := j.byID[id]
			j.mu.Unlock()
			if rec.T206bodyHash != hash {
				return T206DeliveryResult{}, fmt.Errorf("%w: %s", T206ErrIdempotencyConflict, req.IdempotencyKey)
			}
			return T206DeliveryResult{DeliveryID: rec.deliveryID, Status: T206StatusDuplicate, Coalesced: true}, nil
		}
	}

	// Fix the key before the external call, as api.memo in Pi before park.
	now := time.Now().UTC()
	j.byID[req.DeliveryID] = T206deliveryRecord{
		deliveryID:     req.DeliveryID,
		T206bodyHash:       hash,
		idempotencyKey: req.IdempotencyKey,
		status:         T206StatusQueued,
		seenAt:         now,
	}
	if req.IdempotencyKey != "" {
		j.byKey[req.IdempotencyKey] = req.DeliveryID
	}
	j.mu.Unlock()

	status, err := d.T206Transport(ctx, req)
	if err != nil {
		// The staged entry stays in the journal: a retry after a network
		// error is a duplicate, never a second job.
		return T206DeliveryResult{DeliveryID: req.DeliveryID}, err
	}

	j.mu.Lock()
	rec := j.byID[req.DeliveryID]
	rec.status = status
	j.byID[req.DeliveryID] = rec
	j.mu.Unlock()

	return T206DeliveryResult{DeliveryID: req.DeliveryID, Status: status}, nil
}

// Get returns the journaled outcome for a deliveryId without attempting a
// delivery. It is the read side used by observability and by tests.
func (j *T206DeliveryJournal) Get(deliveryID string) (T206DeliveryResult, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.byID[deliveryID]
	if !ok {
		return T206DeliveryResult{}, false
	}
	return T206DeliveryResult{DeliveryID: rec.deliveryID, Status: rec.status}, true
}

// Len reports how many distinct deliveries the journal has recorded.
func (j *T206DeliveryJournal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.byID)
}

func T206bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ===== T3.207 приём 202 и вердикт (#283) =====
// Package wake implements the non-blocking wake acceptance of ADR 0082.
//
// A wake call is an admission, not an answer. Accept returns 202 with a
// deliveryId at once, starts the real work in the background and publishes
// events as hints. The final state comes only from a verdict on a separate
// channel: never from the 202 response and never from an agent status such as
// FINISHED, IDLE or agent_end. The adapter behaves the same in hosted and local
// mode (ADR 0082), and events are hints while state is the truth (ADR 0080).

// T207DeliveryId ties one admission, its events and its verdict together (ADR
// 0081). It is opaque to the core.
type T207DeliveryId string

// T207Mode tells hosted and local execution apart. It changes where the handler
// runs, never the acknowledgement or the finality rule (ADR 0082).
type T207Mode string

const (
	T207Hosted T207Mode = "hosted"
	T207Local  T207Mode = "local"
)

// T207AcceptedStatus is the only status a wake admission returns.
const T207AcceptedStatus = 202

// T207Accepted is the whole 202 response: an acknowledgement and a deliveryId.
// It has no result, no outcome and no verdict, so finality can never be read
// from it.
type T207Accepted struct {
	Status     int        `json:"status"`
	T207DeliveryId T207DeliveryId `json:"deliveryId"`
}

// T207EventType names a published hint. A hint tells a listener what to re-read;
// it does not say that work has ended.
type T207EventType string

const (
	T207AcceptedEvent  T207EventType = "wake.accepted"
	T207DeliveredEvent T207EventType = "nudge.delivered"
)

// T207Event is a hint published on the event channel, always carrying the
// deliveryId of the admission it belongs to.
type T207Event struct {
	Type       T207EventType  `json:"type"`
	T207DeliveryId T207DeliveryId `json:"deliveryId"`
}

// T207Verdict is the only fact that can finish a task. T207Pending keeps it open;
// T207Ready is final.
type T207Verdict string

const (
	T207Pending T207Verdict = "pending"
	T207Ready   T207Verdict = "ready"
)

// T207AgentSignal is a terminal-looking status from an agent. None of them finish
// a task: FINISHED and IDLE come from Cursor, agent_end from Pi, and ADR 0082
// forbids reading finality from any of them.
type T207AgentSignal string

const (
	T207Finished T207AgentSignal = "FINISHED"
	T207Idle     T207AgentSignal = "IDLE"
	T207AgentEnd T207AgentSignal = "agent_end"
)

// T207Request is one wake admission. An empty T207DeliveryId is generated. T207Mode is
// recorded for observability but never changes the acknowledgement.
type T207Request struct {
	Target     string
	Payload    string
	T207DeliveryId T207DeliveryId
	T207Mode       T207Mode
}

// T207Work is the background job started after an admission. It receives the same
// deliveryId as the 202 response.
type T207Work func(ctx context.Context, req T207Request, id T207DeliveryId)

// T207Deliverer admits wake calls without waiting for results. It owns the event
// and verdict channels and the per-delivery state, and is safe for concurrent
// callers.
type T207Deliverer struct {
	work     T207Work
	events   chan T207Event
	verdicts chan T207Verdict
	seen     map[T207DeliveryId]struct{}
	states   map[T207DeliveryId]T207Verdict
	nextID   atomic.Uint64
	mu       sync.Mutex
}

// T207New builds a T207Deliverer that runs work in the background after each
// admission. A nil work is allowed and leaves the delivery as admitted only.
func T207New(work T207Work) *T207Deliverer {
	return &T207Deliverer{
		work:     work,
		events:   make(chan T207Event, 64),
		verdicts: make(chan T207Verdict, 16),
		seen:     make(map[T207DeliveryId]struct{}),
		states:   make(map[T207DeliveryId]T207Verdict),
	}
}

// Accept admits one wake call. It returns 202 with a deliveryId immediately,
// before any background work runs. A repeated deliveryId is a duplicate: it
// returns the same acknowledgement and starts no second job (ADR 0081).
func (d *T207Deliverer) Accept(ctx context.Context, req T207Request) T207Accepted {
	id := req.T207DeliveryId
	if id == "" {
		id = T207DeliveryId(fmt.Sprintf("dlv_%d", d.nextID.Add(1)))
	}

	d.mu.Lock()
	if _, duplicate := d.seen[id]; duplicate {
		d.mu.Unlock()
		return T207Accepted{Status: T207AcceptedStatus, T207DeliveryId: id}
	}
	d.seen[id] = struct{}{}
	d.states[id] = T207Pending
	d.mu.Unlock()

	d.Publish(T207Event{Type: T207AcceptedEvent, T207DeliveryId: id})
	go func() {
		d.Publish(T207Event{Type: T207DeliveredEvent, T207DeliveryId: id})
		if d.work != nil {
			d.work(ctx, req, id)
		}
	}()

	return T207Accepted{Status: T207AcceptedStatus, T207DeliveryId: id}
}

// Publish emits a hint. It never blocks the caller: a full buffer drops the
// hint instead of delaying an admission or a decision.
func (d *T207Deliverer) Publish(e T207Event) {
	select {
	case d.events <- e:
	default:
	}
}

// Events is the hint channel. A reader must never treat an event as final.
func (d *T207Deliverer) Events() <-chan T207Event { return d.events }

// Verdicts is the decision channel. Only T207Ready on this channel finishes work.
func (d *T207Deliverer) Verdicts() <-chan T207Verdict { return d.verdicts }

// Decide records a decision from the verdict channel. Only T207Ready flips the
// state and publishes on the verdict channel; anything else leaves the task
// open. An unknown deliveryId is ignored.
func (d *T207Deliverer) Decide(id T207DeliveryId, v T207Verdict) {
	d.mu.Lock()
	if _, known := d.states[id]; !known {
		d.mu.Unlock()
		return
	}
	if v == T207Ready {
		d.states[id] = T207Ready
	}
	d.mu.Unlock()
	if v == T207Ready {
		select {
		case d.verdicts <- T207Ready:
		default:
		}
	}
}

// Observe records an agent status without treating it as final. FINISHED, IDLE
// and agent_end are hints at most; the state stays T207Pending until a verdict.
func (d *T207Deliverer) Observe(id T207DeliveryId, signal T207AgentSignal) T207Verdict {
	_ = signal // ADR 0082: an agent status is never a verdict.
	return d.State(id)
}

// State is the source of truth for one admission. It is T207Pending until an
// explicit T207Ready verdict arrives.
func (d *T207Deliverer) State(id T207DeliveryId) T207Verdict {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.states[id]
}

// ===== T3.208 события подсказка, состояние истина (#284) =====
// Package wake delivers obligations from authoritative state, using wake
// events only as hints (ADR 0080, T3.208).
//
// Events are hints, state is truth. An event carries only an id and a seq; it
// never carries an obligation. Obligations enter a T208Deliverer exclusively
// through a full List of authoritative state, so a missed event costs a
// wake-up, never an obligation: the next full read recovers it. Events are
// deduplicated by id and coalesced into one dirty signal of capacity one. A
// late subscriber gets no replay of past events (Pi Chord); it gets the
// current state from a full list instead.
//
// Cursors are process-local. They live only in T208Deliverer memory and are never
// persisted, so a restarted process always begins with a full list. A 410 Gone
// or cursor_expired signal from a watch discards the dead cursor and triggers
// an immediate re-list; the deliverer never depends on automatic reconnect.

// T208CursorExpired is the source code reported when a watch cursor is no longer
// valid. Together with HTTP 410 Gone it is a re-list signal.
const T208CursorExpired = "cursor_expired"

// T208httpStatusGone is the HTTP status that means a resource and its cursor are
// gone. It is kept local so this package does not depend on net/http.
const T208httpStatusGone = 410

// T208GoneError reports that a watch cursor is dead. It models both HTTP 410 Gone
// and a source that reports cursor_expired. The deliverer answers it with a
// full re-list instead of retrying the dead cursor.
type T208GoneError struct {
	Status int
	Code   string
}

func (e *T208GoneError) Error() string {
	return fmt.Sprintf("wake: cursor gone (status=%d code=%q)", e.Status, e.Code)
}

// T208IsGone reports whether err carries a 410 Gone or cursor_expired signal.
func T208IsGone(err error) bool {
	var g *T208GoneError
	if !errors.As(err, &g) {
		return false
	}
	return g.Status == T208httpStatusGone || g.Code == T208CursorExpired
}

// T208Event is a wake hint. It carries only an id, used for deduplication, and a
// seq, used for ordering. It never carries an obligation or state, and it is
// never replayed to a late subscriber. State comes from List, not from here.
type T208Event struct {
	ID  string
	Seq uint64
}

// T208Obligation is one durable unit of work discovered in authoritative state.
type T208Obligation struct {
	ID       string
	Revision uint64
}

// T208Snapshot is one full read of authoritative state. Revision orders snapshots
// so an older read can never overwrite a newer one. Cursor is a process-local
// watch position; it dies with the process and is never persisted.
type T208Snapshot struct {
	Revision    uint64
	Cursor      string
	Obligations []T208Obligation
}

// T208StateSource is the authoritative reader behind a T208Deliverer. List is a full
// read and the only way obligations enter the deliverer. Watch waits for the
// next hint from a process-local cursor and may fail with a T208GoneError.
type T208StateSource interface {
	List(ctx context.Context) (T208Snapshot, error)
	Watch(ctx context.Context, cursor string) (T208Event, error)
}

// T208Deliverer reconciles authoritative state into a local obligation set. It
// holds an event-id dedup index, a high-water seq, and a coalescing dirty bit
// of capacity one. Every field, including the cursor, is in memory: a restart
// starts with no state and no cursor, hence with a full list.
type T208Deliverer struct {
	source T208StateSource
	jitter func() time.Duration

	mu          sync.Mutex
	byID        map[string]T208Obligation
	seen        map[string]struct{}
	lastSeq     uint64
	hasSeq      bool
	revision    uint64
	hasRevision bool
	cursor      string
	dirty       bool
	relists     int
	goneRelists int
}

// T208NewDeliverer builds a T208Deliverer over source. jitter supplies the delay added
// before each periodic full read; a nil jitter means no delay. The deliverer
// starts with no state and no cursor, so its first Reconcile is always a full
// list.
func T208NewDeliverer(source T208StateSource, jitter func() time.Duration) *T208Deliverer {
	return &T208Deliverer{
		source: source,
		jitter: jitter,
		byID:   make(map[string]T208Obligation),
		seen:   make(map[string]struct{}),
	}
}

// Apply records a wake hint. It is idempotent by event id: a repeated id is a
// no-op and never changes state. An event whose seq is not newer than the
// high-water mark is discarded. A fresh event only sets the coalescing dirty
// bit; it never changes obligations, because obligations come from List. It
// reports whether the event was accepted.
func (d *T208Deliverer) Apply(ev T208Event) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ev.ID == "" {
		return false
	}
	if _, ok := d.seen[ev.ID]; ok {
		return false
	}
	d.seen[ev.ID] = struct{}{}
	if d.hasSeq && ev.Seq <= d.lastSeq {
		return false
	}
	if !d.hasSeq || ev.Seq > d.lastSeq {
		d.lastSeq = ev.Seq
		d.hasSeq = true
	}
	d.dirty = true
	return true
}

// Reconcile performs one full List and makes its obligations the current
// state. It is unconditional: even with no events, a periodic reconcile
// recovers an obligation whose event was lost. A snapshot whose revision is
// not newer than the current one is discarded, so a late or duplicated read
// cannot roll state back. mu is held across the read to keep one writer at a
// time. It returns the number of obligations now held and whether the snapshot
// was applied.
func (d *T208Deliverer) Reconcile(ctx context.Context) (int, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.source == nil {
		return 0, false, errors.New("wake: no state source")
	}
	snap, err := d.source.List(ctx)
	if err != nil {
		return len(d.byID), false, err
	}
	if d.hasRevision && snap.Revision <= d.revision {
		return len(d.byID), false, nil
	}
	d.hasRevision = true
	d.revision = snap.Revision
	d.cursor = snap.Cursor
	d.byID = make(map[string]T208Obligation, len(snap.Obligations))
	for _, o := range snap.Obligations {
		d.byID[o.ID] = o
	}
	d.dirty = false
	d.relists++
	return len(d.byID), true, nil
}

// WatchOnce waits for one hint from the current process-local cursor and
// applies it. If the source reports 410 Gone or cursor_expired, the dead
// cursor is discarded and an immediate full re-list runs; no automatic
// reconnect is assumed. It reports the event, whether a re-list happened, and
// any error.
func (d *T208Deliverer) WatchOnce(ctx context.Context) (T208Event, bool, error) {
	if d.source == nil {
		return T208Event{}, false, errors.New("wake: no state source")
	}
	d.mu.Lock()
	cursor := d.cursor
	d.mu.Unlock()

	ev, err := d.source.Watch(ctx, cursor)
	if err != nil {
		if T208IsGone(err) {
			_, _, relistErr := d.Reconcile(ctx)
			if relistErr == nil {
				d.mu.Lock()
				d.goneRelists++
				d.mu.Unlock()
			}
			return T208Event{}, true, relistErr
		}
		return T208Event{}, false, err
	}
	d.Apply(ev)
	return ev, false, nil
}

// Run drives periodic full reads until ctx is cancelled. It lists once at
// startup, then on every tick waits the configured jitter and lists again.
// This periodic read is the safety net that makes the deliverer independent
// of any single lost event or missing reconnect.
func (d *T208Deliverer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	_, _, _ = d.Reconcile(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.jitter != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(d.jitter()):
				}
			}
			_, _, _ = d.Reconcile(ctx)
		}
	}
}

// Obligations returns a copy of the current authoritative obligation set.
func (d *T208Deliverer) Obligations() []T208Obligation {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]T208Obligation, 0, len(d.byID))
	for _, o := range d.byID {
		out = append(out, o)
	}
	return out
}

// Has reports whether an obligation id is currently held.
func (d *T208Deliverer) Has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.byID[id]
	return ok
}

// State reports the last applied revision, the current process-local cursor,
// and whether an unprocessed hint is pending.
func (d *T208Deliverer) State() (revision uint64, cursor string, dirty bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.revision, d.cursor, d.dirty
}

// Relists returns how many full reads have been applied.
func (d *T208Deliverer) Relists() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.relists
}

// GoneRelists returns how many re-lists were triggered by 410/cursor_expired.
func (d *T208Deliverer) GoneRelists() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.goneRelists
}

// ===== T3.209 lease obligation (#289) =====
// Package wake manages the lease on a Beacon obligation. A claim reserves an
// obligation for one worker for wakeTimeoutMs. A second claim at a live lease
// is rejected, an expired lease returns the obligation to created, release
// frees a lease after a lost response, and fail publishes a reason the user
// can see. No outcome is left silent.

// T209State is the lifecycle of one obligation in the lease manager.
type T209State string

const (
	// T209StateCreated means the obligation is available for claim.
	T209StateCreated T209State = "created"
	// T209StateClaimed means a worker holds a live lease.
	T209StateClaimed T209State = "claimed"
)

// T209ClaimOutcome tells the caller what happened to a claim request.
type T209ClaimOutcome string

const (
	// T209ClaimGranted means a fresh lease was created.
	T209ClaimGranted T209ClaimOutcome = "granted"
	// T209ClaimReclaimed means an expired lease was recovered first.
	T209ClaimReclaimed T209ClaimOutcome = "reclaimed"
	// T209ClaimConflict means a live lease blocked the request.
	T209ClaimConflict T209ClaimOutcome = "conflict"
)

// T209ReasonKind separates a worker failure from a lease that ran out of time.
type T209ReasonKind string

const (
	// T209ReasonFail is a failure reported by the worker through Fail.
	T209ReasonFail T209ReasonKind = "fail"
	// T209ReasonTimeout is a worker that never responded inside the lease window.
	T209ReasonTimeout T209ReasonKind = "timeout"
)

// T209Reason is the user-visible explanation of a failed or expired obligation.
// Text is meant to be shown verbatim in the host harness, for example a
// "Try Again" notice in Slack.
type T209Reason struct {
	Kind         T209ReasonKind
	ObligationID string
	WorkerID     string
	Text         string
}

// T209Lease is the reservation a worker holds until ExpiresAt.
type T209Lease struct {
	WorkerID  string
	ClaimedAt time.Time
	ExpiresAt time.Time
}

// Live reports whether the lease still holds at time t.
func (l T209Lease) Live(t time.Time) bool {
	return l.WorkerID != "" && t.Before(l.ExpiresAt)
}

var (
	// T209ErrLeaseLive is returned when a live lease blocks a claim.
	T209ErrLeaseLive = errors.New("wake: obligation has a live lease")
	// T209ErrNoLease is returned when release or fail finds no live lease.
	T209ErrNoLease = errors.New("wake: obligation has no live lease")
	// T209ErrWrongWorker is returned when the caller does not own the lease.
	T209ErrWrongWorker = errors.New("wake: obligation is leased to another worker")
	// T209ErrEmptyReason is returned when fail carries no visible text.
	T209ErrEmptyReason = errors.New("wake: fail reason is empty")
	// T209ErrInvalidTimeout is returned when wakeTimeoutMs is not positive.
	T209ErrInvalidTimeout = errors.New("wake: wakeTimeoutMs must be positive")
	// T209ErrMissingID is returned when an obligation or worker id is empty.
	T209ErrMissingID = errors.New("wake: obligation id and worker id are required")
)

// T209ClaimResult reports the outcome of Claim. On a conflict, T209Lease and
// OwnerWorkerID describe the lease that blocked the request, so a caller that
// lost a response can recognise its own reservation and avoid a second turn.
type T209ClaimResult struct {
	Outcome       T209ClaimOutcome
	T209Lease         T209Lease
	OwnerWorkerID string
}

// T209record is the state kept for one obligation. It holds a lease only while the
// obligation is claimed.
type T209record struct {
	state      T209State
	lease      T209Lease
	lastReason string
}

// T209Manager owns the leases for a set of obligations. It is safe for concurrent
// use. Now and OnReason are optional seams for tests and for wiring the
// published reason into the host harness.
type T209Manager struct {
	WakeTimeoutMs int64
	Now           func() time.Time
	OnReason      func(T209Reason)

	mu      sync.Mutex
	records map[string]*T209record
	reasons []T209Reason
}

// T209NewManager builds a lease manager with the given wake timeout in milliseconds.
func T209NewManager(wakeTimeoutMs int64) *T209Manager {
	return &T209Manager{
		WakeTimeoutMs: wakeTimeoutMs,
		records:       make(map[string]*T209record),
	}
}

func (m *T209Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *T209Manager) recordLocked(id string) *T209record {
	r := m.records[id]
	if r == nil {
		r = &T209record{state: T209StateCreated}
		m.records[id] = r
	}
	return r
}

// publishLocked records a reason and hands it to the sink. The sink must not
// call back into T209Manager while it runs.
func (m *T209Manager) publishLocked(reason T209Reason) {
	m.reasons = append(m.reasons, reason)
	if m.OnReason != nil {
		m.OnReason(reason)
	}
}

// expireLocked returns a claimed obligation to created once its lease window
// is over. It publishes a timeout reason so the worker that never came up is
// visible to the user instead of failing silently. It reports whether it
// expired a lease.
func (m *T209Manager) expireLocked(id string, r *T209record, at time.Time) bool {
	if r.state != T209StateClaimed || r.lease.WorkerID == "" || at.Before(r.lease.ExpiresAt) {
		return false
	}
	worker := r.lease.WorkerID
	r.state = T209StateCreated
	r.lease = T209Lease{}
	m.publishLocked(T209Reason{
		Kind:         T209ReasonTimeout,
		ObligationID: id,
		WorkerID:     worker,
		Text: fmt.Sprintf(
			"wake: worker %q did not respond within %d ms, obligation %s is available again",
			worker, m.WakeTimeoutMs, id,
		),
	})
	return true
}

// Claim reserves an obligation for workerID. A live lease rejects the claim
// with T209ErrLeaseLive. An expired lease is recovered first, published as a
// timeout, and then granted to the new worker.
func (m *T209Manager) Claim(id, workerID string) (T209ClaimResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" || workerID == "" {
		return T209ClaimResult{}, T209ErrMissingID
	}
	if m.WakeTimeoutMs <= 0 {
		return T209ClaimResult{}, T209ErrInvalidTimeout
	}

	at := m.now()
	r := m.recordLocked(id)
	reclaimed := m.expireLocked(id, r, at)
	if r.state == T209StateClaimed {
		return T209ClaimResult{
			Outcome:       T209ClaimConflict,
			T209Lease:         r.lease,
			OwnerWorkerID: r.lease.WorkerID,
		}, T209ErrLeaseLive
	}

	outcome := T209ClaimGranted
	if reclaimed {
		outcome = T209ClaimReclaimed
	}
	r.state = T209StateClaimed
	r.lease = T209Lease{
		WorkerID:  workerID,
		ClaimedAt: at,
		ExpiresAt: at.Add(time.Duration(m.WakeTimeoutMs) * time.Millisecond),
	}
	return T209ClaimResult{
		Outcome:       outcome,
		T209Lease:         r.lease,
		OwnerWorkerID: workerID,
	}, nil
}

// Release frees a live lease so another claim can take the obligation. It is
// the path for a lost wake response: the worker that claimed may release
// before trying again. A different worker cannot release someone else's lease.
func (m *T209Manager) Release(id, workerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.records[id]
	if r == nil {
		return T209ErrNoLease
	}
	m.expireLocked(id, r, m.now())
	if r.state != T209StateClaimed {
		return T209ErrNoLease
	}
	if r.lease.WorkerID != workerID {
		return T209ErrWrongWorker
	}
	r.state = T209StateCreated
	r.lease = T209Lease{}
	return nil
}

// Fail reports a worker failure with text the user can see. It publishes the
// reason, frees the lease, and leaves the obligation available for a retry.
func (m *T209Manager) Fail(id, workerID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if text == "" {
		return T209ErrEmptyReason
	}
	r := m.records[id]
	if r == nil {
		return T209ErrNoLease
	}
	m.expireLocked(id, r, m.now())
	if r.state != T209StateClaimed {
		return T209ErrNoLease
	}
	if r.lease.WorkerID != workerID {
		return T209ErrWrongWorker
	}
	r.lastReason = text
	r.state = T209StateCreated
	r.lease = T209Lease{}
	m.publishLocked(T209Reason{
		Kind:         T209ReasonFail,
		ObligationID: id,
		WorkerID:     workerID,
		Text:         text,
	})
	return nil
}

// T209State returns the effective state of an obligation. Reading the state of an
// expired lease also publishes its timeout reason.
func (m *T209Manager) T209State(id string) T209State {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.records[id]
	if r == nil {
		return T209StateCreated
	}
	m.expireLocked(id, r, m.now())
	return r.state
}

// LeaseOf returns the current lease and whether it is live at the current
// time. It does not expire the lease.
func (m *T209Manager) LeaseOf(id string) (T209Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.records[id]
	if r == nil || r.state != T209StateClaimed {
		return T209Lease{}, false
	}
	lease := r.lease
	return lease, lease.Live(m.now())
}

// Reasons returns a copy of every published reason in publication order.
func (m *T209Manager) Reasons() []T209Reason {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]T209Reason(nil), m.reasons...)
}

