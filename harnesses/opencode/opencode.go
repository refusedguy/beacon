package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	Model           string
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

func New(endpoint string, rest ...string) *Harness {
	h := &Harness{Endpoint: strings.TrimRight(endpoint, "/"), HTTP: &http.Client{Timeout: requestTimeout}}
	if len(rest) > 0 {
		h.Model = rest[0]
	}
	if len(rest) > 1 {
		h.Agent = rest[1]
	}
	if len(rest) > 2 {
		h.Token = rest[2]
	}
	if len(rest) > 3 {
		h.ParentSessionID = rest[3]
	}
	return h
}

func NewOpenCode(endpoint, model, token string) *Harness {
	return New(endpoint, model, "", token)
}

func NewFromConfig(cfg Config) *Harness {
	h := New(cfg.Endpoint, cfg.Model, cfg.Agent, cfg.Token, cfg.ParentSessionID)
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

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}

func messageID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s-%d", prefix, hex.EncodeToString(b[:]), time.Now().UnixNano())
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

func wakeText(w core.WakeContext, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "correction run=%s pr=%d head=%s rebase=%d fix=%d\n", w.RunID, w.PRNumber, w.HeadSHA, w.RebaseTry, w.FixTry)
	fmt.Fprintf(&b, "reason: %s\n", w.Reason)
	if w.Excerpt != "" {
		fmt.Fprintf(&b, "excerpt: %s\n", truncate(w.Excerpt, limit))
	}
	b.WriteString("fix the issue, push, then call beacon_watch_pr again")
	return b.String()
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
		req.Header.Set("Authorization", "Bearer "+h.Token)
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
	parent := req.ParentSessionID
	if parent == "" {
		parent = h.ParentSessionID
	}
	title := req.Task.Title
	if title == "" {
		title = "Beacon " + string(req.RunID)
	}
	body := map[string]any{
		"parentID":  parent,
		"title":     title,
		"agent":     h.Agent,
		"directory": req.Workspace.Path,
		"location":  map[string]any{"directory": req.Workspace.Path},
	}
	if h.Model != "" {
		body["model"] = h.Model
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

func (h *Harness) Events() <-chan core.HarnessEvent {
	h.init()
	return h.events
}

var (
	_ core.AgentHarness = (*Harness)(nil)
	_ core.AgentHarness = (*OpenCode)(nil)
)
