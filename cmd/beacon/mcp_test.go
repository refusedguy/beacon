package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// TestW43MCPDispatchesAndRuns proves that `beacon mcp` reaches the MCP runner,
// that the runner is called exactly once and that its exit code and diagnostics
// propagate through the CLI. The runner is replaced so the test does not start
// a real stdio server.
func TestW43MCPDispatchesAndRuns(t *testing.T) {
	previous := mcpRunner
	if previous == nil {
		t.Fatal("mcpRunner не установлен: настоящая реализация отсутствует")
	}

	calls := 0
	mcpRunner = func(errOut io.Writer) int {
		calls++
		fmt.Fprint(errOut, "mcp served\n")
		return 7
	}
	t.Cleanup(func() { mcpRunner = previous })

	c, out, errOut, childCalls := t06NewCLI(t, t06NoChild(t))
	if code := c.Run([]string{"mcp"}); code != 7 {
		t.Fatalf("код выхода %d, ожидался 7", code)
	}
	if calls != 1 {
		t.Fatalf("раннер вызван %d раз, ожидался 1", calls)
	}
	if len(*childCalls) != 0 {
		t.Fatalf("beacon mcp не должен запускать дочерний процесс: %+v", *childCalls)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if errOut.String() != "mcp served\n" {
		t.Fatalf("stderr не передан в раннер: %q", errOut.String())
	}
}

// TestW43MCPInCatalog checks that mcp is discoverable through list and what in
// the same way as task and doctor.
func TestW43MCPInCatalog(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))
	if code := c.Run([]string{"list", "--format", "json"}); code != 0 {
		t.Fatalf("list: код %d, stderr %q", code, errOut.String())
	}
	var entries []command
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "mcp" {
			found = true
			if entry.Usage != "beacon mcp" || entry.Summary == "" {
				t.Fatalf("запись mcp неполная: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("в каталоге нет mcp: %q", out.String())
	}

	whatOut, whatErr := new(bytes.Buffer), new(bytes.Buffer)
	if code := runWhat([]string{"mcp"}, whatOut, whatErr); code != 0 {
		t.Fatalf("what mcp: код %d, stderr %q", code, whatErr.String())
	}
	if !strings.Contains(whatOut.String(), "beacon mcp") {
		t.Fatalf("what mcp не знает команду: %q", whatOut.String())
	}
}

// TestMCPCompositionRootBuildsServerWithStore proves that `beacon mcp` opens a
// real SQLite store and passes it, together with the other production ports,
// into the server. The transport is replaced so the test does not block on
// stdin; the store itself is real and is exercised through a save/read pair.
func TestMCPCompositionRootBuildsServerWithStore(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	previous := serveMCP
	t.Cleanup(func() { serveMCP = previous })

	called := false
	serveMCP = func(_ context.Context, deps mcp.Dependencies) error {
		called = true
		if deps.Store == nil {
			t.Fatal("composition root передал nil Store в сервер")
		}
		if _, ok := deps.Store.(*store.DB); !ok {
			t.Fatalf("Store имеет тип %T, ожидался *store.DB", deps.Store)
		}
		if deps.Stamper == nil || deps.Writer == nil || deps.Events == nil || deps.Snapshot == nil {
			t.Fatalf("не все SQL-порты подключены: %+v", deps)
		}

		w := domain.Watch{
			ID:        "composition-root-smoke",
			Repo:      "refusedguy/beacon",
			PR:        42,
			HeadSHA:   "composition-head",
			Status:    domain.StatusWatching,
			Verdict:   domain.VerdictWatching,
			CreatedAt: time.Now(),
		}
		if err := deps.Store.SaveWatch(w); err != nil {
			t.Fatalf("SaveWatch через настоящий store: %v", err)
		}
		got, err := deps.Store.Watch(w.ID)
		if err != nil {
			t.Fatalf("Watch через настоящий store: %v", err)
		}
		if got.HeadSHA != w.HeadSHA || got.Repo != w.Repo {
			t.Fatalf("watch не сохранился: %+v", got)
		}
		return nil
	}

	errOut := new(bytes.Buffer)
	if code := mcpRunner(errOut); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if !called {
		t.Fatal("сервер не был запущен с собранными зависимостями")
	}
	if _, err := os.Stat(filepath.Join(base, "beacon", mcpStateDBName)); err != nil {
		t.Fatalf("файл состояния не создан: %v", err)
	}
}

// TestReadMCPConnection covers the connection-file contract from ADR 0036 §1.
// A missing file is a valid local stdio mode; a present but invalid file must
// not be used as a reconnect target.
func TestReadMCPConnection(t *testing.T) {
	dir := t.TempDir()

	conn, err := readMCPConnection(dir)
	if err != nil {
		t.Fatalf("отсутствующий файл: %v", err)
	}
	if conn != (mcpConnection{}) {
		t.Fatalf("отсутствующий файл дал соединение %+v", conn)
	}
	if conn.target() != "" {
		t.Fatalf("пустое соединение дало target %q", conn.target())
	}

	want := mcpConnection{Host: "127.0.0.1", Port: 7432, Token: "secret"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mcpConnectionFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readMCPConnection(dir)
	if err != nil {
		t.Fatalf("корректный файл: %v", err)
	}
	if got != want {
		t.Fatalf("соединение %+v, ожидалось %+v", got, want)
	}
	if got.target() != "127.0.0.1:7432" {
		t.Fatalf("target %q", got.target())
	}

	cases := []struct {
		name string
		data string
	}{
		{"битый JSON", "{"},
		{"пустой host", `{"host":"","port":7432}`},
		{"порт ноль", `{"host":"127.0.0.1","port":0}`},
		{"порт вне диапазона", `{"host":"127.0.0.1","port":70000}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readMCPConnection(dir); err == nil {
				t.Fatalf("ожидалась ошибка для %q", tc.data)
			}
		})
	}
}

// TestMCPReconnectsAfterAddressChange is the acceptance test for ADR 0036 §1.
// The connection file changes between two `beacon mcp` runs. The client reads
// the new address and port on the second start, and the watch registered on
// the first run is still waiting in the persisted store.
func TestMCPReconnectsAfterAddressChange(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	state := filepath.Join(base, "beacon")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}

	previousConnector := mcpConnector
	previousServe := serveMCP
	previousActive := activeMCPConnection
	t.Cleanup(func() {
		mcpConnector = previousConnector
		serveMCP = previousServe
		activeMCPConnection = previousActive
	})

	var connects []mcpConnection
	mcpConnector = func(_ context.Context, conn mcpConnection) error {
		connects = append(connects, conn)
		return nil
	}

	const watchID = "reconnect-watch"
	serveMCP = func(_ context.Context, deps mcp.Dependencies) error {
		if _, err := deps.Store.Watch(watchID); err != nil {
			w := domain.Watch{
				ID:        watchID,
				Repo:      "refusedguy/beacon",
				PR:        42,
				HeadSHA:   "reconnect-head",
				Status:    domain.StatusWatching,
				Verdict:   domain.VerdictWatching,
				CreatedAt: time.Now(),
			}
			if err := deps.Store.SaveWatch(w); err != nil {
				t.Fatalf("SaveWatch: %v", err)
			}
		}
		return nil
	}

	writeConnection := func(host string, port int) {
		data, err := json.Marshal(mcpConnection{Host: host, Port: port, Token: "token"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, mcpConnectionFileName), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// First daemon instance.
	writeConnection("10.0.0.1", 4001)
	if code := mcpRunner(new(bytes.Buffer)); code != 0 {
		t.Fatalf("первый запуск: код %d", code)
	}
	if activeMCPConnection.Host != "10.0.0.1" || activeMCPConnection.Port != 4001 {
		t.Fatalf("первый запуск подключился к %+v", activeMCPConnection)
	}

	// The daemon moved to another address and port.
	writeConnection("10.0.0.2", 4002)
	if code := mcpRunner(new(bytes.Buffer)); code != 0 {
		t.Fatalf("второй запуск: код %d", code)
	}
	if len(connects) != 2 {
		t.Fatalf("connect вызван %d раз, ожидался 2", len(connects))
	}
	if connects[0].target() != "10.0.0.1:4001" || connects[1].target() != "10.0.0.2:4002" {
		t.Fatalf("неверная цепочка подключений: %+v", connects)
	}
	if activeMCPConnection.Host != "10.0.0.2" || activeMCPConnection.Port != 4002 {
		t.Fatalf("после рестарта активное соединение %+v", activeMCPConnection)
	}

	// The registered task survived the restart on the new endpoint.
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()
	w, err := deps.Store.Watch(watchID)
	if err != nil {
		t.Fatalf("задача потеряна после рестарта: %v", err)
	}
	if w.Status != domain.StatusWatching {
		t.Fatalf("задача не ждёт после рестарта: статус %s", w.Status)
	}
}

// TestMCPConnectFailurePropagates checks that a failed reconnect is reported
// through the runner exit code instead of silently serving a dead endpoint.
func TestMCPConnectFailurePropagates(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	state := filepath.Join(base, "beacon")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(mcpConnection{Host: "127.0.0.1", Port: 4003})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, mcpConnectionFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	previousConnector := mcpConnector
	t.Cleanup(func() { mcpConnector = previousConnector })
	mcpConnector = func(_ context.Context, _ mcpConnection) error {
		return fmt.Errorf("master недоступен")
	}

	errOut := new(bytes.Buffer)
	if code := mcpRunner(errOut); code == 0 {
		t.Fatal("ошибка подключения не изменила код выхода")
	}
	if !strings.Contains(errOut.String(), "connect to 127.0.0.1:4003") {
		t.Fatalf("в stderr нет адреса подключения: %q", errOut.String())
	}
}

// TestMCPPrecheckFailsFastWithoutBypass is the acceptance test for ADR 0041 §6.
// With an init event expected and no bypass configured, `beacon mcp` ends with
// precheck_failure and a clear reason before the server starts waiting. The
// run must not hang for init_event_timeout.
func TestMCPPrecheckFailsFastWithoutBypass(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv("BEACON_INIT_EVENT", "control-plane")
	t.Setenv("BEACON_INIT_BYPASS", "")
	t.Setenv("BEACON_INIT_EVENT_TIMEOUT", "")

	previousServe := serveMCP
	t.Cleanup(func() { serveMCP = previousServe })

	serveCalled := false
	serveMCP = func(context.Context, mcp.Dependencies) error {
		serveCalled = true
		return nil
	}

	start := time.Now()
	errOut := new(bytes.Buffer)
	code := mcpRunner(errOut)
	elapsed := time.Since(start)

	if code == 0 {
		t.Fatalf("код выхода %d, ожидался не ноль; stderr %q", code, errOut.String())
	}
	if serveCalled {
		t.Fatal("сервер начал ждать до отказа precheck")
	}
	if !strings.Contains(errOut.String(), mcpPrecheckFailure) {
		t.Fatalf("в stderr нет precheck_failure: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "bypass") {
		t.Fatalf("в stderr нет причины про bypass: %q", errOut.String())
	}
	if elapsed >= initEventTimeoutDefault/2 {
		t.Fatalf("precheck занял %s, ожидался быстрый отказ (менее %s)", elapsed, initEventTimeoutDefault/2)
	}
}

// TestMCPPrecheckSkippedWithoutInitEventSource proves that a run with no init
// event to precheck proceeds. The reconnect tests in this file rely on that
// behavior: they configure no init event source and no bypass.
func TestMCPPrecheckSkippedWithoutInitEventSource(t *testing.T) {
	t.Setenv("BEACON_INIT_EVENT", "")
	t.Setenv("BEACON_INIT_BYPASS", "")
	if err := runMCPPrecheck(context.Background(), readInitEventConfig()); err != nil {
		t.Fatalf("без источника init event precheck должен проходить: %v", err)
	}
}

// TestMCPPrecheckPassesWithBypass proves that a configured bypass lets the
// server start waiting, so the precheck does not block the normal run.
func TestMCPPrecheckPassesWithBypass(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv("BEACON_INIT_EVENT", "control-plane")
	t.Setenv("BEACON_INIT_BYPASS", "1")

	previousServe := serveMCP
	t.Cleanup(func() { serveMCP = previousServe })

	serveCalled := false
	serveMCP = func(context.Context, mcp.Dependencies) error {
		serveCalled = true
		return nil
	}

	if code := mcpRunner(new(bytes.Buffer)); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0", code)
	}
	if !serveCalled {
		t.Fatal("bypass не пропустил запуск сервера")
	}
}

// TestMCPPrecheckFailureReason checks the error callers branch on. The outcome
// is precheck_failure and the failure carries a non-empty reason.
func TestMCPPrecheckFailureReason(t *testing.T) {
	cfg := initEventConfig{source: "control-plane", bypass: false, timeout: 3 * time.Second}
	err := runMCPPrecheck(context.Background(), cfg)
	if !isPrecheckFailure(err) {
		t.Fatalf("ожидалась ошибка precheck_failure, получено %v", err)
	}
	failure, ok := err.(*precheckFailureError)
	if !ok {
		t.Fatalf("тип ошибки %T, ожидался *precheckFailureError", err)
	}
	if failure.reason == "" {
		t.Fatal("причина precheck пуста")
	}
	if !strings.Contains(err.Error(), mcpPrecheckFailure) {
		t.Fatalf("в ошибке нет precheck_failure: %q", err.Error())
	}
	if !strings.Contains(failure.reason, "control-plane") {
		t.Fatalf("в причине нет имени источника: %q", failure.reason)
	}
}

// TestReadInitEventConfig covers parsing at the CLI boundary: the default
// timeout, the opt-in bypass and a fallback for an invalid timeout value.
func TestReadInitEventConfig(t *testing.T) {
	t.Setenv("BEACON_INIT_EVENT", "")
	t.Setenv("BEACON_INIT_BYPASS", "")
	t.Setenv("BEACON_INIT_EVENT_TIMEOUT", "")
	cfg := readInitEventConfig()
	if cfg.source != "" || cfg.bypass {
		t.Fatalf("пустая конфигурация дала %+v", cfg)
	}
	if cfg.timeout != initEventTimeoutDefault {
		t.Fatalf("таймаут по умолчанию %s, ожидался %s", cfg.timeout, initEventTimeoutDefault)
	}

	t.Setenv("BEACON_INIT_EVENT", "control-plane")
	t.Setenv("BEACON_INIT_BYPASS", "true")
	t.Setenv("BEACON_INIT_EVENT_TIMEOUT", "2s")
	cfg = readInitEventConfig()
	if cfg.source != "control-plane" || !cfg.bypass || cfg.timeout != 2*time.Second {
		t.Fatalf("конфигурация %+v, ожидался источник control-plane, bypass и таймаут 2s", cfg)
	}

	t.Setenv("BEACON_INIT_EVENT_TIMEOUT", "not-a-duration")
	cfg = readInitEventConfig()
	if cfg.timeout != initEventTimeoutDefault {
		t.Fatalf("битый таймаут дал %s, ожидался %s", cfg.timeout, initEventTimeoutDefault)
	}
}

// TestMCPPrecheckReasonReachesWaitResultOverToolsCall is the T3.84 acceptance
// test. A failed startup precheck installs its reason on the production
// wait_result service, so a real MCP tools/call returns precheck_failure with
// the reason instead of waiting silently (ADR 0041 §6).
func TestMCPPrecheckReasonReachesWaitResultOverToolsCall(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv("BEACON_INIT_EVENT", "control-plane")
	t.Setenv("BEACON_INIT_BYPASS", "")
	t.Setenv("BEACON_INIT_EVENT_TIMEOUT", "")

	previousServe := serveMCP
	t.Cleanup(func() {
		serveMCP = previousServe
		setPrecheckFailureReason("")
	})
	serveMCP = func(context.Context, mcp.Dependencies) error { return nil }

	errOut := new(bytes.Buffer)
	if code := mcpRunner(errOut); code == 0 {
		t.Fatalf("код выхода %d, ожидался не ноль; stderr %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), mcpPrecheckFailure) {
		t.Fatalf("в stderr нет precheck_failure: %q", errOut.String())
	}

	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := newMCPServer(deps).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-precheck-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}()

	result, err := clientSession.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_wait_result",
		Arguments: mcp.WaitResultInput{WatchID: "precheck-watch"},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_wait_result: %v", err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out mcp.WaitResultOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if out.Status != mcp.PrecheckFailureOutcome || out.Verdict != mcp.PrecheckFailureOutcome {
		t.Fatalf("status/verdict = %q/%q, ожидалось %q", out.Status, out.Verdict, mcp.PrecheckFailureOutcome)
	}
	check, ok := out.Checks["precheck"]
	if !ok {
		t.Fatalf("в ответе нет проверки precheck: %+v", out)
	}
	if !strings.Contains(check.Conclusion, "bypass") {
		t.Fatalf("в причине precheck нет bypass: %q", check.Conclusion)
	}
}

// TestMCPSuccessfulRunClearsPrecheckReason keeps the normal wait path intact:
// a run with a configured bypass clears the failure from an earlier run, so
// wait_result no longer returns precheck_failure (T3.41, T3.81, T3.82).
func TestMCPSuccessfulRunClearsPrecheckReason(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv("BEACON_INIT_EVENT", "")
	t.Setenv("BEACON_INIT_BYPASS", "")

	previousServe := serveMCP
	t.Cleanup(func() {
		serveMCP = previousServe
		setPrecheckFailureReason("")
	})
	serveMCP = func(context.Context, mcp.Dependencies) error { return nil }

	setPrecheckFailureReason("устаревшая причина")
	if code := mcpRunner(new(bytes.Buffer)); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0", code)
	}

	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()

	_, err = newMCPWaitResult(deps).Wait(context.Background(), "sub-A", mcp.WaitResultInput{}, mcp.CallerExit{})
	if err == nil || !strings.Contains(err.Error(), "watchID is required") {
		t.Fatalf("ожидалась обычная проверка входа, получено %v", err)
	}
}

// TestReadSyncCallConfig covers the unified sync-call threshold at the CLI
// boundary (ADR 0045 §2): 30s by default, capped at 120s, with a fallback to the
// default for a broken setting. The threshold is a time bound, not ADR 0026's
// delivery field, so no delivery value is read here.
func TestReadSyncCallConfig(t *testing.T) {
	t.Setenv("BEACON_SYNC_CALL_TIMEOUT", "")
	if got := readSyncCallConfig().after; got != syncCallTimeoutDefault {
		t.Fatalf("порог по умолчанию %s, ожидался %s", got, syncCallTimeoutDefault)
	}

	t.Setenv("BEACON_SYNC_CALL_TIMEOUT", "45s")
	if got := readSyncCallConfig().after; got != 45*time.Second {
		t.Fatalf("настроенный порог %s, ожидался 45s", got)
	}

	t.Setenv("BEACON_SYNC_CALL_TIMEOUT", " 7s ")
	if got := readSyncCallConfig().after; got != 7*time.Second {
		t.Fatalf("порог с пробелами %s, ожидался 7s", got)
	}

	// A value above the ceiling is clamped, the exact ceiling is accepted.
	t.Setenv("BEACON_SYNC_CALL_TIMEOUT", "10m")
	if got := readSyncCallConfig().after; got != syncCallTimeoutMax {
		t.Fatalf("порог 10m не ограничен максимумом: %s, ожидался %s", got, syncCallTimeoutMax)
	}
	t.Setenv("BEACON_SYNC_CALL_TIMEOUT", "120s")
	if got := readSyncCallConfig().after; got != syncCallTimeoutMax {
		t.Fatalf("порог ровно 120s дал %s, ожидался %s", got, syncCallTimeoutMax)
	}

	for _, raw := range []string{"not-a-duration", "0s", "-5s", "   "} {
		t.Setenv("BEACON_SYNC_CALL_TIMEOUT", raw)
		if got := readSyncCallConfig().after; got != syncCallTimeoutDefault {
			t.Fatalf("значение %q дало %s, ожидался дефолт %s", raw, got, syncCallTimeoutDefault)
		}
	}
}

// TestSyncCallThresholdWiredIntoProductionDeps proves the card's link: the
// composition root puts the single threshold into Dependencies.After, so the
// production server applies it to its long synchronous calls. Previously After
// stayed at zero, which disabled the ticket conversion entirely.
func TestSyncCallThresholdWiredIntoProductionDeps(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"по умолчанию", "", syncCallTimeoutDefault},
		{"настроенный", "5s", 5 * time.Second},
		{"ограничен максимумом", "1h", syncCallTimeoutMax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEACON_SYNC_CALL_TIMEOUT", tc.raw)
			deps, closeStore, err := openMCPDependenciesInDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = closeStore() }()
			if deps.After != tc.want {
				t.Fatalf("Dependencies.After = %s, ожидался %s", deps.After, tc.want)
			}
		})
	}
}

// TestSyncCallOverThresholdReturnsJobTicket is the acceptance test for ADR 0045
// §2. With a tiny threshold the production composition root answers a long
// synchronous beacon_get_result call with a ticket carrying a job id instead of
// holding the connection. A plain client further waits by that job id. The call
// is bounded by a context deadline so a hang fails the test rather than the CI.
func TestSyncCallOverThresholdReturnsJobTicket(t *testing.T) {
	t.Setenv("BEACON_SYNC_CALL_TIMEOUT", "1ns")

	deps, closeStore, err := openMCPDependenciesInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()

	const caller = "sync-ticket-session"
	w := domain.Watch{
		ID:        "sync-ticket-watch",
		Repo:      "refusedguy/beacon",
		PR:        42,
		HeadSHA:   "sync-ticket-head",
		Revision:  1,
		Status:    domain.StatusNotifying,
		Verdict:   domain.VerdictReady,
		CreatedAt: time.Now(),
	}
	if err := deps.Store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	obligationID := domain.ObligationKey(w.ID, w.Revision, caller)
	if err := deps.Store.SaveObligation(domain.Obligation{
		ID:             obligationID,
		WatchID:        w.ID,
		Revision:       w.Revision,
		SubscriberID:   caller,
		VerdictPointer: domain.VerdictReady,
	}); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := newMCPServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-sync-ticket-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}()

	result, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "beacon_get_result",
		Arguments: map[string]any{"watchID": w.ID},
		Meta:      mcpsdk.Meta{"ai.opencode/sessionID": caller},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_get_result: %v", err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out mcp.GetResultOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	if out.JobID != obligationID {
		t.Fatalf("jobID = %q, ожидался %q", out.JobID, obligationID)
	}
	if out.Instructions == "" {
		t.Fatalf("в тикете нет инструкций по ожиданию: %+v", out)
	}
	if out.ResultType != "" || out.TaskID != "" {
		t.Fatalf("обычный клиент не должен получать task-форму: %+v", out)
	}
}
