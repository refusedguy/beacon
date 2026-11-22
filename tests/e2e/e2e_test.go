//go:build e2e

// Package e2e drives real processes only. There is no fake host, fake session
// or in-memory MCP transport here. Facts come from three tools:
//
//  1. the official go-sdk mcp client with CommandTransport (server subprocess);
//  2. the `opencode api` CLI against a live `opencode serve` v2.3.0;
//  3. files written by a plugin that the live server actually loaded.
//
// Run: bash tests/e2e/run.sh
//
// Honesty notes (also in docs/e2e/README.md):
//  * The positive wake check loads .opencode/plugins/beacon-bridge.js from this
//    repository. The T0.3 spike copy in fixtures is no longer a source of wake
//    confirmation; the stub fixture stays as the negative control.
//  * mcp_go_sdk_CommandTransport proves the protocol and the eight-tool
//    contract only. live_opencode_full_mcp_cycle calls the real handlers of
//    `beacon mcp` and reads watchID, revision and verdict from their answers.
//  * The full cycle has no hardcoded verdict driver. cycleDaemon observes CI
//    facts, runs the real domain engine with the ShipFacts gate (ADR 0061 item
//    2) and commits the verdict, obligation and wake intent. runLiveMCPCycle
//    never calls domain.Transition; only the daemon does. The verdict read back
//    comes from the real wait_result/get_result handlers over CommandTransport,
//    never from a value invented in the test driver.
//  * Negative checks install no-op handlers and require the positive
//    verification to fail. A harness that stays green with a stub is not e2e.

package e2e_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

var (
	goBin    = envOr("GO_BIN", "/tmp/opencode/go126/bin/go")
	openBin  = envOr("OPENCODE_BIN", "opencode")
	repoRoot = repoRootPath()
	// expectedSet comes from the MCP package so the tools/list contract has
	// one owner. A new tool registration without a matching name breaks both
	// assertions below.
	expectedSet = beaconmcp.AdvertisedToolNames()
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func repoRootPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "../.."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func tmpRoot() string {
	if st, err := os.Stat("/tmp/opencode"); err == nil && st.IsDir() {
		return "/tmp/opencode"
	}
	return os.TempDir()
}

func newBase(t *testing.T, tag string) string {
	t.Helper()
	base, err := os.MkdirTemp(tmpRoot(), "beacon-e2e-"+tag+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("base: %s", base)
	return base
}

func resolveMcpSource(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("BEACON_MCP_SRC"),
		repoRoot,
		filepath.Join(filepath.Dir(repoRoot), "w43"),
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "cmd", "beacon", "mcp.go")); err == nil {
			return dir
		}
	}
	t.Fatal("beacon mcp недоступен: нет cmd/beacon/mcp.go ни в корне репозитория, " +
		"ни по BEACON_MCP_SRC. Укажи BEACON_MCP_SRC=<checkout с mcp.go>")
	return ""
}

func buildBeacon(t *testing.T, src, out string) {
	t.Helper()
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go" // fallback to PATH
	}
	cmd := exec.Command(goBin, "build", "-o", out, "./cmd/beacon")
	cmd.Dir = src
	cmd.Env = os.Environ()
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build из %s: %v\n%s", src, err, combined)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

// --- 1. MCP client: official go-sdk with CommandTransport -------------------

func testMCPWithGoSDK(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.Command(binary, "mcp")
	transport := &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-e2e", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, &mcpsdk.ClientSessionOptions{
		ProtocolVersion: "2025-06-18", // one of beacon's LegacyProtocolVersions
	})
	if err != nil {
		t.Fatalf("CommandTransport connect: %v", err)
	}
	defer session.Close()

	init := session.InitializeResult()
	if init == nil || init.ServerInfo == nil {
		t.Fatal("initialize не вернул serverInfo")
	}
	if init.ServerInfo.Name != "beacon" {
		t.Fatalf("serverInfo.name = %q, ожидалось beacon", init.ServerInfo.Name)
	}
	if !strings.Contains(init.Instructions, "Beacon cycle protocol") {
		t.Fatalf("в initialize нет cycle instructions: %q", init.Instructions)
	}
	t.Logf("initialize: %s %s, protocol %s", init.ServerInfo.Name, init.ServerInfo.Version, init.ProtocolVersion)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Clone(expectedSet)
	slices.Sort(want)
	if len(names) != len(expectedSet) {
		t.Fatalf("tools/list вернул %d инструментов: %v", len(names), names)
	}
	if !slices.Equal(names, want) {
		t.Fatalf("tools/list: получено %v, ожидалось %v", names, want)
	}
	// PROTO-01: expectedSet comes from AdvertisedToolNames, so the eleventh
	// name is part of the shared catalog rather than a local E2E copy. Pin it
	// here to catch a future switch back to a ten-name literal.
	if !slices.Contains(expectedSet, beaconmcp.WakeToolName) {
		t.Fatalf("общий каталог не содержит %s: %v", beaconmcp.WakeToolName, expectedSet)
	}
	t.Logf("tools/list: ровно %d инструментов: %v", len(names), names)

	// tools/call goes through the same CommandTransport session. The tool is a
	// read-only stub in main; we only assert the protocol round trip here.
	call, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "beacon_list_running",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_list_running: %v", err)
	}
	if call.IsError {
		t.Fatal("beacon_list_running вернул isError=true")
	}
	t.Log("tools/call beacon_list_running: ответ получен, isError=false")
}

// Inspector is deliberately optional: in this environment npx produced no
// output for 60s. Enable with BEACON_E2E_INSPECTOR=1 when npx works.
func testInspectorOptional(t *testing.T, binary string) {
	if os.Getenv("BEACON_E2E_INSPECTOR") == "" {
		t.Skip("Inspector опционален: BEACON_E2E_INSPECTOR=1 включает проверку")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := []string{
		"--yes", "@modelcontextprotocol/inspector@2.5.0", "--cli", binary,
		"--method", "initialize", "--format", "json",
	}
	cmd := exec.CommandContext(ctx, "npx", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("Inspector недоступен в этой среде: %v; вывод: %.300s", err, out)
	}
	var parsed struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("Inspector вернул не JSON: %.300s", out)
	}
	if parsed.Result.ServerInfo.Name != "beacon" {
		t.Fatalf("Inspector: serverInfo.name=%q", parsed.Result.ServerInfo.Name)
	}
	t.Log("MCP Inspector 2.5.0: initialize → serverInfo.name=beacon")
}

// --- 2. opencode api CLI against a live server ------------------------------

type server struct {
	url      string
	password string
	proc     *exec.Cmd
	logPath  string
	base     string
	project  string
}

type apiResp struct {
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Data json.RawMessage `json:"data"`
}

type pluginInfo struct {
	ID     string `json:"id"`
	Source struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
	Features struct {
		Server bool `json:"server"`
	} `json:"features"`
	State struct {
		Status string `json:"status"`
	} `json:"state"`
}

type apiSession struct {
	ID string `json:"id"`
}

