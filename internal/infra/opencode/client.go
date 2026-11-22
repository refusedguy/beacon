// Package opencode is a thin reader of the HTTP API that opencode already
// opens at the address from service.json. It speaks no agent protocol and
// knows no names from the Beacon domain core (ADR 0006, ADR 0024, ADR 0060 п1).
//
// The package lives in internal/infra because it owns the network capability:
// the capabilities gate forbids net/http in cmd/beacon (ADR 0005). The CLI
// imports this package through an allowed cmd -> internal/infra edge.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authUser = "opencode"

	// sessionListTTL keeps the event loop from re-reading GET /api/session on
	// every event (ADR 0060 п6). Zero disables the cache.
	sessionListTTL = 2 * time.Second

	// reconnectWait is the pause before an SSE reconnect. The client never
	// replays missed events; the caller re-reads state instead (ADR 0060 п2).
	reconnectWait = 500 * time.Millisecond

	// maxSSEFrameBytes and maxHTTPBodyBytes bound memory for service input
	// (ADR 0060 п6).
	maxSSEFrameBytes = 1 << 20
	maxHTTPBodyBytes = 1 << 20
)

// Doer is the single HTTP seam. *http.Client implements it, and tests pass a
// fake so no real server is needed.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client reads the HTTP API at the address from service.json.
type Client struct {
	baseURL  string
	password string
	doer     Doer

	sessionTTL    time.Duration
	reconnectWait time.Duration
	now           func() time.Time

	mu             sync.Mutex
	cachedSessions []Session
	cachedAt       time.Time
}

// New validates the address from service.json and builds a client. A nil doer
// means a real *http.Client with no global timeout: SSE holds a connection
// open for a long time, and callers cancel via context.
func New(cfg Config, doer Doer) (*Client, error) {
	raw := strings.TrimSpace(cfg.URL)
	if raw == "" {
		return nil, errors.New("пустой url сервиса opencode")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("неверный url сервиса opencode %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("неверная схема url сервиса opencode %q", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url сервиса opencode без хоста: %q", raw)
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &Client{
		baseURL:       strings.TrimRight(raw, "/"),
		password:      cfg.Password,
		doer:          doer,
		sessionTTL:    sessionListTTL,
		reconnectWait: reconnectWait,
		now:           time.Now,
	}, nil
}

// Agent is one entry of GET /api/agent. The primary source of agents is the
// agent field of a session; this route is kept for compatibility with older
// OpenCode versions (ADR 0060 п2).
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// modelID is the model field of a GET /api/session row. opencode v2 reports it
// as an object {"id","providerID","variant"}; earlier versions and the test
// stand-in report a plain string. UnmarshalJSON accepts both and keeps the id,
// so callers always read a model id as a string (ADR 0060, T3.148).
type modelID string

func (m *modelID) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	switch data[0] {
	case '{':
		// v2 object form: take the id and ignore providerID/variant.
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		*m = modelID(obj.ID)
	case '"':
		// Legacy plain-string form, kept for backward compatibility.
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*m = modelID(s)
	case 'n': // null
		*m = ""
	default:
		return fmt.Errorf("model: ожидалась строка или объект с id, получено %s", data)
	}
	return nil
}

// Session is one entry of GET /api/session. Fields follow the v2 response
// shape recorded in ADR 0060.
type Session struct {
	ID       string          `json:"id"`
	ParentID string          `json:"parentID"`
	Agent    string          `json:"agent"`
	Status   json.RawMessage `json:"status"`
	ModelID  modelID         `json:"model"`
	Title    string          `json:"title"`
	Time     struct {
		Busy    int64  `json:"busy"`
		Idle    int64  `json:"idle"`
		Outcome string `json:"outcome"`
	} `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// ParseSessions is the single reader of a session.list payload. It accepts the
// {"data":[...]} envelope returned by the API and by `opencode api`, as well as
// a bare array for a stand-in. Every caller of session.list uses this function,
// so the wire shape is decoded in one place (ADR 0060, ADR 0092).
func ParseSessions(raw []byte) ([]Session, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) != 0 {
		var sessions []Session
		if err := json.Unmarshal(envelope.Data, &sessions); err == nil {
			return sessions, nil
		}
	}
	var sessions []Session
	if err := json.Unmarshal(raw, &sessions); err != nil {
		return nil, fmt.Errorf("session.list: %w", err)
	}
	return sessions, nil
}

// list is the envelope used by the agent route: {"data":[...],"location":{...}}.
type list[T any] struct {
	Data     T `json:"data"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

func decodeList[T any](resp *http.Response) (list[T], error) {
	defer resp.Body.Close()
	var out list[T]
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHTTPBodyBytes)).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

// get performs one authenticated GET. Callers own resp.Body on success; on a
// non-2xx status the body is closed here and an error is returned.
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(authUser, c.password)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("opencode %s: %s: %s", path, resp.Status, msg)
	}
	return resp, nil
}

