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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/infra/config"
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
	h, err := newHarness(cfg, token, "", root)
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
	before := cw.live.Load()
	req := core.SpawnRequest{RunID: "run-1", Workspace: core.Workspace{Path: t.TempDir()}, Prompt: "work"}
	// Dispatch resolves the model from the live config on every spawn. The
	// harness-level model is only a startup fallback and stays fixed.
	req.ModelRef = cw.modelResolver("")
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
	req.ModelRef = cw.modelResolver("")
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
	if got := cw.live.Load().harness; got != before.harness {
		t.Fatal("model-only reload rebuilt the harness")
	}
}

// TestConfigReloadModelOnlySkipsHarnessAndBroadcast drives 60 fake ticks that
// toggle only the model. Each tick reads and hashes the file, but no harness or
// policy is rebuilt and no reload is broadcast. The harness pointer, policy
// pointer and relay signal channel stay identical throughout; generation
// advances once per tick so the model resolver cache invalidates.
func TestConfigReloadModelOnlySkipsHarnessAndBroadcast(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()
	signal := before.relay.reloadSignal()

	current := "model-a"
	for i := 0; i < 60; i++ {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		next := "model-b"
		if current == "model-b" {
			next = "model-a"
		}
		writeWatchFile(t, path, strings.Replace(string(body), "model: "+current, "model: "+next, 1))
		if !cw.reload() {
			t.Fatalf("tick %d: model-only change was not applied", i)
		}

		state := cw.live.Load()
		if state.cfg.Harness.Model != next {
			t.Fatalf("tick %d: model = %q, want %q", i, state.cfg.Harness.Model, next)
		}
		if state.harness != before.harness {
			t.Fatalf("tick %d: harness was rebuilt", i)
		}
		if state.policy != before.policy {
			t.Fatalf("tick %d: policy was rebuilt", i)
		}
		if state.relay != before.relay {
			t.Fatalf("tick %d: event relay was replaced", i)
		}
		if got := before.relay.reloadSignal(); got != signal {
			t.Fatalf("tick %d: reload was broadcast", i)
		}
		if state.generation != uint64(i+1) {
			t.Fatalf("tick %d: generation = %d, want %d", i, state.generation, i+1)
		}
		current = next
	}
}

// TestConfigReloadRebuildsOnRealChange confirms a non-model reloadable edit
// keeps the current behavior: the harness and policy are rebuilt and listeners
// are signaled to retarget their watchers.
func TestConfigReloadRebuildsOnRealChange(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()
	signal := before.relay.reloadSignal()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"merge:\n  required_checks: [build]\n")
	if !cw.reload() {
		t.Fatal("reloadable change was not applied")
	}

	state := cw.live.Load()
	if state.harness == before.harness {
		t.Fatal("real change did not rebuild the harness")
	}
	if state.policy == before.policy {
		t.Fatal("real change did not rebuild the policy")
	}
	if state.relay != before.relay {
		t.Fatal("real change replaced the event relay")
	}
	if got := before.relay.reloadSignal(); got == signal {
		t.Fatal("real change did not broadcast a reload")
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

// TestConfigReloadRetriesAfterInvalidEdit confirms an invalid edit does not
// consume the watch hash. The next tick must still see the corrected file as a
// change and apply it.
func TestConfigReloadRetriesAfterInvalidEdit(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()
	validHash := cw.hash

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	writeWatchFile(t, path, "harness: [unclosed\n")
	if cw.reload() {
		t.Fatal("invalid YAML was applied")
	}
	if got := cw.live.Load(); got != before {
		t.Fatal("invalid YAML replaced the live state")
	}
	if cw.hash != validHash {
		t.Fatal("invalid edit advanced the watch hash; the corrected file would be ignored")
	}

	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("corrected config was not applied on the next tick")
	}
	if got := cw.live.Load().cfg.Harness.Model; got != "model-b" {
		t.Fatalf("model after correction = %q, want model-b", got)
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
	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("reload did not apply the changed config")
	}

	if out := logs.String(); strings.Contains(out, token) {
		t.Fatalf("token leaked into logs: %s", out)
	}
}