// serviceConnection is the registration record opencode writes to
// $XDG_STATE_HOME/opencode/service.json when started with --service. The
// record appears only once the HTTP API is listening and carries the real
// address and password, so it is a reliable readiness source: no log scraping
// and no guessed port.
type serviceConnection struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}

// waitServiceConnection waits for opencode's registration file and returns the
// address and password written there. The short ticker only bounds how long we
// wait for a small file written by the server itself; it never polls a log or
// sleeps a fixed 200 ms per iteration. The context deadline keeps a hung
// opencode from stalling CI.
func waitServiceConnection(t *testing.T, srv *server, path string) serviceConnection {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			var conn serviceConnection
			if json.Unmarshal(data, &conn) == nil && conn.URL != "" && conn.Password != "" {
				return conn
			}
		}
		select {
		case <-ctx.Done():
			stopServer(srv)
			t.Fatalf("opencode service не зарегистрировался за 30s: %s", tail(t, srv.logPath))
		case <-ticker.C:
		}
	}
}

func startServer(t *testing.T, base, pluginPath string) *server {
	t.Helper()
	project := filepath.Join(base, "project")
	mustMkdir(t, filepath.Join(project, ".opencode", "plugins"))
	mustMkdir(t, filepath.Join(base, "xdg-config"))
	mustMkdir(t, filepath.Join(base, "xdg-data", "opencode"))
	mustMkdir(t, filepath.Join(base, "xdg-cache"))
	mustMkdir(t, filepath.Join(base, "state", "beacon", "receipts"))

	copyFile(t, filepath.Join(repoRoot, "tests", "e2e", "fixtures", "opencode.json"),
		filepath.Join(project, "opencode.json"))
	if pluginPath != "" {
		copyFile(t, pluginPath, filepath.Join(project, ".opencode", "plugins", "beacon-bridge.js"))
	}
	// The spike project lived inside a git checkout; opencode resolves a
	// project location from git, so an isolated temp project needs its own.
	git := exec.Command("git", "init", "-q", ".")
	git.Dir = project
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	// A provider key is needed for session.prompt. Copy the real auth file
	// without ever logging its contents or adding it to the repository.
	realAuth := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode", "auth.json")
	if data, err := os.ReadFile(realAuth); err == nil {
		if err := os.WriteFile(filepath.Join(base, "xdg-data", "opencode", "auth.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	stateHome := filepath.Join(base, "state")
	logPath := filepath.Join(base, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		// --service chdirs the server to HOME. Keep the temp project there so
		// plugin discovery and session location stay scoped to this test.
		"HOME="+project,
		"XDG_CONFIG_HOME="+filepath.Join(base, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(base, "xdg-data"),
		"XDG_CACHE_HOME="+filepath.Join(base, "xdg-cache"),
		"XDG_STATE_HOME="+stateHome,
		// The repository plugin resolves its state dir from ctx.location unless
		// BEACON_STATE_DIR pins it. Pin the harness dir so its facts land where
		// collectFacts reads them, the same dir the fixture plugin uses.
		"BEACON_STATE_DIR="+filepath.Join(stateHome, "beacon"),
		"OPENCODE_CONFIG="+filepath.Join(project, "opencode.json"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
	)
	// --service makes opencode publish its live address and password in
	// $XDG_STATE_HOME/opencode/service.json. Port 0 lets opencode choose the
	// port and report the one it actually bound, so there is no close/rebind
	// window between reserving a port and starting the server.
	proc := exec.Command(openBin, "serve", "--hostname", "127.0.0.1", "--port", "0", "--service")
	proc.Dir = project
	proc.Env = env
	proc.Stdout = logFile
	proc.Stderr = logFile
	if err := proc.Start(); err != nil {
		t.Fatalf("opencode serve: %v", err)
	}
	srv := &server{
		proc:    proc,
		logPath: logPath,
		base:    base,
		project: project,
	}

	conn := waitServiceConnection(t, srv, filepath.Join(stateHome, "opencode", "service.json"))
	srv.url = conn.URL
	srv.password = conn.Password
	return srv
}

func stopServer(srv *server) {
	if srv != nil && srv.proc != nil && srv.proc.Process != nil {
		_ = srv.proc.Process.Kill()
		_ = srv.proc.Wait()
	}
}

func (srv *server) api(t *testing.T, method, path, data string) apiResp {
	t.Helper()
	args := []string{"api", "--server", srv.url}
	if data != "" {
		args = append(args, "-d", data)
	}
	args = append(args, method, path)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, openBin, args...)
	cmd.Env = append(os.Environ(), "OPENCODE_PASSWORD="+srv.password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode api %s %s: %v\n%s", method, path, err, out)
	}
	var resp apiResp
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("opencode api %s %s: не JSON: %s", method, path, out)
	}
	return resp
}

// apiAttempt runs the same `opencode api` CLI as api, but reports a non-zero
// exit instead of failing the test. Negative controls need the CLI error body
// as a fact: a stubbed or overly permissive API would answer 200 here.
func (srv *server) apiAttempt(t *testing.T, method, path, data string) (apiResp, string, error) {
	t.Helper()
	args := []string{"api", "--server", srv.url}
	if data != "" {
		args = append(args, "-d", data)
	}
	args = append(args, method, path)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, openBin, args...)
	cmd.Env = append(os.Environ(), "OPENCODE_PASSWORD="+srv.password)
	out, err := cmd.CombinedOutput()
	var resp apiResp
	if json.Unmarshal(out, &resp) != nil {
		// Error bodies are not apiResp JSON (they carry a _tag and an HTTP
		// status line). An empty Data is the fact that no wake was created.
		resp = apiResp{}
	}
	return resp, string(out), err
}

type sseCollector struct {
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	mu        sync.Mutex
	raw       []string
	sessionID string
}

func (s *sseCollector) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.raw {
		if strings.Contains(line, "session.execution.started") && strings.Contains(line, s.sessionID) {
			return true
		}
	}
	return false
}

func (s *sseCollector) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.raw)
}

func startSSE(t *testing.T, srv *server, sessionID string) *sseCollector {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, openBin, "api", "--server", srv.url, "GET", "/api/event")
	cmd.Env = append(os.Environ(), "OPENCODE_PASSWORD="+srv.password)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &sseCollector{cmd: cmd, cancel: cancel, sessionID: sessionID}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			c.mu.Lock()
			c.raw = append(c.raw, line)
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *sseCollector) stop() {
	if c == nil {
		return
	}
	c.cancel()
	_ = c.cmd.Wait()
}

// --- 3. Wake facts, read from files written by the loaded plugin ------------

type wakeFacts struct {
	PromptOK    bool
	ExecStarted bool
	Receipt     bool
	Posted      bool
	SSEStarted  bool
}

