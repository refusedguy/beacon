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
