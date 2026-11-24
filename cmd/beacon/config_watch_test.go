package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/pkg/core"
)

// watchConfig writes a complete config with a single mutable model field.
func watchConfig(t *testing.T, endpoint, model, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf("state_db: %s\nworkspace_dir: %s\nbase_branch: main\n"+
		"task_source:\n  provider: markdown\n  dir: %s\n"+
		"scm:\n  provider: github\n  owner: owner\n  repo: repo\n"+
		"harness:\n  provider: opencode\n  endpoint: %s\n  model: %s\n  token: %q\n",
		filepath.Join(dir, "state.db"), filepath.Join(dir, "worktrees"), dir, endpoint, model, token)
	writeWatchFile(t, path, body)
	return path
}

func writeWatchFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestWatcher(t *testing.T, path string) (*configWatcher, *reloadableHarness) {
	t.Helper()
	t.Setenv("BEACON_MODEL", "")
	cfg, root, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, token := loadTokens(cfg)
	h, err := newHarness(cfg, token, "")
	if err != nil {
		t.Fatal(err)
	}
	cw := newConfigWatcher(path, root, "", newLiveState(cfg, h), time.Hour)
	wrapper := &reloadableHarness{live: &cw.live, events: h.Events()}
	return cw, wrapper
}

func recordModelServer(t *testing.T, models *[]map[string]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			_, _ = io.WriteString(w, `{"id":"ses_worker"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/model"):
			var body struct {
				Model map[string]string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			*models = append(*models, body.Model)
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"),
			r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/move"):
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func lastModel(models []map[string]string) string {
	if len(models) == 0 {
		return ""
	}
	return models[len(models)-1]["id"]
}

func TestConfigReloadUpdatesHarnessModelWithoutNewProcess(t *testing.T) {
	var mu sync.Mutex
	var models []map[string]string
	srv := recordModelServer(t, &models, &mu)
	defer srv.Close()

	path := watchConfig(t, srv.URL, "model-a", "worker-token")
	cw, h := newTestWatcher(t, path)
	req := core.SpawnRequest{RunID: "run-1", Workspace: core.Workspace{Path: t.TempDir()}, Prompt: "work"}
	if _, err := h.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("reload did not apply the changed config")
	}
	if _, err := h.Spawn(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if lastModel(models) != "model-b" {
		t.Fatalf("model after reload = %q, want model-b: %v", lastModel(models), models)
	}
	if len(models) != 2 {
		t.Fatalf("spawn calls = %d, want 2 (same process)", len(models))
	}
}

func TestConfigReloadKeepsPreviousModelOnInvalidConfig(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	for name, body := range map[string]string{
		"malformed": "harness: [unclosed\n",
		"empty":     "",
		"partial":   "harness:\n  model: model-b\n",
	} {
		t.Run(name, func(t *testing.T) {
			writeWatchFile(t, path, body)
			if cw.reload() {
				t.Fatal("invalid config was applied")
			}
			if got := cw.live.Load().cfg.Harness.Model; got != "model-a" {
				t.Fatalf("model = %q, want model-a", got)
			}
		})
	}
}

func captureWatchLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestConfigReloadNeverLogsToken(t *testing.T) {
	const token = "worker-super-secret-42"
	logs := captureWatchLogs(t)
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", token)
	cw, _ := newTestWatcher(t, path)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(string(body), "model: model-a", "model: model-b", 1)
	next += "concurrency:\n  max_in_flight: 3\n"
	writeWatchFile(t, path, next)
	if !cw.reload() {
		t.Fatal("reload did not apply the changed config")
	}

	out := logs.String()
	if strings.Contains(out, token) {
		t.Fatalf("token leaked into logs: %s", out)
	}
	if !strings.Contains(out, "concurrency.max_in_flight") {
		t.Fatalf("restart warning missing field name: %s", out)
	}
	if !strings.Contains(out, "restart beacon mcp") {
		t.Fatalf("restart warning missing action: %s", out)
	}
}

func TestConfigReloadUpdatesRequiredChecks(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	p := &reloadablePolicy{live: &cw.live}
	obs := core.Observation{PRState: "open"}
	if got := p.Evaluate(obs).Verdict; got == core.VerdictWait {
		t.Fatalf("initial verdict = %q, want no required-check gate", got)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"merge:\n  required_checks: [build]\n")
	if !cw.reload() {
		t.Fatal("reload did not apply the changed config")
	}
	decision := p.Evaluate(obs)
	if decision.Verdict != core.VerdictWait || decision.Criterion != "required_checks" {
		t.Fatalf("verdict = %+v, want required_checks wait", decision)
	}
}

func TestConfigReloadSkipsUnchangedFile(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cw.reload() {
		t.Fatal("unchanged file triggered a reload")
	}
	writeWatchFile(t, path, string(body))
	if cw.reload() {
		t.Fatal("rewritten identical file triggered a reload")
	}
	if cw.generations != 0 {
		t.Fatalf("generations = %d, want 0", cw.generations)
	}
	writeWatchFile(t, path, strings.Replace(string(body), "model-a", "model-b", 1))
	if !cw.reload() || cw.generations != 1 {
		t.Fatalf("changed file generations = %d, want 1", cw.generations)
	}
}

func TestMCPServerKeepsServingDuringReload(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	t.Setenv("BEACON_MODEL", "")
	w, err := wire(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	server := beaconmcp.NewServer(mcpServices(w))
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func() {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{Name: "beacon_status"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Content) == 0 {
			t.Fatal("empty beacon_status result")
		}
	}
	call()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, strings.Replace(string(body), "model-a", "model-b", 1))
	if !w.watcher.reload() {
		t.Fatal("reload did not apply the changed config")
	}
	call()
	if got := w.watcher.live.Load().cfg.Harness.Model; got != "model-b" {
		t.Fatalf("model = %q, want model-b", got)
	}
}
