// Package opencode implements the AgentHarness adapter for the OpenCode HTTP API.
package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// requestTimeout bounds each OpenCode HTTP request.
const requestTimeout = 30 * time.Second

// locationReloadAttempts bounds the number of editor location reload tries.
const locationReloadAttempts = 3

// locationReloadBackoff is the pause between location reload retries.
const locationReloadBackoff = 100 * time.Millisecond

// statusClientClosedRequest is the non-standard 499 that OpenCode returns when
// an editor closes a reload request before it finishes.
const statusClientClosedRequest = 499

// Harness is an AgentHarness that talks to a running OpenCode server.
type Harness struct {
	Endpoint        string
	Repo            string
	Model           string
	ModelRef        core.ModelRef
	Agent           string
	Token           string
	ParentSessionID string
	HTTP            *http.Client
	ReconnectMin    time.Duration
	ReconnectMax    time.Duration
	ExcerptLimit    int

	initOnce  sync.Once
	watchOnce sync.Once
	modelOnce sync.Once
	events    chan core.HarnessEvent
	mu        sync.RWMutex
	runs      map[string]core.RunID
	setupWG   sync.WaitGroup

	// modelDefault is the harness-level model resolved once on first use. A
	// model carried by a SpawnRequest still takes precedence per dispatch.
	modelDefault core.ModelRef
}

// modelWire is the vendor wire shape of a model reference. The core model ref
// is transport neutral, so this mapping belongs at the adapter boundary.
type modelWire struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

// wireModel maps a neutral core model ref onto the vendor wire object.
func wireModel(ref core.ModelRef) modelWire {
	return modelWire{ID: ref.ID, ProviderID: ref.ProviderID, Variant: ref.Variant}
}

// Config holds the settings used to build a Harness.
type Config struct {
	Endpoint        string
	Repo            string
	Model           string
	Agent           string
	Token           string
	ParentSessionID string
	Client          *http.Client
}

// New builds a Harness from cfg. A nil Client gets a default client with the
// request timeout.
func New(cfg Config) *Harness {
	h := &Harness{
		Endpoint:        strings.TrimRight(cfg.Endpoint, "/"),
		Repo:            cfg.Repo,
		Model:           cfg.Model,
		Agent:           cfg.Agent,
		Token:           cfg.Token,
		ParentSessionID: cfg.ParentSessionID,
		HTTP:            &http.Client{Timeout: requestTimeout},
	}
	if cfg.Client != nil {
		h.HTTP = cfg.Client
	}
	return h
}

// init lazily fills in defaults and allocates the harness channels and maps.
func (h *Harness) init() {
	h.initOnce.Do(func() {
		if h.Endpoint == "" {
			h.Endpoint = "http://127.0.0.1:4096"
		} else {
			h.Endpoint = strings.TrimRight(h.Endpoint, "/")
		}
		if h.HTTP == nil {
			h.HTTP = &http.Client{Timeout: requestTimeout}
		}
		if h.events == nil {
			h.events = make(chan core.HarnessEvent, 256)
		}
		if h.runs == nil {
			h.runs = make(map[string]core.RunID)
		}
		if h.ReconnectMin <= 0 {
			h.ReconnectMin = 100 * time.Millisecond
		}
		if h.ReconnectMax <= 0 {
			h.ReconnectMax = 2 * time.Second
		}
		if h.ExcerptLimit <= 0 {
			h.ExcerptLimit = 1200
		}
	})
}

// base returns the configured OpenCode endpoint after initialization.
func (h *Harness) base() string {
	h.init()
	return h.Endpoint
}

// firstNonEmpty returns the first non-empty string among a and b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// modelRef returns the model to use as a Model.Ref object. A model resolved at
// dispatch from the parent session wins over the startup fallback. The fallback
// is resolved once per harness because its inputs are fixed for the harness
// lifetime. OpenCode child sessions do not inherit the parent model, so the ref
// is sent both when the session is created and when it is configured. A bare id
// is not a ref.
func (h *Harness) modelRef(req core.SpawnRequest) core.ModelRef {
	if !req.ModelRef.IsZero() {
		return req.ModelRef
	}
	h.modelOnce.Do(func() {
		if !h.ModelRef.IsZero() {
			h.modelDefault = h.ModelRef
		} else if h.Model != "" {
			h.modelDefault = core.ModelRef{ProviderID: "opencode", ID: h.Model}
		}
	})
	return h.modelDefault
}