// verifyWake uses facts that the mandated tools can actually deliver.
// opencode api CLI does not stream the SSE body of /api/event in v2.3.0
// (it returns zero bytes), so SSEStarted is reported separately and never
// faked: a raw event stream is not available through the permitted tool.
func verifyWake(f wakeFacts) error {
	var missing []string
	if !f.PromptOK {
		missing = append(missing, "prompt.ok delivery=steer")
	}
	if !f.ExecStarted {
		missing = append(missing, "ctx-event session.execution.started")
	}
	if !f.Receipt {
		missing = append(missing, "receipts/<id>.wrote")
	}
	if !f.Posted {
		missing = append(missing, "receipts.jsonl state=posted")
	}
	if len(missing) != 0 {
		return fmt.Errorf("нет доказательств: %s", strings.Join(missing, ", "))
	}
	return nil
}

type jsonLine struct {
	Kind      string `json:"kind"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	Delivery  string `json:"delivery"`
	SessionID string `json:"sessionID"`
	State     string `json:"state"`
}

func readJSONLines(path string) []jsonLine {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []jsonLine
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var item jsonLine
		if err := json.Unmarshal([]byte(line), &item); err == nil {
			out = append(out, item)
		}
	}
	return out
}

func collectFacts(stateDir, id, sessionID string, sse *sseCollector) wakeFacts {
	facts := wakeFacts{
		Receipt:    fileExists(filepath.Join(stateDir, "receipts", id+".wrote")),
		SSEStarted: sse.started(),
	}
	for _, item := range readJSONLines(filepath.Join(stateDir, "plugin-events.jsonl")) {
		if item.Kind == "prompt-ok" && item.ID == id && item.Delivery == "steer" {
			facts.PromptOK = true
		}
		if item.Kind == "ctx-event" && item.Type == "session.execution.started" && item.SessionID == sessionID {
			facts.ExecStarted = true
		}
	}
	for _, item := range readJSONLines(filepath.Join(stateDir, "receipts.jsonl")) {
		if item.ID == id && item.State == "posted" {
			facts.Posted = true
		}
	}
	return facts
}

func waitFacts(t *testing.T, stateDir, id, sessionID string, sse *sseCollector, timeout time.Duration) wakeFacts {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		facts := collectFacts(stateDir, id, sessionID, sse)
		if verifyWake(facts) == nil || time.Now().After(deadline) {
			return facts
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// newSession creates a session on a live server and returns its real id.
func newSession(t *testing.T, srv *server) apiSession {
	t.Helper()
	resp := srv.api(t, "POST", "/api/session", "{}")
	var session apiSession
	if err := json.Unmarshal(resp.Data, &session); err != nil || session.ID == "" {
		t.Fatalf("POST /api/session: %s (%v)", resp.Data, err)
	}
	t.Logf("сессия: %s", session.ID)
	return session
}

// apiMessage is the part of Session.Message.Info that the live wake check
// reads: type, text, id and creation time. The schema is a union, so unknown
// fields are ignored on purpose.
type apiMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Finish string `json:"finish"`
}

// waitWakeMessages polls the real /api/session/{id}/message endpoint until the
// session both received the wake (a user message with the exact token) and
// continued the turn (an assistant message created at or after that user
// message). It never invents either fact: both must come from the API.
func waitWakeMessages(t *testing.T, srv *server, sessionID, token string, timeout time.Duration) (apiMessage, apiMessage, []apiMessage) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var seen []apiMessage
	var user, assistant apiMessage
	for {
		resp := srv.api(t, "GET", "/api/session/"+sessionID+"/message", "")
		// srv.api already unwrapped the envelope's `data` field, so resp.Data
		// is the array of Session.Message.Info values itself.
		var list []apiMessage
		if err := json.Unmarshal(resp.Data, &list); err != nil {
			t.Fatalf("GET /api/session/%s/message: не JSON: %s (%v)", sessionID, resp.Data, err)
		}
		seen = list
		user, assistant = apiMessage{}, apiMessage{}
		for _, m := range list {
			if m.Type == "user" && strings.Contains(m.Text, token) {
				user = m
				break
			}
		}
		if user.ID != "" {
			for _, m := range list {
				if m.Type == "assistant" && m.Time.Created >= user.Time.Created {
					assistant = m
					break
				}
			}
		}
		if user.ID != "" && assistant.ID != "" {
			return user, assistant, seen
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	summary := make([]string, 0, len(seen))
	for _, m := range seen {
		summary = append(summary, fmt.Sprintf("%s:%s", m.Type, m.ID))
	}
	t.Fatalf("сессия %s не подтвердила wake за %s: user(token=%q)=%v assistant=%v сообщения=%v",
		sessionID, timeout, token, user.ID != "", assistant.ID != "", summary)
	return apiMessage{}, apiMessage{}, seen
}

// waitPluginActive polls /api/plugin until the real server reports the loaded
// plugin. The plugin is loaded lazily, so a session must exist first.
func waitPluginActive(t *testing.T, srv *server) pluginInfo {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		listResp := srv.api(t, "GET", "/api/plugin", "")
		var plugins []pluginInfo
		if err := json.Unmarshal(listResp.Data, &plugins); err != nil {
			t.Fatalf("GET /api/plugin: %s (%v)", listResp.Data, err)
		}
		for _, p := range plugins {
			if p.ID == "beacon-bridge" {
				t.Logf("/api/plugin beacon-bridge: source=%q path=%q status=%q server=%v",
					p.Source.Type, p.Source.Path, p.State.Status, p.Features.Server)
				if p.Source.Type == "local" && p.State.Status == "active" && p.Features.Server {
					return p
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("плагин beacon-bridge не появился активным в /api/plugin за 20s")
	return pluginInfo{}
}

// postWakePointer writes one event to events.jsonl. The loaded plugin polls
// that file; this is a driver of an external event, not a fake handler.
func postWakePointer(t *testing.T, base, tag, sessionID, text string) (string, string) {
	t.Helper()
	stateDir := filepath.Join(base, "state", "beacon")
	id := "beacon-e2e-" + tag + "-" + fmt.Sprint(time.Now().UnixNano())
	pointer := fmt.Sprintf(`{"id":%q,"sessionID":%q,"text":%q}`+"\n", id, sessionID, text)
	f, err := os.OpenFile(filepath.Join(stateDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(pointer); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return id, fmt.Sprintf("id=%s session=%s", id, sessionID)
}

// runWakeScenario starts opencode with the given plugin, writes one pointer to
// events.jsonl and returns the facts that external observers recorded.
func runWakeScenario(t *testing.T, base, pluginPath, tag string) (wakeFacts, string) {
	return runWakeScenarioWithTimeout(t, base, pluginPath, tag, 15*time.Second)
}

func runWakeScenarioWithTimeout(t *testing.T, base, pluginPath, tag string, timeout time.Duration) (wakeFacts, string) {
	t.Helper()
	srv := startServer(t, base, pluginPath)
	defer stopServer(srv)

	session := newSession(t, srv)
	waitPluginActive(t, srv)

	sse := startSSE(t, srv, session.ID)
	defer sse.stop()

	stateDir := filepath.Join(base, "state", "beacon")
	id, info := postWakePointer(t, base, tag, session.ID, "beacon-e2e wake")
	facts := waitFacts(t, stateDir, id, session.ID, sse, timeout)
	return facts, fmt.Sprintf("%s sse_lines=%d", info, sse.count())
}

// --- helpers ----------------------------------------------------------------

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func tail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

// --- frozen crash-test scenario set (ADR 0042 item 5, T3.89) -----------------
//
// The set of crash-test scenarios is hashed before any scenario code is run.
// The hash below was recorded when this file was first written. At every run
// the engine recomputes it from the registered scenarios and refuses to start
// if the set changed. A scenario cannot be silently dropped, renamed or added
// to make the suite greener: changing the set requires a reviewed update of
// frozenScenarioHash. This is a rule about the stability of the scenario set,
// not the "tests exist in every card" rule from ADR 0010.

// frozenScenarioHash is sha256 of the sorted scenario names joined by "\n",
// with no trailing newline. Revised in BCX-55: the wake scenario now loads the
// repository plugin and is named opencode_wake_with_repo_plugin instead of the
// fixture-backed opencode_wake_with_reference_plugin. Revised when
// verdict_ready_gate_requires_shipfacts entered the set: the ready verdict must
// come from observed ShipFacts, not a hardcoded driver.
const frozenScenarioHash = "b8d1fef39b3cb10f066c24f94c00cf5dd2ccbfa2119814184aa0de75670d5a40"

type e2eScenario struct {
	name string
	run  func(t *testing.T, binary string)
}

// e2eScenarios is the single source of truth for both the scenarios that run
// and the frozen hash. Adding, removing or renaming an entry changes the hash.
var e2eScenarios = []e2eScenario{
	{"repo_plugin_skeleton", runRepoPluginSkeleton},
	{"mcp_go_sdk_CommandTransport", testMCPWithGoSDK},
	{"mcp_inspector_optional", testInspectorOptional},
	{"opencode_wake_with_repo_plugin", runWakePositive},
	{"live_wake_via_opencode_api", runLiveWakeViaAPI},
	{"live_opencode_full_mcp_cycle", runLiveMCPCycle},
	{"verdict_ready_gate_requires_shipfacts", runVerdictReadyGateRequiresShipFacts},
	{"negative_control_stub_handler_must_fail", runNegativeControl},
}

func scenarioSetHash(names []string) string {
	names = slices.Clone(names)
	slices.Sort(names)
	h := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return hex.EncodeToString(h[:])
}

func registeredScenarioNames() []string {
	names := make([]string, 0, len(e2eScenarios))
	for _, s := range e2eScenarios {
		names = append(names, s.name)
	}
	return names
}

// verifyFrozenScenarios is the engine gate. It returns an error when the live
// scenario set no longer matches the hash fixed before the code was written.
func verifyFrozenScenarios() error {
	names := registeredScenarioNames()
	if len(names) != len(slices.Compact(sorted(names))) {
		return fmt.Errorf("дубликаты в наборе сценариев: %v", names)
	}
	got := scenarioSetHash(names)
	if got != frozenScenarioHash {
		return fmt.Errorf("набор crash-сценариев изменён без пересмотра решения: "+
			"замороженный хеш=%s текущий хеш=%s сценарии=%v; "+
			"пересмотри решение и явно обнови frozenScenarioHash в tests/e2e/e2e_test.go",
			frozenScenarioHash, got, names)
	}
	return nil
}

func sorted(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return out
}

// TestFrozenCrashTestScenarios runs in the e2e CI job without needing opencode
// or a provider token. It is the check that the frozen set is intact.
func TestFrozenCrashTestScenarios(t *testing.T) {
	names := registeredScenarioNames()
	t.Logf("замороженных сценариев: %d; хеш: %s; сценарии: %v",
		len(names), scenarioSetHash(names), sorted(names))
	if err := verifyFrozenScenarios(); err != nil {
		t.Fatal(err)
	}
}

// --- scenario bodies ---------------------------------------------------------

func runRepoPluginSkeleton(t *testing.T, _ string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(repoRoot, ".opencode", "plugins", "beacon-bridge.js"))
	if err != nil {
		t.Fatal(err)
	}
	hasPrompt := strings.Contains(string(source), "session.prompt")
	readsEvents := strings.Contains(string(source), "events.jsonl")
	t.Logf("repo plugin: session.prompt=%v events.jsonl=%v", hasPrompt, readsEvents)
	if !hasPrompt || !readsEvents {
		t.Fatalf("repo plugin без wake-логики: session.prompt=%v events.jsonl=%v", hasPrompt, readsEvents)
	}
}

func runWakePositive(t *testing.T, _ string) {
	t.Helper()
	facts, info := runWakeScenario(t, newBase(t, "positive"),
		filepath.Join(repoRoot, ".opencode", "plugins", "beacon-bridge.js"), "pos")
	t.Logf("%s facts=%+v", info, facts)
	if err := verifyWake(facts); err != nil {
		t.Fatal(err)
	}
	if facts.SSEStarted {
		t.Log("GET /api/event через opencode api: session.execution.started подтверждён")
	} else {
		t.Log("ОГРАНИЧЕНИЕ ИНСТРУМЕНТА: opencode api CLI не отдаёт тело SSE /api/event " +
			"(0 строк); факт пробуждения подтверждён подпиской плагина внутри opencode и квитанцией")
	}
}

func runNegativeControl(t *testing.T, _ string) {
	t.Helper()
	facts, info := runWakeScenario(t, newBase(t, "negative"),
		filepath.Join(repoRoot, "tests", "e2e", "fixtures", "beacon-bridge-stub.js"), "neg")
	t.Logf("%s facts=%+v", info, facts)
	if err := verifyWake(facts); err == nil {
		t.Fatal("заглушка прошла проверку: негативный контроль сломан, тест не является e2e")
	} else {
		t.Logf("заглушка ожидаемо провалила проверку: %v", err)
	}
	if facts.Receipt || facts.Posted || facts.PromptOK {
		t.Fatal("заглушка записала квитанцию или вызвала prompt")
	}
}

// --- wake straight through the OpenCode HTTP API -----------------------------

// runLiveWakeViaAPI proves that a beacon wake pointer posted directly to
// POST /api/session/{sessionID}/prompt with delivery=steer is received by a
// real opencode session and continues its turn. This path does not go through
// the plugin: the official HTTP endpoint is the wake transport. Only the
// `opencode api` CLI is used, the same tool as the other live scenarios; no
// hand-written JSON-RPC client is involved.
func runLiveWakeViaAPI(t *testing.T, _ string) {
	t.Helper()
	base := newBase(t, "wake-api")
	srv := startServer(t, base, "")
	defer stopServer(srv)

	session := newSession(t, srv)

	token := "BEACON-WAKE-" + fmt.Sprint(time.Now().UnixNano())
	watchID := "watch-wake-e2e-" + fmt.Sprint(time.Now().UnixNano())
	wakeText := fmt.Sprintf(`{"kind":"verdict","verdict":"keep_working","watchID":%q,"target":"wake-e2e","token":%q}`, watchID, token)
	body := fmt.Sprintf(`{"text":%q,"delivery":"steer"}`, wakeText)

	resp := srv.api(t, "POST", "/api/session/"+session.ID+"/prompt", body)
	var ack struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		Type      string `json:"type"`
		Delivery  string `json:"delivery"`
		Payload   struct {
			Text string `json:"text"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(resp.Data, &ack); err != nil {
		t.Fatalf("POST prompt: не JSON: %s (%v)", resp.Data, err)
	}
	if ack.ID == "" || ack.Type != "user" || ack.Delivery != "steer" ||
		ack.SessionID != session.ID || ack.Payload.Text != wakeText {
		t.Fatalf("prompt ack не подтверждает steer-доставку: %+v", ack)
	}
	t.Logf("POST /api/session/%s/prompt: ack id=%s type=%s delivery=%s text=%q",
		session.ID, ack.ID, ack.Type, ack.Delivery, ack.Payload.Text)

	user, assistant, all := waitWakeMessages(t, srv, session.ID, token, 30*time.Second)
	t.Logf("сессия получила wake: user=%s created=%d; агент продолжил: assistant=%s created=%d; всего сообщений=%d",
		user.ID, user.Time.Created, assistant.ID, assistant.Time.Created, len(all))
	// Honesty: the configured model may be unavailable in a given environment.
	// opencode still records the assistant turn; whether it ended in content or
	// in a provider error is a fact we log, not a success we claim.
	t.Logf("результат хода: finish=%q", assistant.Finish)

	// Negative control: a bogus sessionID must not create a false wake.
	bogusID := "ses_wake_e2e_missing_" + fmt.Sprint(time.Now().UnixNano())
	badBody := fmt.Sprintf(`{"text":%q,"delivery":"steer"}`, "beacon wake NO-WAKE")
	badResp, badOut, badErr := srv.apiAttempt(t, "POST", "/api/session/"+bogusID+"/prompt", badBody)
	if badErr == nil {
		t.Fatalf("негативный контроль: невалидный sessionID принят: %+v", badResp)
	}
	if len(badResp.Data) != 0 {
		t.Fatalf("негативный контроль: получен wake-ack для невалидной сессии: %s", badResp.Data)
	}
	if !strings.Contains(badOut, "SessionNotFoundError") && !strings.Contains(badOut, "404") {
		t.Fatalf("негативный контроль: нет ошибки 404/SessionNotFoundError: %s", badOut)
	}
	// Prove nothing was created for the bogus id either.
	if _, missingOut, missingErr := srv.apiAttempt(t, "GET", "/api/session/"+bogusID+"/message", ""); missingErr == nil {
		t.Fatalf("негативный контроль: невалидная сессия была создана: %s", missingOut)
	}
	t.Logf("негативный контроль: POST в несуществующую сессию отклонён (%s); сессия не создана", strings.TrimSpace(badOut))
}