// post performs one authenticated JSON POST. As with get, the caller owns
// resp.Body on success; on a non-2xx status the body is closed here.
func (c *Client) post(ctx context.Context, path string, payload any) (*http.Response, error) {
	return c.send(ctx, http.MethodPost, path, payload)
}

// patch performs one authenticated JSON PATCH (the inbox promote route).
func (c *Client) patch(ctx context.Context, path string, payload any) (*http.Response, error) {
	return c.send(ctx, http.MethodPatch, path, payload)
}

func (c *Client) send(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("opencode %s %s: %w", method, path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(authUser, c.password)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("opencode %s %s: %s: %s", method, path, resp.Status, msg)
	}
	return resp, nil
}

// decodeSingle reads one object from the {"data":{...}} envelope used by the
// session routes. It also accepts a bare object so a handler that omits the
// envelope still works. The body is closed here, like in decodeList.
func decodeSingle[T any](resp *http.Response) (T, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes))
	if err != nil {
		var zero T
		return zero, err
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		var zero T
		return zero, err
	}
	var out T
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, &out); err != nil {
			var zero T
			return zero, err
		}
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// ListAgents reads GET /api/agent.
func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	resp, err := c.get(ctx, "/api/agent")
	if err != nil {
		return nil, err
	}
	decoded, err := decodeList[[]Agent](resp)
	if err != nil {
		return nil, fmt.Errorf("opencode /api/agent: %w", err)
	}
	return decoded.Data, nil
}

// Sessions reads GET /api/session with a TTL cache. A cache hit returns a copy
// so a caller cannot mutate the cached slice. The payload is decoded by
// ParseSessions, the same decoder used for every other session.list response.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	c.mu.Lock()
	if c.sessionTTL > 0 && c.cachedSessions != nil && c.now().Sub(c.cachedAt) < c.sessionTTL {
		out := append([]Session(nil), c.cachedSessions...)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	resp, err := c.get(ctx, "/api/session")
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("opencode /api/session: %w", err)
	}
	sessions, err := ParseSessions(body)
	if err != nil {
		return nil, fmt.Errorf("opencode /api/session: %w", err)
	}

	if c.sessionTTL > 0 {
		c.mu.Lock()
		c.cachedSessions = append([]Session(nil), sessions...)
		c.cachedAt = c.now()
		c.mu.Unlock()
	}
	return sessions, nil
}

// CreateSession posts POST /api/session and returns the created session. The
// agent is set here because the v2 prompt route has no agent field (ADR 0060
// п3/п4).
func (c *Client) CreateSession(ctx context.Context, parentID, agent, title string, metadata map[string]string) (Session, error) {
	payload := struct {
		ParentID string            `json:"parentID"`
		Agent    string            `json:"agent"`
		Title    string            `json:"title"`
		Metadata map[string]string `json:"metadata"`
	}{
		ParentID: parentID,
		Agent:    agent,
		Title:    title,
		Metadata: metadata,
	}
	resp, err := c.post(ctx, "/api/session", payload)
	if err != nil {
		return Session{}, err
	}
	// POST /api/session answers with the {"data":{...}} envelope. decodeSingle
	// also accepts a bare object for handlers that omit the envelope.
	session, err := decodeSingle[Session](resp)
	if err != nil {
		return Session{}, fmt.Errorf("opencode POST /api/session: %w", err)
	}
	return session, nil
}

// Prompt posts POST /api/session/:sessionID/prompt. It sends only the text:
// the v2 route has no agent field, so the agent comes from the session created
// by CreateSession (ADR 0060 п3/п4). Callers that need resume or delivery use
// PromptWithOptions (ADR 0306).
func (c *Client) Prompt(ctx context.Context, sessionID, text string) error {
	return c.PromptWithOptions(ctx, sessionID, text, PromptOptions{})
}

// PromptOptions are the v2 session.prompt fields the first prompt of a created
// session needs (ADR 0306). MessageID becomes the body's id field; a repeat of
// the same id does not start a second turn. Delivery and Resume let a session
// that finished its turn continue instead of restarting its context.
type PromptOptions struct {
	MessageID string
	Delivery  string
	Resume    bool
}

// MessageIDForObligation derives a deterministic messageID from an obligationID.
// The msg_ prefix keeps it distinct from obligation and watch ids, and the live
// OpenAPI names the request field id (ADR 0306).
func MessageIDForObligation(obligationID string) string {
	return "msg_" + obligationID
}

// StartPromptOptions is the option set for the first prompt of a session
// created from an obligation. Sending delivery steer with resume true makes the
// created session continue its turn (ADR 0306).
func StartPromptOptions(obligationID string) PromptOptions {
	return PromptOptions{
		MessageID: MessageIDForObligation(obligationID),
		Delivery:  "steer",
		Resume:    true,
	}
}