func TestConfigReloadRejectsRestartFields(t *testing.T) {
	const token = "worker-token-do-not-log"
	logs := captureWatchLogs(t)
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", token)
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mixed := strings.Replace(string(body), "model: model-a", "model: model-b", 1) +
		"concurrency:\n  max_in_flight: 3\n"
	writeWatchFile(t, path, mixed)
	if cw.reload() {
		t.Fatal("reload with a restart-only field was applied")
	}
	if got := cw.live.Load(); got != before {
		t.Fatal("rejected reload replaced the live state")
	}
	if got := before.cfg.Harness.Model; got != "model-a" {
		t.Fatalf("stored model = %q, want model-a", got)
	}
	if got := before.cfg.Concurrency.MaxInFlight; got != 20 {
		t.Fatalf("stored max_in_flight = %d, want 20", got)
	}

	out := logs.String()
	if !strings.Contains(out, "concurrency.max_in_flight") {
		t.Fatalf("restart warning missing field name: %s", out)
	}
	if !strings.Contains(out, "restart beacon") {
		t.Fatalf("restart warning missing action: %s", out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("token leaked into logs: %s", out)
	}

	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("reload without restart fields was rejected")
	}
	if got := cw.live.Load().cfg.Harness.Model; got != "model-b" {
		t.Fatalf("model = %q, want model-b", got)
	}
}

