// Package httpapi is the thin client of Beacon's own HTTP projection.
//
// The CLI may not open sockets itself (ADR 0005), so every byte that crosses
// the wire lives here. The endpoint comes from <stateDir>/connection.json, the
// same file the MCP client reads (ADR 0194). The client speaks three routes of
// the server projection: a snapshot, an SSE event stream and human decisions.
// It knows no domain names: projections are plain JSON shapes (ADR 0191).
package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// clientConnectionFileName sits next to beacon.db in the state directory.
	clientConnectionFileName = "connection.json"

	clientSnapshotPath = "/api/v1/snapshot"
	clientEventsPath   = "/api/v1/events"
	clientDecidePath   = "/api/v1/human/decide"

	// clientDefaultPointLimit is the hourly budget shown when a server omits it.
	clientDefaultPointLimit = 500

	clientMaxFrameBytes = 1 << 20
)

var (
	// ErrNoConnection means connection.json is absent. Callers report a clear
	// configuration error instead of dialing a made-up address.
	ErrNoConnection = errors.New("httpapi: connection.json missing")
	// ErrNoServer covers a transport failure: no listener, dropped dial or a
	// cancelled connection. A human command uses it to fall back to SQLite.
	ErrNoServer = errors.New("httpapi: server unavailable")
)

// readFile is replaceable in tests.
var readFile = os.ReadFile

// ClientConn is the endpoint record written by the server into connection.json.
type ClientConn struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// Validate rejects a record that cannot name a listener. A missing token is
// allowed: a local server may run without authentication.
func (c ClientConn) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("пустой host в connection.json")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("порт %d вне диапазона 1..65535", c.Port)
	}
	return nil
}

// Address is the host:port pair of the endpoint.
func (c ClientConn) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// LoadClientConn reads and validates <dir>/connection.json. A missing file is
// a typed error so a CLI can explain that the live projection is not configured.
func LoadClientConn(dir string) (ClientConn, error) {
	path := filepath.Join(dir, clientConnectionFileName)
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ClientConn{}, fmt.Errorf("%w: %s", ErrNoConnection, path)
		}
		return ClientConn{}, fmt.Errorf("httpapi: read %s: %w", path, err)
	}
	var conn ClientConn
	if err := json.Unmarshal(data, &conn); err != nil {
		return ClientConn{}, fmt.Errorf("httpapi: decode %s: %w", path, err)
	}
	if err := conn.Validate(); err != nil {
		return ClientConn{}, fmt.Errorf("httpapi: %s: %w", path, err)
	}
	return conn, nil
}

// ClientDoer is the one HTTP seam. *http.Client implements it; tests pass a fake.
type ClientDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// ProjectionClient reads and writes the /api/v1 projection at the endpoint from
// connection.json. A nil doer builds a real client with no global timeout:
// SSE holds a connection open and callers cancel through context.
type ProjectionClient struct {
	conn    ClientConn
	baseURL string
	doer    ClientDoer

	mu     sync.Mutex
	lastID string
}

// NewProjectionClient builds a client for conn. The address is always http://host:port; a
// server on a loopback interface owns TLS termination elsewhere if needed.
func NewProjectionClient(conn ClientConn, doer ClientDoer) (*ProjectionClient, error) {
	if err := conn.Validate(); err != nil {
		return nil, err
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &ProjectionClient{
		conn:    conn,
		baseURL: "http://" + conn.Address(),
		doer:    doer,
	}, nil
}

// Connection returns the endpoint the client was built for.
func (c *ProjectionClient) Connection() ClientConn { return c.conn }

func (c *ProjectionClient) authorize(req *http.Request) {
	if token := strings.TrimSpace(c.conn.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *ProjectionClient) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("httpapi: %w", err)
	}
	c.authorize(req)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s: %v", ErrNoServer, path, err)
	}
	return resp, nil
}

func clientReadResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, clientMaxFrameBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("httpapi: %s: %s", resp.Status, detail)
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// GET /api/v1/snapshot
// ---------------------------------------------------------------------------

// ClientSnapshot is the read-only projection printed by beacon watch --live. The
// zero value of every field is renderable as a dash, so a partial server never
// breaks the table.
type ClientSnapshot struct {
	Project      string             `json:"project"`
	Points       ClientPoints       `json:"points"`
	Cycle        ClientCycle        `json:"cycle"`
	Orchestrator ClientOrchestrator `json:"orchestrator"`
	Watches      []ClientWatch      `json:"watches"`
	Attention    []string           `json:"attention"`
	Escalations  []string           `json:"escalations"`
	// EventTail holds the newest replayed event lines when the server sends
	// them. The field stays optional so an older projection still decodes.
	EventTail []string `json:"eventTail"`
}

