package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// w10aMockRuntime is a WorkerRuntime stand-in. It never starts a process:
// Prompt publishes two updates into the buffered channel so the spawn command
// can be exercised end to end without any external runner binary.
type w10aMockRuntime struct {
	initErr   error
	newErr    error
	promptErr error
	sessionID string

	updates chan wake.WorkerUpdate
	done    chan struct{}

	mu           sync.Mutex
	initialized  bool
	cwd          string
	promptTarget string
	promptText   string
	closed       bool
}

func newW10aMockRuntime() *w10aMockRuntime {
	return &w10aMockRuntime{
		sessionID: "ses_w10a",
		updates:   make(chan wake.WorkerUpdate, 8),
		done:      make(chan struct{}),
	}
}

func (m *w10aMockRuntime) Initialize(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialized = true
	return m.initErr
}

func (m *w10aMockRuntime) NewSession(_ context.Context, cwd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cwd = cwd
	return m.sessionID, m.newErr
}

func (m *w10aMockRuntime) LoadSession(context.Context, string, string) error { return nil }

func (m *w10aMockRuntime) Prompt(_ context.Context, sessionID, text string) error {
	m.mu.Lock()
	m.promptTarget = sessionID
	m.promptText = text
	m.mu.Unlock()
	for _, word := range []string{"first", "second"} {
		m.updates <- wake.WorkerUpdate{
			SessionID: sessionID,
			Update: json.RawMessage(fmt.Sprintf(
				`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}`, word)),
		}
	}
	return m.promptErr
}

func (m *w10aMockRuntime) Cancel(context.Context, string) error { return nil }
func (m *w10aMockRuntime) Updates() <-chan wake.WorkerUpdate    { return m.updates }
func (m *w10aMockRuntime) Done() <-chan struct{}                { return m.done }

func (m *w10aMockRuntime) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

type w10aStats struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (s *w10aStats) SaveRuntimeStats(payload []byte, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, append([]byte(nil), payload...))
	return nil
}

func (s *w10aStats) snapshot() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.payloads...)
}

type w10aEvents struct {
	mu     sync.Mutex
	events []httpapi.Event
	notify chan struct{}
}

func (s *w10aEvents) Publish(e httpapi.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	if s.notify != nil {
		s.notify <- struct{}{}
	}
}

func (s *w10aEvents) snapshot() []httpapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]httpapi.Event(nil), s.events...)
}

// w10aInstallACP swaps the ACP ports for one test. cfgOut, when non-nil,
// receives the config passed to the factory so the command line can be
// asserted without reaching for a real runner.
func w10aInstallACP(t *testing.T, rt wake.WorkerRuntime, factoryErr error, stats spawnStatsStore, events spawnEventSink, ctx context.Context, cancel context.CancelFunc, cfgOut *wake.ACPRuntimeConfig) {
	t.Helper()
	prevFactory, prevLife := spawnACPRuntimeFactory, spawnACPLifetime
	prevStats, prevEvents := spawnRuntimeStats, spawnEvents
	spawnACPRuntimeFactory = func(cfg wake.ACPRuntimeConfig) (wake.WorkerRuntime, error) {
		if cfgOut != nil {
			*cfgOut = cfg
		}
		if factoryErr != nil {
			return nil, factoryErr
		}
		return rt, nil
	}
	spawnACPLifetime = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	spawnRuntimeStats, spawnEvents = stats, events
	t.Cleanup(func() {
		spawnACPRuntimeFactory = prevFactory
		spawnACPLifetime = prevLife
		spawnRuntimeStats, spawnEvents = prevStats, prevEvents
	})
}