func TestConfigReloadKeepsEventRelay(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, h := newTestWatcher(t, path)
	stable := h.Events()
	before := cw.live.Load().relay

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("reload did not apply the changed config")
	}
	if got := cw.live.Load().relay; got != before {
		t.Fatal("reload replaced the event relay")
	}
	if got := h.Events(); got != stable {
		t.Fatal("reload changed the consumer event channel")
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

// writeWatchRepo writes a complete config under <root>/.beacon and a CI
// workflow under <root>/.github/workflows. Required-check resolution derives
// the project root from the config path, so both files must share a root.
func writeWatchRepo(t *testing.T, mode, workflow string) (configPath, workflowPath string) {
	t.Helper()
	root := t.TempDir()
	beaconDir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(beaconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(beaconDir, "config.yaml")
	body := fmt.Sprintf("state_db: %s\nworkspace_dir: %s\nbase_branch: main\n"+
		"task_source:\n  provider: markdown\n  dir: %s\n"+
		"scm:\n  provider: github\n  owner: owner\n  repo: repo\n"+
		"harness:\n  provider: opencode\n  endpoint: http://127.0.0.1:1\n  model: model-a\n  token: worker-token\n"+
		"merge:\n  required_checks_mode: %s\n",
		filepath.Join(root, "state.db"), filepath.Join(root, "worktrees"), root, mode)
	writeWatchFile(t, configPath, body)

	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath = filepath.Join(dir, "ci.yml")
	writeWatchFile(t, workflowPath, workflow)
	return configPath, workflowPath
}

// TestConfigReloadWatchesWorkflowJobRename confirms a ci.yml job rename
// refreshes live required checks without a config edit or a harness restart.
// The watcher hashes workflow files alongside the config file, and only the
// policy changes in the live snapshot.
func TestConfigReloadWatchesWorkflowJobRename(t *testing.T) {
	const oldWorkflow = "name: CI\njobs:\n" +
		"  build:\n    runs-on: ubuntu-latest\n" +
		"  vet:\n    runs-on: ubuntu-latest\n"
	path, workflowPath := writeWatchRepo(t, config.RequiredChecksWorkflow, oldWorkflow)
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()
	if want := []string{"build", "vet"}; !reflect.DeepEqual(before.cfg.Merge.RequiredChecks, want) {
		t.Fatalf("initial required checks = %v, want %v", before.cfg.Merge.RequiredChecks, want)
	}
	p := &reloadablePolicy{live: &cw.live}
	signal := before.relay.reloadSignal()

	const newWorkflow = "name: CI\njobs:\n" +
		"  verify:\n    runs-on: ubuntu-latest\n" +
		"  test:\n    runs-on: ubuntu-latest\n"
	writeWatchFile(t, workflowPath, newWorkflow)
	if !cw.reload() {
		t.Fatal("workflow-only change did not apply")
	}

	state := cw.live.Load()
	if want := []string{"test", "verify"}; !reflect.DeepEqual(state.cfg.Merge.RequiredChecks, want) {
		t.Fatalf("required checks after rename = %v, want %v", state.cfg.Merge.RequiredChecks, want)
	}
	obs := core.Observation{PRState: "open", Checks: []core.CheckResult{
		{Name: "test", State: core.CheckSuccess},
		{Name: "verify", State: core.CheckSuccess},
	}}
	if got := p.Evaluate(obs); got.Verdict != core.VerdictMerge {
		t.Fatalf("policy verdict after rename = %+v, want merge", got)
	}
	if got := p.Evaluate(core.Observation{PRState: "open", Checks: []core.CheckResult{
		{Name: "build", State: core.CheckSuccess},
		{Name: "vet", State: core.CheckSuccess},
	}}); got.Verdict != core.VerdictWait || !strings.Contains(got.Reason, "verify") {
		t.Fatalf("old check names still satisfy the policy: %+v", got)
	}
	if state.harness != before.harness {
		t.Fatal("workflow-only change rebuilt the harness")
	}
	if state.policy == before.policy {
		t.Fatal("workflow-only change did not refresh the policy")
	}
	if state.relay != before.relay || before.relay.reloadSignal() != signal {
		t.Fatal("workflow-only change replaced the relay or broadcast a reload")
	}
	if state.generation != before.generation+1 {
		t.Fatalf("generation = %d, want %d", state.generation, before.generation+1)
	}
}

// TestConfigReloadWorkflowChangeLeavesManualChecks confirms that a workflow
// rename in manual mode leaves the explicit list and the live state untouched.
func TestConfigReloadWorkflowChangeLeavesManualChecks(t *testing.T) {
	const oldWorkflow = "name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n"
	path, workflowPath := writeWatchRepo(t, config.RequiredChecksManual, oldWorkflow)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, string(body)+"  required_checks: [deploy]\n")
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()
	signal := before.relay.reloadSignal()
	if want := []string{"deploy"}; !reflect.DeepEqual(before.cfg.Merge.RequiredChecks, want) {
		t.Fatalf("manual required checks = %v, want %v", before.cfg.Merge.RequiredChecks, want)
	}

	writeWatchFile(t, workflowPath, "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n")
	if cw.reload() {
		t.Fatal("workflow-only change in manual mode applied a reload")
	}
	state := cw.live.Load()
	if state != before {
		t.Fatal("workflow-only change in manual mode swapped live state")
	}
	if want := []string{"deploy"}; !reflect.DeepEqual(state.cfg.Merge.RequiredChecks, want) {
		t.Fatalf("manual required checks changed to %v, want %v", state.cfg.Merge.RequiredChecks, want)
	}
	if state.harness != before.harness || state.policy != before.policy || state.relay != before.relay {
		t.Fatal("workflow-only change in manual mode replaced a service")
	}
	if before.relay.reloadSignal() != signal {
		t.Fatal("workflow-only change in manual mode broadcast a reload")
	}
}

// TestConfigReloadWorkflowChangeKeepsPreviousOnInvalidConfig confirms the
// invalid-config guard still holds when a workflow edit also changes the
// watch hash.
func TestConfigReloadWorkflowChangeKeepsPreviousOnInvalidConfig(t *testing.T) {
	const oldWorkflow = "name: CI\njobs:\n  build:\n    runs-on: ubuntu-latest\n"
	path, workflowPath := writeWatchRepo(t, config.RequiredChecksWorkflow, oldWorkflow)
	cw, _ := newTestWatcher(t, path)
	before := cw.live.Load()

	writeWatchFile(t, workflowPath, "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n")
	writeWatchFile(t, path, "harness: [unclosed\n")
	if cw.reload() {
		t.Fatal("invalid config applied alongside a workflow change")
	}
	if got := cw.live.Load(); got != before {
		t.Fatal("invalid config replaced the live state")
	}
	if want := []string{"build"}; !reflect.DeepEqual(before.cfg.Merge.RequiredChecks, want) {
		t.Fatalf("live required checks = %v, want %v", before.cfg.Merge.RequiredChecks, want)
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

// relayHarness gives a reload test two distinct event channels and lets it
// observe which harness the proxy is watching.
type relayHarness struct {
	bareHarness
	events    chan core.HarnessEvent
	watchCall chan struct{}
}

func (h *relayHarness) Events() <-chan core.HarnessEvent { return h.events }

func (h *relayHarness) WatchIdle(ctx context.Context) {
	if h.watchCall != nil {
		select {
		case h.watchCall <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
}

func newRelayWatcher(t *testing.T, h core.AgentHarness) (*configWatcher, *reloadableHarness) {
	t.Helper()
	state := newLiveState(config.Config{}, h)
	cw := &configWatcher{interval: time.Hour}
	cw.live.Store(state)
	return cw, &reloadableHarness{live: &cw.live, events: h.Events()}
}

// swapRelayState mirrors the swap a successful reload performs, including the
// signal that retargets consumers at the new harness.
func swapRelayState(cw *configWatcher, h core.AgentHarness) {
	previous := cw.live.Load()
	next := newLiveState(config.Config{}, h)
	next.relay = previous.relay
	cw.live.Store(next)
	previous.relay.signalReload()
}

func waitHarnessEvent(t *testing.T, ch <-chan core.HarnessEvent, wantRunID string) {
	t.Helper()
	select {
	case ev := <-ch:
		if string(ev.RunID) != wantRunID {
			t.Fatalf("event run id = %q, want %q", ev.RunID, wantRunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for event %q", wantRunID)
	}
}

func waitWatchCall(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WatchIdle")
	}
}

// waitForwarderChange waits for the pump to replace the active forwarder after
// a reload and returns the new one.
func waitForwarderChange(t *testing.T, proxy *reloadableHarness, previous *eventForwarder) *eventForwarder {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := proxy.activeForwarder(); got != nil && got != previous {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("active event forwarder did not change after reload")
	return nil
}

func TestHarnessEventsUseStableChannelAfterReload(t *testing.T) {
	oldEvents := make(chan core.HarnessEvent, 4)
	newEvents := make(chan core.HarnessEvent, 4)
	cw, proxy := newRelayWatcher(t, &relayHarness{events: oldEvents})
	stable := proxy.Events()

	oldEvents <- core.HarnessEvent{RunID: "before", Kind: "idle"}
	waitHarnessEvent(t, stable, "before")

	swapRelayState(cw, &relayHarness{events: newEvents})
	newEvents <- core.HarnessEvent{RunID: "after", Kind: "idle"}
	waitHarnessEvent(t, stable, "after")
}

// TestHarnessEventsFromRetiredHarnessAreDropped confirms a reload stops the
// retired harness's forwarder. Events are notifications only, so dropping ones
// published by a retired harness does not lose state: consumers re-read state.
func TestHarnessEventsFromRetiredHarnessAreDropped(t *testing.T) {
	oldEvents := make(chan core.HarnessEvent, 4)
	newEvents := make(chan core.HarnessEvent, 4)
	cw, proxy := newRelayWatcher(t, &relayHarness{events: oldEvents})
	stable := proxy.Events()
	retired := proxy.activeForwarder()
	if retired == nil {
		t.Fatal("no initial forwarder")
	}

	swapRelayState(cw, &relayHarness{events: newEvents})
	waitForwarderChange(t, proxy, retired)

	oldEvents <- core.HarnessEvent{RunID: "retired", Kind: "idle"}
	select {
	case ev := <-stable:
		t.Fatalf("event from retired harness reached the relay: %q", ev.RunID)
	case <-time.After(100 * time.Millisecond):
	}

	newEvents <- core.HarnessEvent{RunID: "current", Kind: "idle"}
	waitHarnessEvent(t, stable, "current")
}

// TestReloadRetiresOldForwarders drives repeated reloads and confirms each
// retired forwarder exits and exactly one stays active. The forwarder count
// stays constant instead of growing with the number of reloads.
func TestReloadRetiresOldForwarders(t *testing.T) {
	events := make(chan core.HarnessEvent, 4)
	cw, proxy := newRelayWatcher(t, &relayHarness{events: events})
	proxy.Events()
	active := proxy.activeForwarder()
	if active == nil {
		t.Fatal("no initial forwarder")
	}

	const reloads = 30
	retired := make([]*eventForwarder, 0, reloads)
	for i := 0; i < reloads; i++ {
		next := make(chan core.HarnessEvent, 1)
		swapRelayState(cw, &relayHarness{events: next})
		retired = append(retired, active)
		active = waitForwarderChange(t, proxy, active)
	}
	if got := proxy.activeForwarder(); got != active {
		t.Fatal("more than one forwarder is active")
	}
	for i, f := range retired {
		select {
		case <-f.done:
		default:
			t.Fatalf("forwarder from reload %d is still running", i)
		}
	}
}

func TestWatchIdleFollowsReloadedHarness(t *testing.T) {
	oldWatch := make(chan struct{}, 2)
	newWatch := make(chan struct{}, 2)
	idleEvents := make(chan core.HarnessEvent, 1)
	cw, proxy := newRelayWatcher(t, &relayHarness{events: idleEvents, watchCall: oldWatch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.WatchIdle(ctx)
	}()

	waitWatchCall(t, oldWatch)
	swapRelayState(cw, &relayHarness{events: idleEvents, watchCall: newWatch})
	waitWatchCall(t, newWatch)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchIdle did not stop after context cancel")
	}
}

// TestModelResolverCapsCache confirms many distinct parents cannot grow the
// per-generation cache past its bound. The oldest parents are evicted first.
func TestModelResolverCapsCache(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	total := maxModelCacheEntries + 20
	for i := 0; i < total; i++ {
		cw.modelResolver(fmt.Sprintf("ses_%d", i))
	}

	cw.modelMu.Lock()
	defer cw.modelMu.Unlock()
	if len(cw.models) != maxModelCacheEntries {
		t.Fatalf("model cache size = %d, want %d", len(cw.models), maxModelCacheEntries)
	}
	if len(cw.modelOrder) != maxModelCacheEntries {
		t.Fatalf("model cache order size = %d, want %d", len(cw.modelOrder), maxModelCacheEntries)
	}
	if _, ok := cw.models["ses_0"]; ok {
		t.Fatal("oldest parent was not evicted")
	}
	if _, ok := cw.models[fmt.Sprintf("ses_%d", total-1)]; !ok {
		t.Fatal("newest parent is missing from the cache")
	}
}

// TestModelResolverCacheResetsOnGeneration confirms a model reload drops old
// resolutions so dispatch sees the newly configured model.
func TestModelResolverCacheResetsOnGeneration(t *testing.T) {
	path := watchConfig(t, "http://127.0.0.1:1", "model-a", "worker-token")
	cw, _ := newTestWatcher(t, path)
	ref := cw.modelResolver("ses_1")
	if ref.ID != "model-a" {
		t.Fatalf("initial model = %q, want model-a", ref.ID)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, path, strings.Replace(string(body), "model: model-a", "model: model-b", 1))
	if !cw.reload() {
		t.Fatal("reload did not apply the changed config")
	}

	if got := cw.modelResolver("ses_1"); got.ID != "model-b" {
		t.Fatalf("model after reload = %q, want model-b", got.ID)
	}
	cw.modelMu.Lock()
	cached := len(cw.models)
	cw.modelMu.Unlock()
	if cached != 1 {
		t.Fatalf("cache after generation change = %d, want 1", cached)
	}
}