// --- T3.146: full cycle on a live opencode ----------------------------------

// callE2ETool calls one tool over the official go-sdk session and decodes its
// structured output. A stub answers {"ok":true}, which cannot decode into any
// of the real contract shapes below, so a stub also fails on this boundary.
func callE2ETool[T any](t *testing.T, ctx context.Context, session *mcpsdk.ClientSession,
	name string, args any, meta mcpsdk.Meta) T {
	t.Helper()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: name, Arguments: args, Meta: meta,
	})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("tools/call %s: isError=true: %v", name, result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("tools/call %s: marshal structured content: %v", name, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("tools/call %s: decode %T: %v (%s)", name, out, err, raw)
	}
	return out
}

// --- the full-cycle daemon: the verdict is a result, not a test input -------

// observedCI is one poll of external facts a CI daemon would fetch. It carries
// no verdict. The verdict is computed by domain.EvaluatePR from these facts,
// so changing the facts changes the verdict.
type observedCI struct {
	state      string
	headSHA    string
	rollup     string
	mergeable  string
	mergeState string
	review     string
	checks     map[string]domain.Check
	failures   []string
}

// greenObservation is the fact set that legitimately closes the cycle: every
// required check is green and the three ship facts are observed from check
// names. This is data about CI, not a stubbed ready verdict.
func greenObservation(headSHA string) observedCI {
	green := domain.Check{State: "SUCCESS", Conclusion: "SUCCESS", Required: true}
	return observedCI{
		state:      "OPEN",
		headSHA:    headSHA,
		rollup:     "SUCCESS",
		mergeable:  "MERGEABLE",
		mergeState: "CLEAN",
		checks: map[string]domain.Check{
			"ci/build": green,
			"ci/audit": green,
			"ci/tests": green,
		},
	}
}