func TestW10ASpawnACPStreamsUpdates(t *testing.T) {
	rt := newW10aMockRuntime()
	stats := &w10aStats{}
	events := &w10aEvents{notify: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cfg wake.ACPRuntimeConfig
	w10aInstallACP(t, rt, nil, stats, events, ctx, cancel, &cfg)

	// Stop the attached stream once both updates reached the SSE sink. This
	// stands in for the worker exiting and keeps the test free of sleeps.
	go func() {
		<-events.notify
		<-events.notify
		cancel()
	}()

	c, out, errOut := t3142CLI()
	code := c.runSpawn([]string{"T3.142", "--runtime", "acp", "--runner", "claude", "--title", "Card title"})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got := out.String(); got != "ses_w10a\n" {
		t.Errorf("stdout = %q, want session id line", got)
	}
	if !rt.initialized || rt.cwd == "" {
		t.Errorf("runtime not initialized with a cwd: initialized=%v cwd=%q", rt.initialized, rt.cwd)
	}
	if rt.promptTarget != "ses_w10a" {
		t.Errorf("prompt target = %q, want ses_w10a", rt.promptTarget)
	}
	if !strings.Contains(rt.promptText, "T3.142") {
		t.Errorf("prompt %q does not name the task", rt.promptText)
	}
	if cfg.Runtime != "claude" {
		t.Errorf("factory runtime = %q, want claude", cfg.Runtime)
	}
	if !rt.closed {
		t.Error("runtime Close was not called")
	}

	payloads := stats.snapshot()
	if len(payloads) != 2 {
		t.Fatalf("runtime_stats writes = %d, want 2", len(payloads))
	}
	var last acpSessionStats
	if err := json.Unmarshal(payloads[len(payloads)-1], &last); err != nil {
		t.Fatalf("decode runtime_stats: %v", err)
	}
	if last.TaskID != "T3.142" || last.SessionID != "ses_w10a" || last.Runner != "claude" || last.Title != "Card title" {
		t.Errorf("stats identity = %+v", last)
	}
	if len(last.Updates) != 2 {
		t.Errorf("stats updates = %d, want 2", len(last.Updates))
	}

	published := events.snapshot()
	if len(published) != 2 {
		t.Fatalf("SSE events = %d, want 2", len(published))
	}
	for _, e := range published {
		if e.Type != "session.update" || e.WatchID != "T3.142" || len(e.Payload) == 0 {
			t.Errorf("SSE event = %+v", e)
		}
	}
}

func TestW10ASpawnRejectsUnsupportedRuntime(t *testing.T) {
	c, _, errOut := t3142CLI()
	if code := c.runSpawn([]string{"T3.142", "--runtime", "codex"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "неподдерживаемый runtime") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestW10ASpawnACPFailsClosedWithoutFactory(t *testing.T) {
	prev := spawnACPRuntimeFactory
	spawnACPRuntimeFactory = nil
	t.Cleanup(func() { spawnACPRuntimeFactory = prev })
	c, _, errOut := t3142CLI()
	if code := c.runSpawn([]string{"T3.142", "--runtime", "acp"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "ACP-раннер не подключён") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestW10ASpawnACPFactoryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w10aInstallACP(t, nil, errors.New("bad runner"), &w10aStats{}, &w10aEvents{}, ctx, cancel, nil)
	c, _, errOut := t3142CLI()
	code := c.runSpawn([]string{"T3.142", "--runtime", "acp"})
	if code != 2 || !strings.Contains(errOut.String(), "не удалось запустить ACP-раннер") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
}

func TestW10ASpawnACPErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*w10aMockRuntime)
		stderr string
	}{
		{"init", func(m *w10aMockRuntime) { m.initErr = errors.New("no handshake") }, "ACP initialize"},
		{"empty session", func(m *w10aMockRuntime) { m.sessionID = "  " }, "пустой sessionID"},
		{"new session", func(m *w10aMockRuntime) { m.newErr = errors.New("no slot") }, "ACP session/new"},
		{"prompt", func(m *w10aMockRuntime) { m.promptErr = errors.New("no route") }, "промпт не отправлен"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newW10aMockRuntime()
			tc.mutate(rt)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w10aInstallACP(t, rt, nil, &w10aStats{}, &w10aEvents{}, ctx, cancel, nil)
			c, out, errOut := t3142CLI()
			if code := c.runSpawn([]string{"T3.142", "--runtime", "acp"}); code != 2 {
				t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.stderr) {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.stderr)
			}
			if out.Len() != 0 && tc.name != "prompt" {
				t.Errorf("stdout = %q, want no id on early failure", out.String())
			}
		})
	}
}

func TestW10ASpawnACPConfigEnvOverrides(t *testing.T) {
	t.Setenv(spawnACPRunnerEnv, "codex")
	t.Setenv(spawnACPCommandEnv, " /usr/local/bin/wrap ")
	t.Setenv(spawnACPArgsEnv, " acp --stdio ")
	cfg := spawnACPConfig("")
	if cfg.Runtime != "codex" || cfg.Command != "/usr/local/bin/wrap" {
		t.Fatalf("config = %+v", cfg)
	}
	wantArgs := []string{"acp", "--stdio"}
	if !reflectStringSlices(cfg.Args, wantArgs) {
		t.Errorf("args = %v, want %v", cfg.Args, wantArgs)
	}
	if got := spawnACPConfig("pi").Runtime; got != "pi" {
		t.Errorf("flag must win over env, got %q", got)
	}
}

func reflectStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