// PromptWithOptions posts POST /api/session/:sessionID/prompt with the
// extended v2 body. The agent field stays absent: it comes from the session
// created by CreateSession (ADR 0060, ADR 0075). Plain Prompt passes empty
// options, so its body still carries only text.
func (c *Client) PromptWithOptions(ctx context.Context, sessionID, text string, opts PromptOptions) error {
	payload := struct {
		ID       string `json:"id,omitempty"`
		Text     string `json:"text"`
		Delivery string `json:"delivery,omitempty"`
		Resume   bool   `json:"resume,omitempty"`
	}{
		ID:       opts.MessageID,
		Text:     text,
		Delivery: opts.Delivery,
		Resume:   opts.Resume,
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/prompt"
	resp, err := c.post(ctx, path, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Config reads the raw body of GET /api/config. The config shape belongs to
// opencode, so the client does not guess it. The same route is the health probe
// of the legacy spawn path in server.go.
func (c *Client) Config(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.get(ctx, "/api/config")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("opencode /api/config: %w", err)
	}
	return json.RawMessage(data), nil
}

// Interrupt posts POST /api/session/:id/interrupt?resume=<bool>. The wake path
// used to reach this route through `opencode api`; it now uses the one client
// (ADR 0079, ADR 0092).
func (c *Client) Interrupt(ctx context.Context, sessionID string, resume bool) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/interrupt?resume=" + strconv.FormatBool(resume)
	resp, err := c.post(ctx, path, struct{}{})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Promote patches one inbox item of a session so a parked input becomes steer.
// This is the PATCH /api/session/:id/inbox/:inboxID route of ADR 0076.
func (c *Client) Promote(ctx context.Context, sessionID, inboxID string) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox/" + url.PathEscape(inboxID)
	resp, err := c.patch(ctx, path, map[string]string{"delivery": "steer"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Event is one parsed SSE frame from GET /api/event.
type Event struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// EventResult carries either an event or a stream/reconnect error to the
// caller, so it can decide how to re-read state after a break.
type EventResult struct {
	Event Event
	Err   error
}

// EventStream subscribes to GET /api/event?types=... A long pause with no data
// is normal. After each break the client reconnects without replaying history;
// the caller re-reads the snapshot and sessions on reconnect (ADR 0060 п2).
// Cancel ctx to stop the goroutine and close the channel.
func (c *Client) EventStream(ctx context.Context, types []string) <-chan EventResult {
	out := make(chan EventResult)
	go func() {
		defer close(out)
		for {
			if ctx.Err() != nil {
				return
			}
			resp, err := c.openEvent(ctx, types)
			if err != nil {
				if !c.waitReconnect(ctx, out, err) {
					return
				}
				continue
			}
			c.consumeEvent(ctx, resp.Body, out)
			_ = resp.Body.Close()
			if ctx.Err() != nil {
				return
			}
			if !c.waitReconnect(ctx, out, nil) {
				return
			}
		}
	}()
	return out
}

func (c *Client) openEvent(ctx context.Context, types []string) (*http.Response, error) {
	path := "/api/event"
	if len(types) > 0 {
		q := url.Values{}
		q.Set("types", strings.Join(types, ","))
		path += "?" + q.Encode()
	}
	return c.get(ctx, path)
}

// waitReconnect reports a transport error, then waits before reconnecting.
// It returns false when ctx is cancelled.
func (c *Client) waitReconnect(ctx context.Context, out chan<- EventResult, cause error) bool {
	if cause != nil {
		select {
		case out <- EventResult{Err: cause}:
		case <-ctx.Done():
			return false
		}
	}
	wait := c.reconnectWait
	if wait <= 0 {
		wait = reconnectWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// consumeEvent scans SSE lines until the body ends or ctx is cancelled. A
// second goroutine closes the body on cancellation so a blocked Scan returns.
func (c *Client) consumeEvent(ctx context.Context, body io.ReadCloser, out chan<- EventResult) {
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()
		case <-stop:
		}
		close(stopped)
	}()
	defer func() {
		close(stop)
		<-stopped
	}()

	emit := func(lines []string) bool {
		if len(lines) == 0 {
			return true
		}
		payload := strings.Join(lines, "\n")
		var ev Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			select {
			case out <- EventResult{Err: fmt.Errorf("opencode event: %w", err)}:
			case <-ctx.Done():
				return false
			}
			return true
		}
		select {
		case out <- EventResult{Event: ev}:
		case <-ctx.Done():
			return false
		}
		return true
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEFrameBytes)
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if !emit(dataLines) {
				return
			}
			dataLines = nil
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:, id:, retry: and comments carry no payload here.
		}
	}
	if len(dataLines) > 0 {
		emit(dataLines)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case out <- EventResult{Err: fmt.Errorf("opencode event stream: %w", err)}:
		case <-ctx.Done():
		}
	}
}