// shipFactsFromChecks derives ShipFacts from observed required green checks.
// It mirrors the production adapter in internal/infra/mcp/tool_get_result.go
// (T3.75), which is unexported and cannot be imported from this external test
// package. Trivial has no observable signal and stays false instead of being
// invented.
func shipFactsFromChecks(checks map[string]domain.Check) domain.ShipFacts {
	var facts domain.ShipFacts
	for name, check := range checks {
		if !check.Required {
			continue
		}
		if !strings.EqualFold(check.Conclusion, "SUCCESS") && !strings.EqualFold(check.State, "SUCCESS") {
			continue
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "build") {
			facts.Build = true
		}
		if strings.Contains(lower, "audit") {
			facts.Audit = true
		}
		if strings.Contains(lower, "test") {
			facts.TDD = true
		}
	}
	return facts
}

// snapshot assembles a domain.Snapshot from observed facts. The ship facts are
// filled from the observed checks, so a green rollup alone cannot make a change
// ready (ADR 0061 item 2).
func (o observedCI) snapshot(w domain.Watch, at time.Time) domain.Snapshot {
	return domain.Snapshot{
		State:            o.state,
		HeadSHA:          o.headSHA,
		WatchHeadSHA:     w.HeadSHA,
		Rollup:           o.rollup,
		Mergeable:        o.mergeable,
		MergeStateStatus: o.mergeState,
		ReviewDecision:   o.review,
		Checks:           o.checks,
		Failures:         o.failures,
		SuitesValid:      true,
		At:               at,
		Ship:             shipFactsFromChecks(o.checks),
	}
}

// cycleDaemon is the test-side stand-in for the CI poll daemon. It observes
// facts, runs the real verdict engine, and commits the result atomically. It
// is the only place in this file that applies domain.Transition: scenarios
// call tick and never choose a verdict themselves.
type cycleDaemon struct {
	db      *store.DB
	observe func(w domain.Watch) observedCI
}

// decide computes the verdict from observed facts through the real engine.
// Evaluate applies the compiled-in ShipFacts formula (ADR 0061 item 2): a green
// rollup without build, audit and tdd/trivial facts stays watching. EvaluatePR
// deliberately skips that formula, so it cannot serve a daemon whose verdict
// must follow the observed facts.
func (d cycleDaemon) decide(w domain.Watch, at time.Time) (domain.Verdict, []domain.WakeTarget) {
	return domain.Evaluate(d.observe(w).snapshot(w, at), w.Policy)
}

