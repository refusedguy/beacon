// Package acp provides a minimal Agent Client Protocol harness for Beacon.
// It launches an ACP agent per worker session over stdio and speaks the
// baseline initialize, session/new, session/prompt and session/cancel methods.
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const defaultTimeout = 30 * time.Minute

var prPattern = regexp.MustCompile(`(?:pull/|"pr"\s*:\s*)(\d+)`)

// Config is the explicit constructor input. Command is the ACP agent to
// launch, split on whitespace into an executable and arguments.
type Config struct {
	Command         string
	Model           string
	ParentSessionID string
	Timeout         time.Duration
}

// Harness implements core.AgentHarness by hosting one ACP agent process per
// session. It deliberately implements none of the optional ports: ACP has no
// parent channel, no worktree registry and no local model lookup.
type Harness struct {
	Command         string
	Model           string
	ParentSessionID string
	Timeout         time.Duration

	newProcess func(command, cwd string) (process, error)

	once     sync.Once
	mu       sync.Mutex
	sessions map[string]*acpSession
	events   chan core.HarnessEvent
}

type acpSession struct {
	id    string
	runID core.RunID
	proc  process
}

// New builds a harness. The variadic tail mirrors opencode.New for the
// composition root: model first, then parent session ID.
func New(command string, rest ...string) *Harness {
	h := &Harness{Command: command}
	if len(rest) > 0 {
		h.Model = rest[0]
	}
	if len(rest) > 1 {
		h.ParentSessionID = rest[1]
	}
	return h
}

func NewFromConfig(cfg Config) *Harness {
	h := New(cfg.Command, cfg.Model, cfg.ParentSessionID)
	h.Timeout = cfg.Timeout
	return h
}

func (h *Harness) init() {
	if h.Timeout <= 0 {
		h.Timeout = defaultTimeout
	}
	if h.events == nil {
		h.events = make(chan core.HarnessEvent, 64)
	}
	if h.sessions == nil {
		h.sessions = make(map[string]*acpSession)
	}
	if h.newProcess == nil {
		h.newProcess = func(command, cwd string) (process, error) {
			return startProcess(command, cwd)
		}
	}
}

func (h *Harness) open(ctx context.Context, cwd string) (process, error) {
	h.once.Do(h.init)
	if strings.TrimSpace(h.Command) == "" {
		return nil, fmt.Errorf("acp harness requires command")
	}
	p, err := h.newProcess(h.Command, cwd)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	var negotiated struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := p.call(cctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"clientInfo":      map[string]string{"name": "beacon", "version": "dev"},
	}, &negotiated, nil); err != nil {
		_ = p.close()
		return nil, err
	}
	if negotiated.ProtocolVersion != protocolVersion {
		_ = p.close()
		return nil, fmt.Errorf("acp: unsupported protocol version %d", negotiated.ProtocolVersion)
	}
	return p, nil
}

func (h *Harness) newSession(ctx context.Context, p process, cwd string) (string, error) {
	var res struct {
		SessionID string `json:"sessionId"`
	}
	err := p.call(ctx, "session/new", map[string]any{
		"cwd": cwd, "mcpServers": []any{},
	}, &res, nil)
	if err != nil {
		return "", err
	}
	if res.SessionID == "" {
		return "", fmt.Errorf("acp: missing session id")
	}
	return res.SessionID, nil
}

func (h *Harness) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	h.once.Do(h.init)
	cctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	p, err := h.open(cctx, req.Workspace.Path)
	if err != nil {
		return core.Session{}, err
	}
	id, err := h.newSession(cctx, p, req.Workspace.Path)
	if err != nil {
		_ = p.close()
		return core.Session{}, err
	}
	s := &acpSession{id: id, runID: req.RunID, proc: p}
	h.mu.Lock()
	h.sessions[id] = s
	h.mu.Unlock()

	output, err := h.prompt(cctx, s, req.Prompt)
	if err != nil {
		_ = h.Stop(ctx, id)
		return core.Session{}, err
	}
	return core.Session{ID: id, PR: prFromText(output)}, nil
}

// prompt runs one ACP turn and reports idle when the agent stops. It returns
// the agent's text so the caller can pick up a PR number the agent printed.
func (h *Harness) prompt(ctx context.Context, s *acpSession, text string) (string, error) {
	var res struct {
		StopReason string `json:"stopReason"`
	}
	var written strings.Builder
	onNote := func(raw json.RawMessage) {
		if chunk := collectText(raw); chunk != "" {
			written.WriteString(chunk)
		}
	}
	err := s.proc.call(ctx, "session/prompt", map[string]any{
		"sessionId": s.id,
		"prompt":    []any{map[string]string{"type": "text", "text": text}},
	}, &res, onNote)
	if err != nil {
		return "", err
	}
	h.emit(s.runID, s.id)
	return written.String(), nil
}

// collectText validates an ACP text chunk. It is kept separate from prompt so
// the protocol decode stays easy to read.
func collectText(raw json.RawMessage) string {
	var note struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &note) != nil {
		return ""
	}
	if note.Method != "session/update" || note.Params.Update.Kind != "agent_message_chunk" {
		return ""
	}
	if note.Params.Update.Content.Type != "text" {
		return ""
	}
	return note.Params.Update.Content.Text
}

func (h *Harness) session(id string) (*acpSession, bool) {
	h.once.Do(h.init)
	h.mu.Lock()
	s, ok := h.sessions[id]
	h.mu.Unlock()
	return s, ok
}

func (h *Harness) Wake(ctx context.Context, sessionID string, w core.WakeContext) error {
	s, ok := h.session(sessionID)
	if !ok {
		return fmt.Errorf("acp: unknown session %s", sessionID)
	}
	cctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	_, err := h.prompt(cctx, s, wakeText(w))
	return err
}

func wakeText(w core.WakeContext) string {
	return fmt.Sprintf("Beacon correction for run %s, PR #%d, head %s.\nReason: %s\n%s\nFix the issue, push and open a PR.\n",
		w.RunID, w.PRNumber, w.HeadSHA, w.Reason, w.Excerpt)
}

func (h *Harness) Stop(ctx context.Context, sessionID string) error {
	h.once.Do(h.init)
	h.mu.Lock()
	s := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if s == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = s.proc.notify(cctx, "session/cancel", map[string]any{"sessionId": s.id})
	return s.proc.close()
}

func (h *Harness) IsAlive(_ context.Context, sessionID string) (bool, error) {
	s, _ := h.session(sessionID)
	return s != nil, nil
}

// NotifyParent is a no-op. ACP workers are independent sessions with no
// orchestrator inbox to write into.
func (h *Harness) NotifyParent(context.Context, string, string) error { return nil }

func (h *Harness) Events() <-chan core.HarnessEvent {
	h.once.Do(h.init)
	return h.events
}

func (h *Harness) emit(run core.RunID, sessionID string) {
	h.once.Do(h.init)
	select {
	case h.events <- core.HarnessEvent{RunID: run, SessionID: sessionID, Kind: "idle"}:
	default:
	}
}

func prFromText(s string) int {
	m := prPattern.FindStringSubmatch(s)
	if len(m) != 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

var _ core.AgentHarness = (*Harness)(nil)