// messageID builds a unique prompt id with a prefix and random suffix.
func messageID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("msg_%s_%s_%d", prefix, hex.EncodeToString(b[:]), time.Now().UnixNano())
}

// sessionIDFromJSON extracts a session id from an OpenCode response body.
func sessionIDFromJSON(data []byte) string {
	var envelope struct {
		ID   string `json:"id"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ""
	}
	if envelope.ID != "" {
		return envelope.ID
	}
	return envelope.Data.ID
}

// ApplyAuth sets the Authorization header on a request. Tokens of the form
// user:pass use HTTP Basic; any other token uses a Bearer header. The harness
// module cannot import packages under internal, so this copy must stay in sync
// with shared.ApplyAuth.
func ApplyAuth(req *http.Request, token string) {
	if user, pass, ok := strings.Cut(token, ":"); ok {
		req.SetBasicAuth(user, pass)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// do sends an HTTP request to OpenCode and returns the body and status.
func (h *Harness) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	h.init()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	cctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, h.Endpoint+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		ApplyAuth(req, h.Token)
	}
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("opencode %s: status %d: %s", path, resp.StatusCode, redact.Redact(string(data)))
	}
	return data, resp.StatusCode, nil
}

// prompt posts a steering prompt to an OpenCode session. A 409 means this
// message id was already delivered, so a spawn retry treats it as success.
func (h *Harness) prompt(ctx context.Context, session, text, id string) error {
	return h.deliver(ctx, session, text, id, true)
}

// deliver posts a prompt and decides whether a 409 counts as delivery. Wake
// cannot accept a 409: the id belongs to an earlier turn and the nudge would
// never reach a live worker.
func (h *Harness) deliver(ctx context.Context, session, text, id string, acceptConflict bool) error {
	path := "/api/session/" + url.PathEscape(session) + "/prompt"
	body := map[string]any{
		"id":       id,
		"text":     redact.Redact(text),
		"delivery": "steer",
		"resume":   true,
	}
	_, status, err := h.do(ctx, http.MethodPost, path, body)
	if status == http.StatusConflict && acceptConflict {
		return nil
	}
	return err
}

// NotifyParent queues a quiet, redacted message in the parent session's inbox.
func (h *Harness) NotifyParent(ctx context.Context, sessionID, text string) error {
	if sessionID == "" {
		return nil
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/prompt"
	body := map[string]any{
		"id": messageID("notify"), "text": redact.Redact(text),
		"delivery": "queue", "resume": false,
	}
	_, _, err := h.do(ctx, http.MethodPost, path, body)
	return err
}

// bind records the run that owns a session.
func (h *Harness) bind(session string, run core.RunID) {
	if session == "" || run == "" {
		return
	}
	h.init()
	h.mu.Lock()
	h.runs[session] = run
	h.mu.Unlock()
}

// runFor returns the run bound to a session.
func (h *Harness) runFor(session string) (core.RunID, bool) {
	h.init()
	h.mu.RLock()
	run, ok := h.runs[session]
	h.mu.RUnlock()
	return run, ok
}

// unbind drops a session's run mapping so a long-lived daemon does not keep
// every session it ever spawned.
func (h *Harness) unbind(session string) {
	if session == "" {
		return
	}
	h.init()
	h.mu.Lock()
	delete(h.runs, session)
	h.mu.Unlock()
}

// Spawn creates and configures an OpenCode session, then sends the initial prompt.
func (h *Harness) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	h.init()
	parent := firstNonEmpty(req.ParentSessionID, h.ParentSessionID)
	title := firstNonEmpty(req.Task.Title, "Beacon "+string(req.RunID))
	body := map[string]any{
		"title":     title,
		"directory": req.Workspace.Path,
		"location":  map[string]any{"directory": req.Workspace.Path},
	}
	if parent != "" {
		body["parentID"] = parent
	}
	if agent := firstNonEmpty(strings.TrimSpace(req.Task.WorkerAgent), h.Agent); agent != "" {
		body["agent"] = agent
	}
	ref := h.modelRef(req)
	if !ref.IsZero() {
		body["model"] = wireModel(ref)
	}
	data, _, err := h.do(ctx, http.MethodPost, "/api/session", body)
	if err != nil {
		return core.Session{}, err
	}
	id := sessionIDFromJSON(data)
	if id == "" {
		return core.Session{}, fmt.Errorf("opencode: missing session id in %s", redact.Redact(string(data)))
	}
	h.bind(id, req.RunID)
	if err := h.applyModel(ctx, id, ref); err != nil {
		return core.Session{}, err
	}
	h.deferSessionSetup(ctx, id, req)
	if err := h.prompt(ctx, id, req.Prompt, messageID("spawn")); err != nil {
		return core.Session{}, err
	}
	return core.Session{ID: id, PR: core.PRFromText(string(data))}, nil
}

// Wake binds a wake context to a session and sends a limited wake prompt.
func (h *Harness) Wake(ctx context.Context, sessionID string, w core.WakeContext) error {
	h.init()
	h.bind(sessionID, w.RunID)
	return h.deliver(ctx, sessionID, core.WakeTextLimited(w, h.ExcerptLimit), messageID("wake"), false)
}

// Stop deletes an OpenCode session.
func (h *Harness) Stop(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	h.unbind(sessionID)
	_, status, err := h.do(ctx, http.MethodDelete, "/api/session/"+url.PathEscape(sessionID), nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// IsAlive reports whether an OpenCode session still has a worker. A cancelled
// session stays in storage and answers 200, so existence alone is not liveness.
func (h *Harness) IsAlive(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	data, status, err := h.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID), nil)
	if status == http.StatusNotFound {
		h.unbind(sessionID)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if sessionEnded(data) {
		h.unbind(sessionID)
		return false, nil
	}
	return true, nil
}

// sessionEnded reads the terminal outcome OpenCode records for a cancelled
// session. A busy session carries no such outcome.
func sessionEnded(data []byte) bool {
	var info struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return false
	}
	switch info.Outcome {
	case "interrupted", "cancelled", "canceled":
		return true
	}
	return false
}

// canonicalPath resolves symlinks in path, falling back to a clean path.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// reloadLocations tells the live editor to drop its cached worktree locations.
// OpenCode answers 503 while it is still removing a stale row and 499 when an
// editor closes the request early. Both are transient, so they are retried
// with a short bounded backoff. The last error is returned once the attempts
// run out; callers decide whether that error is fatal.
func (h *Harness) reloadLocations(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < locationReloadAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(locationReloadBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		_, status, err := h.do(ctx, http.MethodPost, "/api/location/reload", nil)
		if err == nil {
			return nil
		}
		lastErr = err
		if status != http.StatusServiceUnavailable && status != statusClientClosedRequest {
			return err
		}
	}
	return lastErr
}

// RefreshWorktrees removes stale OpenCode worktree rows for the configured
// repo, then tells the live editor to drop the locations it has cached.
func (h *Harness) RefreshWorktrees(ctx context.Context) error {
	if h.Repo == "" {
		return nil
	}
	data, _, err := h.do(ctx, http.MethodGet, "/api/project", nil)
	if err != nil {
		return err
	}
	var projects []struct {
		ID        string `json:"id"`
		Canonical string `json:"canonical"`
	}
	if err := json.Unmarshal(data, &projects); err != nil {
		return err
	}
	repo := canonicalPath(h.Repo)
	for _, p := range projects {
		if canonicalPath(p.Canonical) != repo {
			continue
		}
		_, status, err := h.do(ctx, http.MethodPost, "/api/worktree/refresh", map[string]string{"projectID": p.ID})
		if status == http.StatusBadRequest || status == http.StatusNotFound {
			slog.Warn("worktree refresh rejected", "status", status, "project", p.ID)
			return nil
		}
		if err != nil {
			slog.Warn("worktree refresh POST failed", "error", err, "project", p.ID)
			return err
		}
		if err := h.reloadLocations(ctx); err != nil {
			slog.Warn("worktree location reload failed", "error", err)
			return err
		}
		return nil
	}
	slog.Warn("worktree refresh: no project matches repo", "repo", h.Repo)
	return nil
}

// Events returns the channel that carries session idle events.
func (h *Harness) Events() <-chan core.HarnessEvent {
	h.init()
	return h.events
}

// Harness satisfies the core harness, worktree and idle ports.
var (
	_ core.AgentHarness      = (*Harness)(nil)
	_ core.WorktreeRefresher = (*Harness)(nil)
	_ core.IdleWatcher       = (*Harness)(nil)
)
