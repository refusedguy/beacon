// Package tui renders Beacon's /api/v1 projection without opening the store.
// The dashboard reads snapshots and events over loopback HTTP and sends human
// commands as REST. Only the server writes state; this client never sees the
// database file.
package tui

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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	snapshotPath = "/api/v1/snapshot"
	eventsPath   = "/api/v1/events"
	decidePath   = "/api/v1/human/decide"

	// maxBodyBytes bounds a single response. A dashboard never needs more than
	// one projected snapshot in memory.
	maxBodyBytes = 1 << 20

	// reconnectDelay is the pause before a dropped event stream is reopened.
	reconnectDelay = time.Second

	// eventReplayCount seeds a cold stream with the tail of the event buffer.
	// The server caps replay at maxReplay and ignores it when Last-Event-ID is
	// present, so reconnect still resumes from the last frame the client saw.
	eventReplayCount = 64

	// defaultPointLimit matches the fallback beacon watch --live prints when
	// the server omits the budget.
	defaultPointLimit = 500
)

var (
	// ErrNoServer marks a transport failure: no listener, a dropped dial, or a
	// cancelled request. Callers show it as a server-unavailable state.
	ErrNoServer = errors.New("tui: server unavailable")
	// ErrNotLoopback marks an endpoint outside the local machine. A dashboard
	// talks to the Beacon server on loopback only.
	ErrNotLoopback = errors.New("tui: endpoint is not loopback")
)

// Doer is the single HTTP seam. *http.Client implements it; tests pass a fake
// or an httptest server client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client reads the server projection and sends commands. A nil doer builds a
// real client without a global timeout, because a SSE request stays open until
// its context is cancelled.
type Client struct {
	baseURL string
	token   string
	doer    Doer

	mu     sync.Mutex
	lastID uint64
}

// NewClient validates the endpoint and builds a client. Only http loopback
// addresses are accepted, so a dashboard cannot be pointed at a remote host.
func NewClient(baseURL, token string, doer Doer) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("tui: parse endpoint: %w", err)
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("tui: endpoint scheme %q is not http", u.Scheme)
	}
	if !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("%w: %s", ErrNotLoopback, u.Hostname())
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("tui: endpoint path %q is not empty", u.Path)
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &Client{
		baseURL: strings.TrimSuffix(u.String(), "/"),
		token:   strings.TrimSpace(token),
		doer:    doer,
	}, nil
}

func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// read consumes a response at the HTTP boundary. It closes the body, bounds
// its size, and turns a non-2xx status into an error with the server's reason.
func (c *Client) read(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("tui: read response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		reason := strings.TrimSpace(string(body))
		if reason == "" {
			reason = resp.Status
		}
		return nil, fmt.Errorf("tui: %s: %s", resp.Status, reason)
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// GET /api/v1/snapshot
// ---------------------------------------------------------------------------

// Snapshot is the read-only projection the dashboard renders. Every field is
// safe to render as empty, so a partial server response cannot break the view.
type Snapshot struct {
	LiveWatches      []Watch   `json:"liveWatches"`
	BlockerIncidents []Blocker `json:"blockerIncidents"`
	PointStats       Points    `json:"pointStats"`
	Escalations      []string  `json:"escalations"`
	LoadErrors       []string  `json:"loadErrors"`
	LastCycle        Cycle     `json:"lastCycle"`
}

// Watch is one live row of the dashboard table.
type Watch struct {
	ID          string       `json:"id"`
	Repo        string       `json:"repo"`
	PR          int          `json:"pr"`
	HeadSHA     string       `json:"headSHA"`
	Status      string       `json:"status"`
	Verdict     string       `json:"verdict"`
	Revision    uint64       `json:"revision"`
	Blocked     *BlockedInfo `json:"blocked,omitempty"`
	Subscribers []Subscriber `json:"subscribers"`
}

// BlockedInfo is the reason a Watch is paused and the number of fix attempts.
// Reason carries one of the domain block codes; the TUI mirrors them locally
// because the layer matrix forbids importing internal/domain here.
type BlockedInfo struct {
	Reason      string `json:"reason"`
	FixAttempts int    `json:"fixAttempts"`
}

// Subscriber is one agent bound to a Watch. Role and Target come from the
// server projection; Phase stays optional for older snapshots.
type Subscriber struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Target string `json:"target"`
	Phase  string `json:"phase"`
}

// Blocker is one open incident shown above the table.
type Blocker struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Session string `json:"session"`
}

// Points is the hourly budget reported by the CI provider. The server sends
// used and budget; other projections send used and limit. Both spellings end
// up in the same fields so the dashboard never prints a zero denominator.
type Points struct {
	Used   int `json:"used"`
	Limit  int `json:"limit"`
	Budget int `json:"budget"`
}

// UnmarshalJSON reads either budget or limit as the hourly cap. It mirrors the
// decoder beacon watch --live uses, including the fallback when neither value
// is present.
func (p *Points) UnmarshalJSON(data []byte) error {
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
	p.Used = firstNonZero(raw.Used, raw.Points, raw.PointsUsed, raw.PointsThisHour)
	p.Budget = firstNonZero(raw.Budget, raw.Limit)
	p.Limit = firstNonZero(raw.Limit, raw.Budget)
	if p.Limit <= 0 {
		p.Limit = defaultPointLimit
		p.Budget = defaultPointLimit
	}
	return nil
}

