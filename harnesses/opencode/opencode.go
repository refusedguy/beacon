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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const requestTimeout = 30 * time.Second

var (
	prPattern      = regexp.MustCompile(`(?:pull/|"pr"\s*:\s*)(\d+)`)
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`AIza[A-Za-z0-9_-]{30,}`),
		regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"']+`),
		regexp.MustCompile(`(?i)((?:token|secret|password|api[_-]?key)\s*[:=]\s*)"[^"]+"`),
		regexp.MustCompile(`(?i)((?:token|secret|password|api[_-]?key)\s*[:=]\s*)[^\s,;}]+`),
	}
)

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
	events    chan core.HarnessEvent
	mu        sync.RWMutex
	runs      map[string]core.RunID
}

type OpenCode = Harness

type Config struct {
	Endpoint        string
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

func (h *Harness) base() string {
	h.init()
	return h.Endpoint
}

func redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if i := strings.IndexAny(m, ":="); i >= 0 {
				return m[:i+1] + " [REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// modelRef returns the model to use as a Model.Ref object. A model resolved at
// dispatch from the parent session wins over the startup fallback. OpenCode
// child sessions do not inherit the parent model, so the ref is sent both when
// the session is created and when it is configured. A bare id is not a ref.
func (h *Harness) modelRef(req core.SpawnRequest) core.ModelRef {
	if !req.ModelRef.IsZero() {
		return req.ModelRef
	}
	if !h.ModelRef.IsZero() {
		return h.ModelRef
	}
	if h.Model != "" {
		return core.ModelRef{ProviderID: "opencode", ID: h.Model}
	}
	return core.ModelRef{}
}

func messageID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("msg_%s_%s_%d", prefix, hex.EncodeToString(b[:]), time.Now().UnixNano())
}

func prFromText(s string) int {
	m := prPattern.FindStringSubmatch(s)
	if len(m) != 2 {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil {
		return 0
	}
	return n
}

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

// ApplyAuth sets the Authorization header for an OpenCode request. Tokens of
// the form user:pass use HTTP Basic; any other token uses a Bearer header.
func ApplyAuth(req *http.Request, token string) {
	if user, pass, ok := strings.Cut(token, ":"); ok {
		req.SetBasicAuth(user, pass)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

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
		return data, resp.StatusCode, fmt.Errorf("opencode %s: status %d: %s", path, resp.StatusCode, redact(string(data)))
	}
	return data, resp.StatusCode, nil
}

func (h *Harness) prompt(ctx context.Context, session, text, id string) error {
	path := "/api/session/" + url.PathEscape(session) + "/prompt"
	body := map[string]any{
		"id":       id,
		"text":     redact(text),
		"delivery": "steer",
		"resume":   true,
	}
	_, status, err := h.do(ctx, http.MethodPost, path, body)
	if status == http.StatusConflict {
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
		"id": messageID("notify"), "text": redact(text),
		"delivery": "queue", "resume": false,
	}
	_, _, err := h.do(ctx, http.MethodPost, path, body)
	return err
}

func (h *Harness) bind(session string, run core.RunID) {
	if session == "" || run == "" {
		return
	}
	h.init()
	h.mu.Lock()
	h.runs[session] = run
	h.mu.Unlock()
}

func (h *Harness) runFor(session string) (core.RunID, bool) {
	h.init()
	h.mu.RLock()
	run, ok := h.runs[session]
	h.mu.RUnlock()
	return run, ok
}

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
	if ref := h.modelRef(req); !ref.IsZero() {
		body["model"] = ref
	}
	data, _, err := h.do(ctx, http.MethodPost, "/api/session", body)
	if err != nil {
		return core.Session{}, err
	}
	id := sessionIDFromJSON(data)
	if id == "" {
		return core.Session{}, fmt.Errorf("opencode: missing session id in %s", redact(string(data)))
	}
	h.bind(id, req.RunID)
	if err := h.configureSession(ctx, id, req); err != nil {
		return core.Session{}, err
	}
	if err := h.prompt(ctx, id, req.Prompt, messageID("spawn")); err != nil {
		return core.Session{}, err
	}
	return core.Session{ID: id, PR: prFromText(string(data))}, nil
}

func (h *Harness) Wake(ctx context.Context, sessionID string, w core.WakeContext) error {
	h.init()
	h.bind(sessionID, w.RunID)
	return h.prompt(ctx, sessionID, wakeText(w, h.ExcerptLimit), messageID("wake"))
}

func (h *Harness) Stop(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	_, status, err := h.do(ctx, http.MethodDelete, "/api/session/"+url.PathEscape(sessionID), nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (h *Harness) IsAlive(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	_, status, err := h.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID), nil)
	if status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
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
		if _, _, err := h.do(ctx, http.MethodPost, "/api/location/reload", nil); err != nil {
			slog.Warn("worktree location reload failed", "error", err)
		}
		return nil
	}
	slog.Warn("worktree refresh: no project matches repo", "repo", h.Repo)
	return nil
}

func (h *Harness) Events() <-chan core.HarnessEvent {
	h.init()
	return h.events
}

var (
	_ core.AgentHarness      = (*Harness)(nil)
	_ core.AgentHarness      = (*OpenCode)(nil)
	_ core.WorktreeRefresher = (*Harness)(nil)
)