// tick is one daemon poll: decide from facts, then commit the verdict, the
// obligation and the wake intent in one store transaction. It returns the new
// revision and obligation id so the scenario reads the verdict back through the
// real MCP handlers.
func (d cycleDaemon) tick(t *testing.T, watchID, subscriberID string) (uint64, string) {
	t.Helper()
	if d.db == nil {
		t.Fatal("демон: нет store")
	}
	w, err := d.db.Watch(watchID)
	if err != nil {
		t.Fatalf("демон: watch: %v", err)
	}
	now := time.Now().UTC()
	verdict, targets := d.decide(w, now)
	if verdict != domain.VerdictReady {
		t.Fatalf("демон вычислил verdict=%s из наблюдаемых фактов, ждали ready", verdict)
	}
	if len(targets) == 0 {
		t.Fatal("демон не вернул wake-цель для ready")
	}

	// Only the daemon applies the transition, with the verdict the engine
	// computed from facts. The full cycle below never calls it directly.
	next, effects := domain.Transition(w, domain.VerdictEvent(verdict, now))
	if next.Verdict != verdict || next.Status != domain.StatusNotifying ||
		next.Revision <= w.Revision || len(effects) == 0 {
		t.Fatalf("демон не перевёл Watch: до %+v после %+v effects=%v", w, next, effects)
	}
	obligationID := domain.ObligationKey(next.ID, next.Revision, subscriberID)
	commit := store.VerdictCommit{
		Watch: next,
		Obligation: domain.Obligation{
			ID: obligationID, WatchID: next.ID, Revision: next.Revision,
			SubscriberID: subscriberID, VerdictPointer: verdict, StagedAt: now,
		},
		Wake: store.WakeIntent{
			WatchID: next.ID, ObligationID: obligationID,
			Revision: next.Revision, StagedAt: now,
		},
	}
	if err := d.db.CommitVerdict(context.Background(), commit); err != nil {
		t.Fatalf("демон: commit verdict: %v", err)
	}
	t.Logf("демон полного цикла: watch=%s revision %d→%d verdict=%s (вычислен из фактов) obligation=%s",
		watchID, w.Revision, next.Revision, verdict, obligationID)
	return next.Revision, obligationID
}

// storedObligation reads the delivery facts from the durable store after the
// cycle. This is an external file fact, not a value returned by a tool.
func storedObligation(t *testing.T, stateDir, obligationID string) domain.Obligation {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "beacon.db"))
	if err != nil {
		t.Fatalf("open store for obligation check: %v", err)
	}
	defer func() { _ = db.Close() }()
	o, err := db.Obligation(obligationID)
	if err != nil {
		t.Fatalf("stored obligation %s: %v", obligationID, err)
	}
	return o
}

// storedRegistration reads the subscribers the watch_pr handler persisted on
// a Watch. It is a file fact, not a value echoed by the tool: a handler that
// answered ok:true without writing would leave no row here. ADR 0088 puts the
// executor registration inside the watch_pr call, before `waiting, watchID`.
func storedRegistration(t *testing.T, stateDir, watchID string) []domain.Subscriber {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "beacon.db"))
	if err != nil {
		t.Fatalf("open store for registration check: %v", err)
	}
	defer func() { _ = db.Close() }()
	w, err := db.Watch(watchID)
	if err != nil {
		t.Fatalf("watch for registration check: %v", err)
	}
	return w.Subscribers
}