// UnmarshalJSON keeps attention and escalations readable whether the server
// sends plain strings or objects carrying a message.
func (s *ClientSnapshot) UnmarshalJSON(data []byte) error {
	type plain ClientSnapshot
	var raw struct {
		plain
		Attention   json.RawMessage `json:"attention"`
		Escalations json.RawMessage `json:"escalations"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = ClientSnapshot(raw.plain)
	attention, err := clientDecodeStrings(raw.Attention)
	if err != nil {
		return fmt.Errorf("httpapi: attention: %w", err)
	}
	escalations, err := clientDecodeStrings(raw.Escalations)
	if err != nil {
		return fmt.Errorf("httpapi: escalations: %w", err)
	}
	s.Attention, s.Escalations = attention, escalations
	return nil
}

// clientDecodeStrings reads a JSON array of strings or of objects with a message.
func clientDecodeStrings(data []byte) ([]string, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err == nil {
		return values, nil
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(data, &objects); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(objects))
	for _, object := range objects {
		for _, key := range []string{"message", "text", "reason", "summary"} {
			value, ok := object[key]
			if !ok {
				continue
			}
			var text string
			if json.Unmarshal(value, &text) == nil && text != "" {
				out = append(out, text)
				break
			}
		}
	}
	return out, nil
}

// ClientPoints is the hourly budget line of the header.
type ClientPoints struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

// UnmarshalJSON accepts the two spellings the projection may use for the
// counters. "used" and "limit" are canonical; the MCP names stay readable.
func (p *ClientPoints) UnmarshalJSON(data []byte) error {
	var raw struct {
		Used           int `json:"used"`
		Points         int `json:"points"`
		PointsUsed     int `json:"pointsUsed"`
		PointsThisHour int `json:"pointsThisHour"`
		Limit          int `json:"limit"`
		Budget         int `json:"budget"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Used = clientFirstNonZero(raw.Used, raw.Points, raw.PointsUsed, raw.PointsThisHour)
	p.Limit = clientFirstNonZero(raw.Limit, raw.Budget)
	if p.Limit <= 0 {
		p.Limit = clientDefaultPointLimit
	}
	return nil
}

// ClientCycle is the last completed orchestrator cycle.
type ClientCycle struct {
	ID       string `json:"id"`
	Duration string `json:"duration"`
}

// UnmarshalJSON accepts cycleID and a millisecond duration alongside the
// canonical names.
func (c *ClientCycle) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          string `json:"id"`
		CycleID     string `json:"cycleID"`
		CycleIDAlt  string `json:"cycleId"`
		Duration    string `json:"duration"`
		DurationMS  int64  `json:"durationMs"`
		DurationMSA int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.ID = clientFirstNonEmpty(raw.ID, raw.CycleID, raw.CycleIDAlt)
	ms := clientFirstNonZeroInt64(raw.DurationMS, raw.DurationMSA)
	if raw.Duration != "" {
		c.Duration = raw.Duration
	} else if ms > 0 {
		c.Duration = (time.Duration(ms) * time.Millisecond).String()
	}
	return nil
}

// ClientOrchestrator reports whether a live manager is bound to this project.
type ClientOrchestrator struct {
	Live    bool   `json:"live"`
	Session string `json:"session"`
}

