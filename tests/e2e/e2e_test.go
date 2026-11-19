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
//  * The wake plugin used in the positive check is a copy of the working T0.3
//    spike. It is NOT .opencode/plugins/beacon-bridge.js in this repository.
//  * Seven tools prove the protocol and the contract. The handlers registered
//    by mcp.NewServer are stubs, so no tool behaviour is claimed.
//  * The negative check installs a no-op handler and requires the positive
//    verification to fail. A harness that stays green with a stub is not e2e.

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	goBin       = envOr("GO_BIN", "/tmp/opencode/go126/bin/go")
	openBin     = envOr("OPENCODE_BIN", "opencode")
	repoRoot    = repoRootPath()
	expectedSet = []string{
		"beacon_watch_pr", "beacon_wait_result", "beacon_get_result",
		"beacon_list_running", "beacon_ack", "beacon_cancel_watch", "beacon_stats",
	}
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

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
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
	if len(names) != 7 {
		t.Fatalf("tools/list вернул %d инструментов: %v", len(names), names)
	}
	if !slices.Equal(names, want) {
		t.Fatalf("tools/list: получено %v, ожидалось %v", names, want)
	}
	t.Logf("tools/list: ровно 7 инструментов: %v", names)

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
	ID       string `json:"id"`
	Source   struct {
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

	port := freePort(t)
	logPath := filepath.Join(base, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(base, "xdg-data"),
		"XDG_CACHE_HOME="+filepath.Join(base, "xdg-cache"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"OPENCODE_CONFIG="+filepath.Join(project, "opencode.json"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
	)
	proc := exec.Command(openBin, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	proc.Dir = project
	proc.Env = env
	proc.Stdout = logFile
	proc.Stderr = logFile
	if err := proc.Start(); err != nil {
		t.Fatalf("opencode serve: %v", err)
	}
	srv := &server{
		url:     fmt.Sprintf("http://127.0.0.1:%d", port),
		proc:    proc,
		logPath: logPath,
		base:    base,
		project: project,
	}

	passwordRe := regexp.MustCompile(`server password\s+(\S+)`)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if proc.ProcessState != nil {
			t.Fatalf("opencode serve завершился: %s", tail(t, logPath))
		}
		data, _ := os.ReadFile(logPath)
		if m := passwordRe.FindSubmatch(data); m != nil && strings.Contains(string(data), "server listening") {
			srv.password = string(m[1])
			return srv
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopServer(srv)
	t.Fatalf("opencode serve не поднялся за 30s: %s", tail(t, logPath))
	return nil
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

// runWakeScenario starts opencode with the given plugin, writes one pointer to
// events.jsonl and returns the facts that external observers recorded.
func runWakeScenario(t *testing.T, base, pluginPath, tag string) (wakeFacts, string) {
	t.Helper()
	srv := startServer(t, base, pluginPath)
	defer stopServer(srv)

	sessionResp := srv.api(t, "POST", "/api/session", "{}")
	var session apiSession
	if err := json.Unmarshal(sessionResp.Data, &session); err != nil || session.ID == "" {
		t.Fatalf("POST /api/session: %s (%v)", sessionResp.Data, err)
	}
	t.Logf("сессия: %s", session.ID)

	// The plugin is loaded lazily; poll /api/plugin until it shows up.
	var active pluginInfo
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		listResp := srv.api(t, "GET", "/api/plugin", "")
		var plugins []pluginInfo
		if err := json.Unmarshal(listResp.Data, &plugins); err != nil {
			t.Fatalf("GET /api/plugin: %s (%v)", listResp.Data, err)
		}
		for _, p := range plugins {
			if p.ID == "beacon-bridge" {
				active = p
				break
			}
		}
		if active.ID != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("/api/plugin beacon-bridge: source=%q path=%q status=%q server=%v",
		active.Source.Type, active.Source.Path, active.State.Status, active.Features.Server)

	sse := startSSE(t, srv, session.ID)
	defer sse.stop()

	stateDir := filepath.Join(base, "state", "beacon")
	id := "beacon-e2e-" + tag + "-" + fmt.Sprint(time.Now().UnixNano())
	pointer := fmt.Sprintf(`{"id":%q,"sessionID":%q,"text":"beacon-e2e wake"}`+"\n", id, session.ID)
	f, err := os.OpenFile(filepath.Join(stateDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(pointer); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	facts := waitFacts(t, stateDir, id, session.ID, sse, 15*time.Second)
	return facts, fmt.Sprintf("id=%s session=%s sse_lines=%d", id, session.ID, sse.count())
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

// --- the suite ---------------------------------------------------------------

func TestE2E(t *testing.T) {
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

	t.Run("repo_plugin_skeleton", func(t *testing.T) {
		source, err := os.ReadFile(filepath.Join(repoRoot, ".opencode", "plugins", "beacon-bridge.js"))
		if err != nil {
			t.Fatal(err)
		}
		hasPrompt := strings.Contains(string(source), "session.prompt")
		readsEvents := strings.Contains(string(source), "events.jsonl")
		t.Logf("repo plugin: session.prompt=%v events.jsonl=%v", hasPrompt, readsEvents)
		if hasPrompt && readsEvents {
			t.Log("repo plugin уже содержит wake-логику; прогон всё равно использует эталон из fixtures")
		} else {
			t.Log("БЛОКЕР: repo plugin в main — наблюдатель без wake-логики; шаг (e) на нём не пройдёт")
		}
		// No assertion on skeleton state: this stays true after T3.6 lands.
	})

	t.Run("mcp_go_sdk_CommandTransport", func(t *testing.T) {
		testMCPWithGoSDK(t, binary)
	})

	t.Run("mcp_inspector_optional", func(t *testing.T) {
		testInspectorOptional(t, binary)
	})

	t.Run("opencode_wake_with_reference_plugin", func(t *testing.T) {
		facts, info := runWakeScenario(t, newBase(t, "positive"),
			filepath.Join(repoRoot, "tests", "e2e", "fixtures", "beacon-bridge.js"), "pos")
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
	})

	t.Run("negative_control_stub_handler_must_fail", func(t *testing.T) {
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
	})
}

func mustRun(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return out
}