func runLiveMCPCycle(t *testing.T, binary string) {
	t.Helper()
	base := newBase(t, "cycle")
	stateDir := filepath.Join(base, "state", "beacon")

	// A real opencode process with the repository plugin. The session id
	// comes from opencode api and is passed as the opaque _meta sessionID.
	srv := startServer(t, base, filepath.Join(repoRoot, ".opencode", "plugins", "beacon-bridge.js"))
	defer stopServer(srv)
	session := newSession(t, srv)
	waitPluginActive(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.Command(binary, "mcp")
	cmd.Env = append(os.Environ(),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"BEACON_INIT_BYPASS=1",
	)
	transport := &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-e2e-cycle", Version: "1"}, nil)
	mcpSession, err := client.Connect(ctx, transport, &mcpsdk.ClientSessionOptions{
		ProtocolVersion: "2025-06-18",
	})
	if err != nil {
		t.Fatalf("CommandTransport connect: %v", err)
	}
	defer func() { _ = mcpSession.Close() }()

	// initialize: the cycle protocol is part of the handshake, not a tool.
	init := mcpSession.InitializeResult()
	if init == nil || init.ServerInfo == nil || init.ServerInfo.Name != beaconmcp.ServerName {
		t.Fatalf("initialize: %+v", init)
	}
	if init.Instructions != beaconmcp.CycleInstructions {
		t.Fatalf("initialize instructions != CycleInstructions: %q", init.Instructions)
	}
	for _, token := range []string{
		"Beacon cycle protocol", "beacon_watch_pr", "beacon_wait_result",
		"beacon_get_result", "beacon_ack", "waiting, watchID", "ready",
	} {
		if !strings.Contains(init.Instructions, token) {
			t.Fatalf("в CycleInstructions нет %q", token)
		}
	}
	t.Logf("initialize: %s %s protocol=%s cycle=%d байт", init.ServerInfo.Name,
		init.ServerInfo.Version, init.ProtocolVersion, len(init.Instructions))

	// tools/list: exactly the eight contracted names, no ninth summary tool.
	tools, err := mcpSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Clone(expectedSet)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tools/list: получено %v, ожидалось %v", names, want)
	}
	t.Logf("tools/list: ровно %d инструментов: %v", len(names), names)

	meta := mcpsdk.Meta{"ai.opencode/sessionID": session.ID}
	watch := callE2ETool[beaconmcp.WatchPROutput](t, ctx, mcpSession, "beacon_watch_pr", map[string]any{
		"repo": "refusedguy/beacon", "pr": 141,
		"headSHA": "beacon-e2e-t3146-head", "role": string(domain.RoleExecutor),
		"notify": map[string]any{"adapter": "opencode", "target": session.ID},
	}, meta)
	// A freshly registered Watch starts at revision 0; the first verdict
	// transition bumps it. A stub would have no watchID at all.
	if watch.WatchID == "" ||
		watch.Status != string(domain.StatusWatching) || watch.Verdict != string(domain.VerdictWatching) {
		t.Fatalf("beacon_watch_pr не записал Watch: %+v", watch)
	}
	t.Logf("beacon_watch_pr: watchID=%s revision=%d status=%s verdict=%s",
		watch.WatchID, watch.Revision, watch.Status, watch.Verdict)

	// T5.61: the registration point is this watch_pr call (ADR 0088). Verify
	// the durable record, not the tool answer. The stored executor subscriber
	// must carry the sessionID from _meta and the notify adapter. This reads a
	// file the handler wrote; it needs no live probe beyond the server already
	// running in this scenario.
	subs := storedRegistration(t, stateDir, watch.WatchID)
	var registered *domain.Subscriber
	for i := range subs {
		if subs[i].Role == domain.RoleExecutor {
			registered = &subs[i]
			break
		}
	}
	if registered == nil || registered.ID != session.ID ||
		registered.Adapter != "opencode" || registered.Target != session.ID {
		t.Fatalf("T5.61: регистрация исполнителя в store: %+v, ждали id=%s adapter=opencode target=%s",
			subs, session.ID, session.ID)
	}
	t.Logf("T5.61: регистрация в store: subscriber=%s adapter=%s target=%s role=%s",
		registered.ID, registered.Adapter, registered.Target, registered.Role)

	// Negative control: a card id in _meta is not an addressable session. The
	// real handler must refuse it, otherwise the stored sessionID above is a
	// stub value and proves nothing. A stub that answers ok:true would pass.
	cardMeta := mcpsdk.Meta{"ai.opencode/sessionID": "T5.61"}
	badReg, badRegErr := mcpSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "beacon_watch_pr",
		Arguments: map[string]any{
			"repo": "refusedguy/beacon", "pr": 142,
			"headSHA": "beacon-e2e-t3146-registration-negative",
			"role":    string(domain.RoleExecutor),
		},
		Meta: cardMeta,
	})
	if badRegErr == nil && !badReg.IsError {
		t.Fatal("T5.61: негативный контроль регистрации: card id в _meta принят как сессия")
	}
	t.Logf("T5.61: негативный контроль регистрации: card id в _meta отклонён (%v)", badRegErr)

	// Acceptance: a call written before the executor descriptor existed still
	// works with no _meta and no executor field. It finds its identity in
	// notify.target, so the registration change did not break older callers.
	legacy := callE2ETool[beaconmcp.WatchPROutput](t, ctx, mcpSession, "beacon_watch_pr", map[string]any{
		"repo": "refusedguy/beacon", "pr": 143,
		"headSHA": "beacon-e2e-t3146-legacy",
		"role":    string(domain.RoleOrchestrator),
		"notify":  map[string]any{"adapter": "opencode", "target": session.ID},
	}, mcpsdk.Meta{})
	if legacy.WatchID == "" {
		t.Fatalf("T5.61: вызов без executor сломан: %+v", legacy)
	}
	t.Logf("T5.61: вызов без executor работает: watchID=%s role=%s", legacy.WatchID, domain.RoleOrchestrator)

	// The agent's turn report from CycleInstructions step 2 goes to a real
	// opencode session through the loaded plugin, not through a test double.
	report := "waiting, " + watch.WatchID
	sse := startSSE(t, srv, session.ID)
	defer sse.stop()
	id, info := postWakePointer(t, base, "cycle", session.ID, report)
	facts := waitFacts(t, stateDir, id, session.ID, sse, 15*time.Second)
	if err := verifyWake(facts); err != nil {
		t.Fatalf("плагин не доставил отчёт в живую сессию: %v", err)
	}
	t.Logf("отчёт по CycleInstructions доставлен в живую сессию: %q; %s facts=%+v", report, info, facts)

	// The daemon decides from observed CI facts and commits the result; the
	// real handlers then read the verdict back. This scenario never applies a
	// transition and never picks a verdict itself.
	db, err := store.Open(filepath.Join(stateDir, "beacon.db"))
	if err != nil {
		t.Fatalf("open store for cycle daemon: %v", err)
	}
	defer func() { _ = db.Close() }()
	daemon := cycleDaemon{db: db, observe: func(w domain.Watch) observedCI {
		return greenObservation(w.HeadSHA)
	}}
	revision, obligationID := daemon.tick(t, watch.WatchID, session.ID)
	wait := callE2ETool[beaconmcp.WaitReasonOutput](t, ctx, mcpSession, "beacon_wait_result", map[string]any{
		"watchID": watch.WatchID, "timeoutMs": 5000, "clientRequestId": "t3146-" + watch.WatchID,
	}, meta)
	if wait.Verdict != string(domain.VerdictReady) || wait.Revision != revision ||
		wait.Status != string(domain.StatusNotifying) {
		t.Fatalf("beacon_wait_result: %+v, ждали verdict=ready revision=%d", wait, revision)
	}
	t.Logf("beacon_wait_result: verdict=%s revision=%d reason=%s", wait.Verdict, wait.Revision, wait.Reason)

	result := callE2ETool[beaconmcp.GetResultOutput](t, ctx, mcpSession, "beacon_get_result", map[string]any{
		"watchID": watch.WatchID,
	}, meta)
	if result.Verdict != string(domain.VerdictReady) || result.Revision != revision ||
		result.ObligationID != obligationID {
		t.Fatalf("beacon_get_result: %+v, ждали verdict=ready revision=%d obligation=%s",
			result, revision, obligationID)
	}
	t.Logf("beacon_get_result: verdict=%s revision=%d obligation=%s", result.Verdict, result.Revision, result.ObligationID)

	ack := callE2ETool[beaconmcp.OKOutput](t, ctx, mcpSession, "beacon_ack", map[string]any{
		"watchID": watch.WatchID, "obligationID": result.ObligationID, "revision": result.Revision,
	}, meta)
	if !ack.OK {
		t.Fatal("beacon_ack: ok=false")
	}
	o := storedObligation(t, stateDir, obligationID)
	if o.ReadAt.IsZero() || o.ClaimedAt.IsZero() {
		t.Fatalf("в store нет фактов доставки: readAt=%v claimedAt=%v", o.ReadAt, o.ClaimedAt)
	}
	t.Logf("beacon_ack: ok=true readAt=%s claimedAt=%s", o.ReadAt.Format(time.RFC3339), o.ClaimedAt.Format(time.RFC3339))

	finalReport := fmt.Sprintf("ready, %s, revision %d", watch.WatchID, result.Revision)
	if !strings.Contains(finalReport, watch.WatchID) || !strings.Contains(finalReport, result.Verdict) {
		t.Fatalf("итоговый отчёт не собран из ответов обработчиков: %q", finalReport)
	}
	t.Logf("итоговый отчёт по CycleInstructions: %q", finalReport)

	// Negative control 1: a stub handler answers ok:true even for invalid
	// input and never writes a Watch. A real handler must refuse this call.
	bad, badErr := mcpSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "beacon_watch_pr",
		Arguments: map[string]any{"repo": "", "pr": 141, "role": string(domain.RoleExecutor)},
		Meta:      meta,
	})
	if badErr == nil && !bad.IsError {
		t.Fatal("негативный контроль обработчика: заглушка ответила ok:true на пустой repo")
	}
	t.Logf("негативный контроль обработчика: пустой repo отклонён (%v)", badErr)

	// Negative control 2: the same wake verification with a no-op plugin must
	// fail, otherwise the loaded-plugin fact above proves nothing.
	negFacts, negInfo := runWakeScenarioWithTimeout(t,
		newBase(t, "cycle-negative"),
		filepath.Join(repoRoot, "tests", "e2e", "fixtures", "beacon-bridge-stub.js"),
		"cycle-neg", 5*time.Second)
	if err := verifyWake(negFacts); err == nil {
		t.Fatal("негативный контроль плагина: заглушка прошла проверку пробуждения")
	} else {
		t.Logf("негативный контроль плагина: %s заглушка ожидаемо провалилась: %v", negInfo, err)
	}
}