// UnmarshalJSON accepts sessionID as an alias for session.
func (o *ClientOrchestrator) UnmarshalJSON(data []byte) error {
	var raw struct {
		Live      bool   `json:"live"`
		Session   string `json:"session"`
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	o.Live = raw.Live
	o.Session = clientFirstNonEmpty(raw.Session, raw.SessionID)
	return nil
}

// ClientWatch is one line of the live table. The blocker and phase are folded into
// their parent columns by the renderer, matching the STATUS(BLOCKER) and
// SUBSCRIBER(PHASE) headers.
type ClientWatch struct {
	PR           string `json:"pr"`
	Watch        string `json:"watch"`
	Verdict      string `json:"verdict"`
	Status       string `json:"status"`
	Blocker      string `json:"blocker"`
	Subscriber   string `json:"subscriber"`
	Phase        string `json:"phase"`
	FixAttempts  int    `json:"fixAttempts"`
	WakeAttempts int    `json:"wakeAttempts"`
	Updated      string `json:"updated"`
}

// UnmarshalJSON keeps the server free to use either camelCase watch fields or
// the shorter projection names.
func (w *ClientWatch) UnmarshalJSON(data []byte) error {
	var raw struct {
		PR           string `json:"pr"`
		PRAlt        string `json:"prNumber"`
		Watch        string `json:"watch"`
		WatchID      string `json:"watchID"`
		WatchIDAlt   string `json:"watchId"`
		Verdict      string `json:"verdict"`
		Status       string `json:"status"`
		Blocker      string `json:"blocker"`
		BlockerID    string `json:"blockerID"`
		Subscriber   string `json:"subscriber"`
		SubscriberID string `json:"subscriberSession"`
		Phase        string `json:"phase"`
		FixAttempts  int    `json:"fixAttempts"`
		FixAlt       int    `json:"fix_attempts"`
		WakeAttempts int    `json:"wakeAttempts"`
		WakeAlt      int    `json:"wake_attempts"`
		Updated      string `json:"updated"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	w.PR = clientFirstNonEmpty(raw.PR, raw.PRAlt)
	w.Watch = clientFirstNonEmpty(raw.Watch, raw.WatchID, raw.WatchIDAlt)
	w.Verdict = raw.Verdict
	w.Status = raw.Status
	w.Blocker = clientFirstNonEmpty(raw.Blocker, raw.BlockerID)
	w.Subscriber = clientFirstNonEmpty(raw.Subscriber, raw.SubscriberID)
	w.Phase = raw.Phase
	w.FixAttempts = clientFirstNonZero(raw.FixAttempts, raw.FixAlt)
	w.WakeAttempts = clientFirstNonZero(raw.WakeAttempts, raw.WakeAlt)
	w.Updated = raw.Updated
	return nil
}

// Snapshot reads GET /api/v1/snapshot. One call yields one complete view; the
// event stream only says when to read it again (ADR 0025).
func (c *ProjectionClient) Snapshot(ctx context.Context) (ClientSnapshot, error) {
	resp, err := c.get(ctx, clientSnapshotPath)
	if err != nil {
		return ClientSnapshot{}, err
	}
	body, err := clientReadResponse(resp)
	if err != nil {
		return ClientSnapshot{}, err
	}
	var snap ClientSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return ClientSnapshot{}, fmt.Errorf("httpapi: decode snapshot: %w", err)
	}
	// A projection may wrap its payload in {"data": {...}}. Accept that shape
	// when the outer object carried no projection of its own.
	if snap.Project == "" && len(snap.Watches) == 0 {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
			var inner ClientSnapshot
			if err := json.Unmarshal(envelope.Data, &inner); err == nil {
				snap = inner
			}
		}
	}
	return snap, nil
}

// ---------------------------------------------------------------------------
// GET /api/v1/events (SSE)
// ---------------------------------------------------------------------------

// ClientEvent is one SSE frame. Data stays raw: a refresh re-reads the snapshot
// instead of interpreting the notification body.
type ClientEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ClientEventResult carries either a frame or a transport error. A closed channel
// means the stream ended; the caller resubscribes and re-reads the snapshot.
type ClientEventResult struct {
	Event ClientEvent
	Err   error
}

// Events subscribes to GET /api/v1/events. The last seen id is sent as
// Last-Event-ID on resubscribe, so the server can resume without replaying the
// whole log (ADR 0025). Cancel ctx to stop the goroutine and close the channel.
func (c *ProjectionClient) Events(ctx context.Context) <-chan ClientEventResult {
	out := make(chan ClientEventResult)
	go func() {
		defer close(out)
		resp, err := c.openEvents(ctx)
		if err != nil {
			clientSendEventResult(ctx, out, ClientEventResult{Err: err})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			clientSendEventResult(ctx, out, ClientEventResult{Err: fmt.Errorf("httpapi: events: %s: %s", resp.Status, strings.TrimSpace(string(body)))})
			return
		}
		c.consumeEvents(ctx, resp.Body, out)
	}()
	return out
}

func (c *ProjectionClient) openEvents(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+clientEventsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("httpapi: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	c.authorize(req)
	c.mu.Lock()
	lastID := c.lastID
	c.mu.Unlock()
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s: %v", ErrNoServer, clientEventsPath, err)
	}
	return resp, nil
}

func clientSendEventResult(ctx context.Context, out chan<- ClientEventResult, result ClientEventResult) bool {
	select {
	case out <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

// consumeEvents scans SSE lines until the body ends or ctx is cancelled. A
// second goroutine closes the body on cancellation so a blocked Scan returns.
func (c *ProjectionClient) consumeEvents(ctx context.Context, body io.ReadCloser, out chan<- ClientEventResult) {
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

	emit := func(id, eventType string, dataLines []string) bool {
		if len(dataLines) == 0 {
			return true
		}
		if id != "" {
			c.mu.Lock()
			c.lastID = id
			c.mu.Unlock()
		}
		return clientSendEventResult(ctx, out, ClientEventResult{Event: ClientEvent{
			ID:   id,
			Type: eventType,
			Data: []byte(strings.Join(dataLines, "\n")),
		}})
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), clientMaxFrameBytes)
	var id, eventType string
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if !emit(id, eventType, dataLines) {
				return
			}
			id, eventType, dataLines = "", "", nil
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// retry: and comments carry no payload here.
		}
	}
	if len(dataLines) > 0 {
		emit(id, eventType, dataLines)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		clientSendEventResult(ctx, out, ClientEventResult{Err: fmt.Errorf("httpapi: events: %w", err)})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/human/decide
// ---------------------------------------------------------------------------

// ClientDecision is one human command. Target is a watchID or a blockerID,
// depending on Verb. The server owns the meaning of each verb. Actor carries
// the identity of the caller so an external app can write an answer through the
// same route the TUI uses. Contract names the wire contract version; empty
// selects HumanDecideContract.
type ClientDecision struct {
	Verb     string `json:"verb"`
	Target   string `json:"target"`
	Note     string `json:"note,omitempty"`
	Actor    string `json:"actor,omitempty"`
	Contract string `json:"contract,omitempty"`
	// AppKey identifies the calling application. The server checks it against
	// its credential registry before accepting a decision for Actor.
	AppKey string `json:"appKey,omitempty"`
}

// decideWire is the JSON shape of POST /api/v1/human/decide. It is the same
// command/watchID shape the TUI client and the server use; ClientDecision keeps
// its Verb/Target names for callers and is translated here at the boundary.
type decideWire struct {
	Command  string `json:"command"`
	WatchID  string `json:"watchID"`
	Note     string `json:"note,omitempty"`
	Actor    string `json:"actor,omitempty"`
	Contract string `json:"contract,omitempty"`
	AppKey   string `json:"appKey,omitempty"`
}

// ClientDecisionReceipt is the server receipt. OK false means the command was
// refused; Message carries the reason when the server provides one. Contract is
// the version the server applied.
type ClientDecisionReceipt struct {
	OK       bool   `json:"ok"`
	Applied  bool   `json:"applied,omitempty"`
	Message  string `json:"message,omitempty"`
	Contract string `json:"contract,omitempty"`
}

// Decide posts one decision with the bearer token from connection.json. A
// refused command is an error, so a caller never reports success by accident.
func (c *ProjectionClient) Decide(ctx context.Context, req ClientDecision) (ClientDecisionReceipt, error) {
	contract := strings.TrimSpace(req.Contract)
	if contract == "" {
		contract = HumanDecideContract
	}
	payload, err := json.Marshal(decideWire{
		Command:  strings.TrimSpace(req.Verb),
		WatchID:  strings.TrimSpace(req.Target),
		Note:     req.Note,
		Actor:    req.Actor,
		Contract: contract,
		AppKey:   strings.TrimSpace(req.AppKey),
	})
	if err != nil {
		return ClientDecisionReceipt{}, fmt.Errorf("httpapi: encode decision: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+clientDecidePath, bytes.NewReader(payload))
	if err != nil {
		return ClientDecisionReceipt{}, fmt.Errorf("httpapi: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authorize(httpReq)
	resp, err := c.doer.Do(httpReq)
	if err != nil {
		return ClientDecisionReceipt{}, fmt.Errorf("%w: POST %s: %v", ErrNoServer, clientDecidePath, err)
	}
	body, err := clientReadResponse(resp)
	if err != nil {
		return ClientDecisionReceipt{}, err
	}
	var out ClientDecisionReceipt
	if len(strings.TrimSpace(string(body))) == 0 {
		// An empty 2xx body is a successful, receipt-less decision.
		out.OK = true
	} else {
		var raw struct {
			OK       *bool  `json:"ok"`
			Applied  bool   `json:"applied,omitempty"`
			Message  string `json:"message,omitempty"`
			Contract string `json:"contract,omitempty"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return ClientDecisionReceipt{}, fmt.Errorf("httpapi: decode decision: %w", err)
		}
		out = ClientDecisionReceipt{Applied: raw.Applied, Message: raw.Message, Contract: raw.Contract}
		// A receipt that omits "ok" is still a success; only an explicit false
		// means the server refused the command.
		out.OK = raw.OK == nil || *raw.OK
	}
	if !out.OK {
		reason := out.Message
		if reason == "" {
			reason = "команда отклонена сервером"
		}
		return out, errors.New(reason)
	}
	return out, nil
}

func clientFirstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func clientFirstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func clientFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