// firstNonZero returns the first positive value in order.
func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// Cycle is the most recent completed reconciler pass.
type Cycle struct {
	At             time.Time `json:"at"`
	WatchesUpdated int       `json:"watchesUpdated"`
	DurationMs     int64     `json:"durationMs"`
}

// Snapshot reads GET /api/v1/snapshot. One call yields a complete view; an
// event only says when to read it again.
func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+snapshotPath, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("tui: %w", err)
	}
	c.authorize(req)
	resp, err := c.doer.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: GET %s: %v", ErrNoServer, snapshotPath, err)
	}
	body, err := c.read(resp)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("tui: decode snapshot: %w", err)
	}
	return snap, nil
}

// ---------------------------------------------------------------------------
// POST /api/v1/human/decide
// ---------------------------------------------------------------------------

// Decision is one human command. Command is one verb the server accepts, and
// WatchID names its target. ObligationID and Note are optional.
type Decision struct {
	Command      string `json:"command"`
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID,omitempty"`
	Note         string `json:"note,omitempty"`
	Actor        string `json:"actor,omitempty"`
}

// Receipt is the server's answer. OK false means the command was refused.
type Receipt struct {
	OK         bool   `json:"ok"`
	Command    string `json:"command"`
	WatchID    string `json:"watchID"`
	Applied    bool   `json:"applied"`
	DecisionID string `json:"decisionID"`
	Status     string `json:"status,omitempty"`
}

// Decide posts one command with the bearer token from the endpoint config. It
// validates its input at the boundary and returns an error when the server
// refuses the command, so a caller never reports success by accident.
func (c *Client) Decide(ctx context.Context, d Decision) (Receipt, error) {
	d.Command = strings.ToLower(strings.TrimSpace(d.Command))
	d.WatchID = strings.TrimSpace(d.WatchID)
	if d.Command == "" || d.WatchID == "" {
		return Receipt{}, errors.New("tui: command and watchID are required")
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return Receipt{}, fmt.Errorf("tui: encode decision: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+decidePath, bytes.NewReader(payload))
	if err != nil {
		return Receipt{}, fmt.Errorf("tui: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.doer.Do(req)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: POST %s: %v", ErrNoServer, decidePath, err)
	}
	body, err := c.read(resp)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("tui: decode receipt: %w", err)
	}
	if !receipt.OK {
		return receipt, fmt.Errorf("tui: server refused %s on %s: %s", d.Command, d.WatchID, receipt.Status)
	}
	return receipt, nil
}

// ---------------------------------------------------------------------------
// GET /api/v1/events (SSE)
// ---------------------------------------------------------------------------

// Event is one SSE frame. Data stays raw because a refresh re-reads the
// snapshot instead of interpreting the notification body.
type Event struct {
	ID   string
	Type string
	Data json.RawMessage
}

// EventResult carries either a frame or a transport error. A closed channel
// means the stream ended and the caller should resubscribe.
type EventResult struct {
	Event Event
	Err   error
}

// Events subscribes to GET /api/v1/events. A cold start asks for a replay tail;
// a resubscribe sends Last-Event-ID so the server returns only missed frames.
// Cancel ctx to stop the goroutine and close the channel.
func (c *Client) Events(ctx context.Context) <-chan EventResult {
	out := make(chan EventResult)
	go func() {
		defer close(out)
		resp, err := c.openEvents(ctx)
		if err != nil {
			sendEvent(ctx, out, EventResult{Err: err})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			reason := strings.TrimSpace(string(body))
			if reason == "" {
				reason = resp.Status
			}
			sendEvent(ctx, out, EventResult{Err: fmt.Errorf("tui: events: %s", reason)})
			return
		}
		c.consumeEvents(ctx, resp.Body, out)
	}()
	return out
}

func (c *Client) openEvents(ctx context.Context) (*http.Response, error) {
	u, err := url.Parse(c.baseURL + eventsPath)
	if err != nil {
		return nil, fmt.Errorf("tui: events endpoint: %w", err)
	}
	query := u.Query()
	query.Set("replay", strconv.Itoa(eventReplayCount))
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("tui: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	c.authorize(req)
	c.mu.Lock()
	lastID := c.lastID
	c.mu.Unlock()
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(lastID, 10))
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s: %v", ErrNoServer, eventsPath, err)
	}
	return resp, nil
}

func sendEvent(ctx context.Context, out chan<- EventResult, result EventResult) bool {
	select {
	case out <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

// consumeEvents scans SSE lines until the body ends or ctx is cancelled. A
// second goroutine closes the body on cancellation so a blocked Scan returns.
func (c *Client) consumeEvents(ctx context.Context, body io.ReadCloser, out chan<- EventResult) {
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
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			c.mu.Lock()
			if n > c.lastID {
				c.lastID = n
			}
			c.mu.Unlock()
		}
		return sendEvent(ctx, out, EventResult{Event: Event{
			ID:   id,
			Type: eventType,
			Data: []byte(strings.Join(dataLines, "\n")),
		}})
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxBodyBytes)
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
			// retry: and heartbeat comments carry no payload.
		}
	}
	if len(dataLines) > 0 {
		emit(id, eventType, dataLines)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		sendEvent(ctx, out, EventResult{Err: fmt.Errorf("tui: events: %w", err)})
	}
}