// --- ADR 0061 item 2: ready needs observed ShipFacts -------------------------

// runVerdictReadyGateRequiresShipFacts proves the ready verdict is a function
// of observed facts. A green rollup with required checks that carry no ship
// fact must not become ready (regression of T3.72 and T3.78). Every verdict
// comes from the real engine through the daemon, never from a string comparison
// in this test.
func runVerdictReadyGateRequiresShipFacts(t *testing.T, _ string) {
	t.Helper()
	base := newBase(t, "shipfacts")
	stateDir := filepath.Join(base, "state", "beacon")
	if err := os.MkdirAll(filepath.Join(stateDir, "receipts"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(stateDir, "beacon.db"))
	if err != nil {
		t.Fatalf("open store for shipfacts gate: %v", err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	w := domain.Watch{
		ID:      "watch-shipfacts-e2e-" + fmt.Sprint(now.UnixNano()),
		Repo:    "refusedguy/beacon",
		PR:      141,
		HeadSHA: "shipfacts-e2e-head",
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{
			ID: "shipfacts-e2e", Role: domain.RoleExecutor,
			Adapter: "opencode", Target: "shipfacts-e2e",
		}},
		Policy:    domain.DefaultPolicy(),
		CreatedAt: now,
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("save watch for shipfacts gate: %v", err)
	}

	// Positive: required green checks named build, audit and test give the
	// engine the ship facts it gates ready on.
	positive := greenObservation(w.HeadSHA)
	daemon := cycleDaemon{db: db, observe: func(domain.Watch) observedCI { return positive }}
	verdict, targets := daemon.decide(w, now)
	if verdict != domain.VerdictReady || len(targets) == 0 {
		t.Fatalf("ShipFacts gate: ждали ready с build/audit/test, получили verdict=%s targets=%v", verdict, targets)
	}
	t.Logf("ShipFacts gate: наблюдаемые build/audit/test → verdict=%s", verdict)

	// The daemon commits that decision. The stored watch and obligation must
	// not diverge from what the engine decided: the verdict is the daemon's
	// result, not a value the scenario wrote.
	revision, obligationID := daemon.tick(t, w.ID, "shipfacts-e2e")
	stored, err := db.Watch(w.ID)
	if err != nil {
		t.Fatalf("read stored watch: %v", err)
	}
	if stored.Verdict != verdict || stored.Revision != revision {
		t.Fatalf("расхождение фактов: демон решил %s@%d, в store %s@%d",
			verdict, revision, stored.Verdict, stored.Revision)
	}
	o, err := db.Obligation(obligationID)
	if err != nil {
		t.Fatalf("read obligation: %v", err)
	}
	if o.VerdictPointer != verdict {
		t.Fatalf("расхождение фактов: verdict pointer=%s, решение=%s", o.VerdictPointer, verdict)
	}
	t.Logf("расхождение фактов: решение демона и store совпадают: %s@%d", stored.Verdict, stored.Revision)

	// Negative control: same green rollup and required green checks, but their
	// names carry no ship fact. The engine must not answer ready.
	negative := positive
	negative.checks = map[string]domain.Check{
		"ci/lint": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
		"ci/docs": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
	}
	negDaemon := cycleDaemon{db: db, observe: func(domain.Watch) observedCI { return negative }}
	negativeVerdict, _ := negDaemon.decide(w, now)
	if negativeVerdict == domain.VerdictReady {
		t.Fatal("негативный контроль ShipFacts: зелёные чеки без ShipFacts дали ready")
	}
	t.Logf("негативный контроль ShipFacts: verdict=%s, ожидалось не ready", negativeVerdict)

	// Divergence: one rollup, two different fact sets, two different verdicts.
	if negativeVerdict == verdict {
		t.Fatal("расхождение фактов: разные наблюдения дали одинаковый verdict")
	}
	t.Logf("расхождение фактов: один зелёный rollup дал %s и %s в зависимости от ShipFacts",
		verdict, negativeVerdict)
}

// TestFullCycleDaemonFactDivergence proves the daemon's verdict follows the
// observed facts. Two observations with the same green rollup but different
// ship facts must produce different verdicts. A hardcoded verdict driver
// cannot do this.
func TestFullCycleDaemonFactDivergence(t *testing.T) {
	head := "divergence-head"
	w := domain.Watch{ID: "divergence-watch", HeadSHA: head, Policy: domain.DefaultPolicy()}
	daemonWith := func(facts map[string]domain.Check) cycleDaemon {
		o := greenObservation(head)
		o.checks = facts
		return cycleDaemon{observe: func(domain.Watch) observedCI { return o }}
	}

	withFacts := daemonWith(map[string]domain.Check{
		"ci/build": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
		"ci/audit": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
		"ci/tests": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
	})
	withoutFacts := daemonWith(map[string]domain.Check{
		"ci/lint": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
		"ci/docs": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
	})

	now := time.Now().UTC()
	with, _ := withFacts.decide(w, now)
	without, _ := withoutFacts.decide(w, now)
	if with != domain.VerdictReady {
		t.Fatalf("наблюдение с ShipFacts дало verdict=%s, ждали ready", with)
	}
	if without == domain.VerdictReady {
		t.Fatalf("наблюдение без ShipFacts дало verdict=%s, ждали не ready", without)
	}
	if with == without {
		t.Fatal("расхождение фактов: разные наблюдения дали одинаковый verdict")
	}
	t.Logf("расхождение фактов подтверждено: ShipFacts дают %s, их отсутствие даёт %s", with, without)
}

// --- the suite ---------------------------------------------------------------

func TestE2E(t *testing.T) {
	// Engine gate: refuse to work at all if the scenario set changed.
	if err := verifyFrozenScenarios(); err != nil {
		t.Fatalf("замороженный crash-test отказался стартовать: %v", err)
	}

	version := strings.TrimSpace(string(mustRun(t, openBin, "--version")))
	t.Logf("opencode: %s", version)
	if os.Getenv("OPENCODE_SKIP_VERSION") == "" {
		want := envOr("OPENCODE_EXPECT_VERSION", "2.3.0")
		if !strings.Contains(version, want) {
			t.Fatalf("нужен реальный opencode %s, получено %s", want, version)
		}
	}

	src := resolveMcpSource(t)
	t.Logf("MCP source: %s", src)
	binary := filepath.Join(newBase(t, "shared"), "beacon")
	buildBeacon(t, src, binary)

	for _, s := range e2eScenarios {
		s := s
		t.Run(s.name, func(t *testing.T) { s.run(t, binary) })
	}
}

func mustRun(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return out
}
