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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// readFile is replaceable in tests.
var readFile = os.ReadFile

// Config is the part of $XDG_STATE_HOME/opencode/service.json that the thin
// client needs. Beacon only reads the registration file of an already running
// service; it never starts one while the file exists (ADR 0029).
type Config struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}

// ReadConfig reads and validates the service registration file. A missing,
// unreadable or malformed file is an error.
func ReadConfig() (Config, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("не удалось определить домашний каталог: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	path := filepath.Join(base, "opencode", "service.json")

	data, err := readFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %v", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("%s: неверный registration-файл: %v", path, err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL == "" {
		return Config{}, fmt.Errorf("%s: пустой url в registration-файле", path)
	}
	return cfg, nil
}

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
		return nil, fmt.Errorf("неверный url сервиса opencode %q: %v", raw, err)
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
// agent field of a session; this route is secondary (ADR 0060 п2).
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// Session is one entry of GET /api/session. Fields follow the v2 response
// shape recorded in ADR 0060.
type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	Agent    string `json:"agent"`
	ModelID  string `json:"model"`
	Title    string `json:"title"`
	Time     struct {
		Busy    int64  `json:"busy"`
		Idle    int64  `json:"idle"`
		Outcome string `json:"outcome"`
	} `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// list is the envelope used by the agent and session routes:
// {"data":[...],"location":{...}}.
type list[T any] struct {
	Data     T `json:"data"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
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

func decodeList[T any](resp *http.Response) (list[T], error) {
	defer resp.Body.Close()
	var out list[T]
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHTTPBodyBytes)).Decode(&out); err != nil {
		return out, err
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
		return nil, fmt.Errorf("opencode /api/agent: %v", err)
	}
	return decoded.Data, nil
}

// Sessions reads GET /api/session with a TTL cache. A cache hit returns a copy
// so a caller cannot mutate the cached slice.
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
	decoded, err := decodeList[[]Session](resp)
	if err != nil {
		return nil, fmt.Errorf("opencode /api/session: %v", err)
	}

	if c.sessionTTL > 0 {
		c.mu.Lock()
		c.cachedSessions = append([]Session(nil), decoded.Data...)
		c.cachedAt = c.now()
		c.mu.Unlock()
	}
	return decoded.Data, nil
}

// Config reads the raw body of GET /api/config. The config shape belongs to
// opencode, so the client does not guess it.
func (c *Client) Config(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.get(ctx, "/api/config")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("opencode /api/config: %v", err)
	}
	return json.RawMessage(data), nil
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
			case out <- EventResult{Err: fmt.Errorf("opencode event: %v", err)}:
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
		case out <- EventResult{Err: fmt.Errorf("opencode event stream: %v", err)}:
		case <-ctx.Done():
		}
	}
}
