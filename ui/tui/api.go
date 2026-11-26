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

// apiRunsPath, apiEventsPath and apiControlPath are the loopback routes; requestTimeout and responseLimit bound requests.
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
// TestControlVocabularyMatchesCore compares the two and fails on drift.
type Action string

// ActionApprove and the other constants name every verb in the control vocabulary.
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

// ControlRequest is the monitor's copy of the loopback control request.
type ControlRequest struct {
	Action Action `json:"action"`
	RunID  string `json:"run_id"`
	SHA    string `json:"sha,omitempty"`
}

// ControlResult is the monitor's copy of the daemon's loopback control answer.
// Reason carries the daemon's explanation when a control action is refused.
type ControlResult struct {
	OK      bool   `json:"ok"`
	RunID   string `json:"run_id"`
	Action  string `json:"action"`
	Applied bool   `json:"applied"`
	Reason  string `json:"reason"`
}

// applyAuth sets the Authorization header on a request. A token of the form
// user:pass uses HTTP Basic; any other token uses a Bearer header. This is the
// only place the TUI builds an auth header, so the runs and event paths cannot
// send different credentials. It is a variable so tests can replace it and
// prove both paths route through it.
var applyAuth = func(req *http.Request, token string) {
	if user, pass, ok := strings.Cut(token, ":"); ok {
		req.SetBasicAuth(user, pass)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// Client is a loopback HTTP client for the Beacon daemon API.
type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

// NewClient builds a Client for the loopback endpoint with bearer token auth.
func NewClient(endpoint, token string) *Client {
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Token:    token,
		HTTP:     &http.Client{Timeout: requestTimeout},
	}
}

// endpoint validates the configured endpoint and returns its canonical form.
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

// do sends an HTTP request to the daemon and returns the response body and
// status. A non-2xx status also returns an error. The body is still returned so
// Control can read a structured refusal from the wire.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	base, err := c.endpoint()
	if err != nil {
		return nil, 0, err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		applyAuth(req, c.Token)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("tui: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, resp.StatusCode, nil
}

// Runs fetches the current run snapshot from the daemon.
func (c *Client) Runs(ctx context.Context) ([]Run, error) {
	data, _, err := c.do(ctx, http.MethodGet, apiRunsPath, nil)
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

// Control sends a control action for a run and returns the daemon's answer. A
// refusal is a wire result with OK false, not a transport error, so the daemon's
// reason reaches the monitor. Other non-2xx statuses stay errors.
func (c *Client) Control(ctx context.Context, req ControlRequest) (ControlResult, error) {
	if !req.Action.Valid() {
		return ControlResult{}, fmt.Errorf("tui: unknown action %q", req.Action)
	}
	if req.RunID == "" {
		return ControlResult{}, fmt.Errorf("tui: control needs a run id")
	}
	data, status, statusErr := c.do(ctx, http.MethodPost, apiControlPath, req)
	if status == http.StatusConflict && statusErr != nil {
		var refusal ControlResult
		if json.Unmarshal(data, &refusal) == nil && !refusal.OK {
			return refusal, nil
		}
	}
	if statusErr != nil {
		return ControlResult{}, statusErr
	}
	var result ControlResult
	if err := json.Unmarshal(data, &result); err != nil {
		return ControlResult{}, fmt.Errorf("tui: decode control result: %w", err)
	}
	return result, nil
}
