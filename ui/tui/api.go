package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	apiRunsPath    = "/api/v1/runs"
	apiEventsPath  = "/api/v1/events"
	apiControlPath = "/api/v1/control"
	requestTimeout = 10 * time.Second
	responseLimit  = 4 << 20
)

// Run is the monitor's copy of the daemon's loopback wire run. The TUI is a
// separate module and cannot import pkg/core, so these field names must stay
// in sync with the DTO the daemon emits.
type Run struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	PR        int    `json:"pr"`
	HeadSHA   string `json:"head_sha"`
	Branch    string `json:"branch"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
}

// Action is a control verb sent to the loopback API. The TUI is a separate
// module and cannot import pkg/core, so this list mirrors core.ControlActions.
type Action string

const (
	ActionApprove Action = "approve"
	ActionUnblock Action = "unblock"
	ActionCancel  Action = "cancel"
	ActionRetry   Action = "retry"
	ActionNudge   Action = "nudge"
	ActionWatch   Action = "watch"
)

// actions is the single vocabulary used by both the parser and the validator.
var actions = [...]Action{
	ActionApprove,
	ActionUnblock,
	ActionCancel,
	ActionRetry,
	ActionNudge,
	ActionWatch,
}

// ParseAction converts a wire verb into its typed form. Unknown verbs keep
// their text so the caller can report what it received.
func ParseAction(raw string) Action {
	return Action(raw)
}

// Valid reports whether a belongs to the control vocabulary.
func (a Action) Valid() bool {
	for _, known := range actions {
		if a == known {
			return true
		}
	}
	return false
}

type ControlRequest struct {
	Action Action `json:"action"`
	RunID  string `json:"run_id"`
	SHA    string `json:"sha,omitempty"`
}

// ControlResult is the monitor's copy of the daemon's loopback control answer.
type ControlResult struct {
	OK      bool   `json:"ok"`
	RunID   string `json:"run_id"`
	Action  string `json:"action"`
	Applied bool   `json:"applied"`
}

type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

func NewClient(endpoint, token string) *Client {
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Token:    token,
		HTTP:     &http.Client{Timeout: requestTimeout},
	}
}

func (c *Client) endpoint() (string, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("tui: invalid endpoint %q", c.Endpoint)
	}
	host := u.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("tui: endpoint must be loopback, got %q", host)
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	base, err := c.endpoint()
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tui: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) Runs(ctx context.Context) ([]Run, error) {
	data, err := c.do(ctx, http.MethodGet, apiRunsPath, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Runs []Run `json:"runs"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Runs != nil {
		return envelope.Runs, nil
	}
	var runs []Run
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("tui: decode runs: %w", err)
	}
	return runs, nil
}

func (c *Client) Control(ctx context.Context, req ControlRequest) (ControlResult, error) {
	if !req.Action.Valid() {
		return ControlResult{}, fmt.Errorf("tui: unknown action %q", req.Action)
	}
	if req.RunID == "" {
		return ControlResult{}, fmt.Errorf("tui: control needs a run id")
	}
	data, err := c.do(ctx, http.MethodPost, apiControlPath, req)
	if err != nil {
		return ControlResult{}, err
	}
	var result ControlResult
	if err := json.Unmarshal(data, &result); err != nil {
		return ControlResult{}, fmt.Errorf("tui: decode control result: %w", err)
	}
	return result, nil
}
