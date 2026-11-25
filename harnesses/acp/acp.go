// Package acp provides a minimal Agent Client Protocol harness for Beacon.
// It launches an ACP agent per worker session over stdio and speaks the
// baseline initialize, session/new, session/prompt and session/cancel methods.
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const defaultTimeout = 30 * time.Minute

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

// NewFromConfig is the only constructor. The composition root maps named
// config fields here instead of passing positional strings.
func NewFromConfig(cfg Config) *Harness {
	return &Harness{
		Command:         cfg.Command,
		Model:           cfg.Model,
		ParentSessionID: cfg.ParentSessionID,
		Timeout:         cfg.Timeout,
	}
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
		h.newProcess = startManagedProcess
	}
}

// managedProcess watches an agent subprocess and records when it exits. The
// waiter reaps the child, so signal 0 or ProcessState alone cannot be used to
// tell a dead agent from one that has not been waited on yet.
type managedProcess struct {
	process
	exited atomic.Bool
}

func startManagedProcess(command, cwd string) (process, error) {
	p, err := startProcess(command, cwd)
	if err != nil {
		return nil, err
	}
	m := &managedProcess{process: p}
	go func() {
		_ = p.reap()
		m.exited.Store(true)
	}()
	return m, nil
}

func (m *managedProcess) alive() bool { return !m.exited.Load() }

func (m *managedProcess) close() error {
	err := m.process.close()
	m.exited.Store(true)
	return err
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
	return core.Session{ID: id, PR: core.PRFromText(output)}, nil
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
	_, err := h.prompt(cctx, s, core.WakeText(w))
	return err
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

// IsAlive reports whether the agent behind a session is still running. A
// session whose process has exited is dropped so orphan reconciliation can
// redispatch the task to a fresh worker.
func (h *Harness) IsAlive(_ context.Context, sessionID string) (bool, error) {
	s, ok := h.session(sessionID)
	if !ok || s == nil || s.proc == nil {
		return false, nil
	}
	if p, ok := s.proc.(interface{ alive() bool }); ok && !p.alive() {
		h.mu.Lock()
		if h.sessions[sessionID] == s {
			delete(h.sessions, sessionID)
		}
		h.mu.Unlock()
		return false, nil
	}
	return true, nil
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

var _ core.AgentHarness = (*Harness)(nil)
