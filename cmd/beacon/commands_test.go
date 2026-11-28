package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestValidateControl(t *testing.T) {
	for _, action := range []string{"approve", "unblock", "cancel", "retry"} {
		if err := validateControl(action, "r1"); err != nil {
			t.Errorf("action %q: %v", action, err)
		}
	}
	for _, tc := range []struct{ action, runID string }{
		{"", "r1"}, {"approve", ""}, {"bogus", "r1"},
	} {
		if err := validateControl(tc.action, tc.runID); err == nil {
			t.Errorf("action %q run %q was accepted", tc.action, tc.runID)
		}
	}
}

func TestValidateControlRequiresNudgeMessage(t *testing.T) {
	if err := validateControl("nudge", "r1", "follow the checklist"); err != nil {
		t.Fatalf("validateControl(\"nudge\", \"r1\", \"follow the checklist\"): %v", err)
	}
	if err := validateControl("nudge", "r1"); err == nil {
		t.Fatal("nudge without a message was accepted")
	}
}

func TestControlCommandForwardsNudgeMessageToDaemon(t *testing.T) {
	var got controlRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(dir, 0o755): %v", err)
	}
	info := connectionInfo{Addr: srv.URL, Token: "tok", PID: os.Getpid()}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal(info): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(dir, \"connection.json\"), data, 0o600): %v", err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)

	configPath := ""
	cmd := newControlCommand(&configPath, wire)
	cmd.SetArgs([]string{"--action", "nudge", "--run-id", "r1", "--message", "check the tests"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute(): %v", err)
	}
	if got.Action != "nudge" || got.RunID != "r1" || got.Message != "check the tests" {
		t.Fatalf("forwarded = %+v", got)
	}
}

func TestDaemonSnapshotCarriesLivenessFields(t *testing.T) {
	got := daemonSnapshot(daemonStatus{
		State: "stale", PID: 42, HeartbeatAgeSecond: 55,
		Error: &daemonError{Message: "last tick failed"},
	})
	if got.State != "stale" || got.PID != 42 || got.HeartbeatAgeSeconds != 55 {
		t.Fatalf("daemon = %+v", got)
	}
	if got.Error != "last tick failed" {
		t.Fatalf("error = %q", got.Error)
	}
}

func TestControlCommandForwardsToDaemonAndPrintsJSON(t *testing.T) {
	var got controlRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/control" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(dir, 0o755): %v", err)
	}
	info := connectionInfo{Addr: srv.URL, Token: "tok", PID: os.Getpid()}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal(info): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(dir, \"connection.json\"), data, 0o600): %v", err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)

	configPath := ""
	cmd := newControlCommand(&configPath, wire)
	cmd.SetArgs([]string{"--action", "approve", "--run-id", "r1", "--sha", "abc", "--json"})
	var execErr error
	out := captureStdout(t, func() { execErr = cmd.Execute() })
	if execErr != nil {
		t.Fatalf("cmd.Execute(): %v", execErr)
	}
	if got.Action != "approve" || got.RunID != "r1" || got.SHA != "abc" {
		t.Fatalf("forwarded = %+v", got)
	}
	var result controlResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if !result.OK || result.Action != "approve" || result.RunID != "r1" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Fatalf("output = %q, want ok true", out)
	}
}

func TestRemoteClientControlPostsControlRequest(t *testing.T) {
	var got controlRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/control" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &remoteClient{info: &connectionInfo{Addr: srv.URL, Token: "tok"}, http: srv.Client()}
	if err := client.call(context.Background(), "retry", "r1", 7, "abc"); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Action != "retry" || got.RunID != "r1" || got.SHA != "abc" || got.PR != 7 {
		t.Fatalf("forwarded = %+v", got)
	}
}

func TestControlCommandRejectsInvalidAction(t *testing.T) {
	configPath := ""
	cmd := newControlCommand(&configPath, wire)
	cmd.SetArgs([]string{"--action", "bogus", "--run-id", "r1"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("invalid action was accepted")
	}
}

func TestPostControlReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	err := postControlWithPR(context.Background(), &connectionInfo{Addr: srv.URL, Token: "tok"}, "retry", "r1", "", 0)
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error = %v, want status 409", err)
	}
}

func TestStatusCommandKeepsFlagNamesAndDefaults(t *testing.T) {
	cmd := newStatusCommand(new(string))
	want := map[string]string{
		"recent":          "false",
		"snapshot":        "false",
		"recent-limit":    "0",
		"decisions-limit": "0",
		"daemon":          "false",
	}
	for name, def := range want {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("status flag %q is not registered", name)
			continue
		}
		if flag.DefValue != def {
			t.Errorf("status flag %q default = %q, want %q", name, flag.DefValue, def)
		}
	}
}

func TestControlCommandKeepsFlagNamesAndDefaults(t *testing.T) {
	cmd := newControlCommand(new(string), wire)
	want := map[string]string{
		"action":  "",
		"run-id":  "",
		"sha":     "",
		"pr":      "0",
		"message": "",
		"json":    "false",
	}
	for name, def := range want {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("control flag %q is not registered", name)
			continue
		}
		if flag.DefValue != def {
			t.Errorf("control flag %q default = %q, want %q", name, flag.DefValue, def)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll(r): %v", err)
	}
	return string(data)
}

func captureWire(stop error) (wireFn, *string) {
	got := new(string)
	fn := func(_ string, parent string) (*wiring, error) {
		*got = parent
		if stop != nil {
			return nil, stop
		}
		return &wiring{}, nil
	}
	return fn, got
}

func TestParentSessionFromEnv(t *testing.T) {
	t.Setenv("BEACON_PARENT_SESSION", "ses_cli_parent")
	if got := parentSessionFromEnv(); got != "ses_cli_parent" {
		t.Fatalf("parentSessionFromEnv() = %q", got)
	}
	t.Setenv("BEACON_PARENT_SESSION", "")
	if got := parentSessionFromEnv(); got != "" {
		t.Fatalf("parentSessionFromEnv() = %q, want empty", got)
	}
}

func TestWireCLIPassesEnvironmentParent(t *testing.T) {
	t.Setenv("BEACON_PARENT_SESSION", "ses_cli_parent")
	fn, got := captureWire(nil)
	if _, err := wireCLI(fn, "cfg.yaml"); err != nil {
		t.Fatalf("wireCLI(\"cfg.yaml\"): %v", err)
	}
	if *got != "ses_cli_parent" {
		t.Fatalf("wire parent = %q, want ses_cli_parent", *got)
	}
}

func TestWireCLIAcceptsEmptyEnvironment(t *testing.T) {
	t.Setenv("BEACON_PARENT_SESSION", "")
	fn, got := captureWire(nil)
	if _, err := wireCLI(fn, "cfg.yaml"); err != nil {
		t.Fatalf("wireCLI(\"cfg.yaml\"): %v", err)
	}
	if *got != "" {
		t.Fatalf("wire parent = %q, want empty", *got)
	}
}

func TestDispatchCommandWiresWithEnvironmentParent(t *testing.T) {
	for _, tc := range []struct{ name, env, want string }{
		{"environment parent", "ses_cli_parent", "ses_cli_parent"},
		{"empty environment", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEACON_PARENT_SESSION", tc.env)
			stop := errors.New("wire stopped")
			fn, got := captureWire(stop)
			configPath := ""
			cmd := newDispatchCommand(&configPath, fn)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"1"})
			if err := cmd.Execute(); !errors.Is(err, stop) {
				t.Fatalf("dispatch error = %v, want %v", err, stop)
			}
			if *got != tc.want {
				t.Fatalf("wire parent = %q, want %q", *got, tc.want)
			}
		})
	}
}

func TestControlFallbackWiresWithEnvironmentParent(t *testing.T) {
	for _, tc := range []struct{ name, env, want string }{
		{"environment parent", "ses_cli_parent", "ses_cli_parent"},
		{"empty environment", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEACON_PROJECT_ROOT", t.TempDir())
			t.Setenv("BEACON_PARENT_SESSION", tc.env)
			stop := errors.New("wire stopped")
			fn, got := captureWire(stop)
			configPath := ""
			cmd := newControlCommand(&configPath, fn)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"--action", "approve", "--run-id", "r1", "--sha", "abc"})
			if err := cmd.Execute(); !errors.Is(err, stop) {
				t.Fatalf("control error = %v, want %v", err, stop)
			}
			if *got != tc.want {
				t.Fatalf("wire parent = %q, want %q", *got, tc.want)
			}
		})
	}
}

func TestControlRefusalPrintsReasonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(core.NewWireControlRefusal(core.ControlApprove, core.RunID("r1"), "run is running"))
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(dir, 0o755): %v", err)
	}
	info := connectionInfo{Addr: srv.URL, Token: "tok", PID: os.Getpid()}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal(info): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
		t.Fatalf("os.WriteFile(connection.json): %v", err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)

	configPath := ""
	cmd := newControlCommand(&configPath, wire)
	cmd.SetArgs([]string{"--action", "approve", "--run-id", "r1", "--sha", "abc"})
	var execErr error
	out := captureStdout(t, func() { execErr = cmd.Execute() })
	if execErr != nil {
		t.Fatalf("cmd.Execute(): %v", execErr)
	}
	var result controlResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if result.OK || result.Action != "approve" || result.RunID != "r1" || result.Reason != "run is running" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(out, `"ok": false`) {
		t.Fatalf("output = %q, want ok false", out)
	}
}

type failingWorktreeRemover struct{ err error }

func (f failingWorktreeRemover) ForceRemove(context.Context, core.Workspace) error { return f.err }

type recordingWorktreeRemover struct{ paths []string }

func (r *recordingWorktreeRemover) ForceRemove(_ context.Context, ws core.Workspace) error {
	r.paths = append(r.paths, ws.Path)
	return nil
}

func gcFixture(t *testing.T) (*sqlite.Store, config.Config) {
	t.Helper()
	root := t.TempDir()
	store, err := sqlite.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceDir := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(filepath.Join(workspaceDir, "unused"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(workspaceDir/unused): %v", err)
	}
	return store, config.Config{WorkspaceDir: workspaceDir}
}

func TestGCReportsRemovalFailure(t *testing.T) {
	store, cfg := gcFixture(t)
	err := gcWorktrees(context.Background(), cfg, store, failingWorktreeRemover{err: errors.New("permission denied")})
	if err == nil {
		t.Fatal("gc returned nil after a failed removal")
	}
	for _, want := range []string{"removed 0", "failed 1", "permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestGCRemovesUnusedWorktreesSilently(t *testing.T) {
	store, cfg := gcFixture(t)
	remover := &recordingWorktreeRemover{}
	if err := gcWorktrees(context.Background(), cfg, store, remover); err != nil {
		t.Fatalf("gcWorktrees: %v", err)
	}
	if len(remover.paths) != 1 {
		t.Fatalf("removed paths = %v, want one directory", remover.paths)
	}
}

func TestWithWriteLockRefusesWhileHeld(t *testing.T) {
	dir := t.TempDir()
	lock, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	defer lock.Close()
	called := false
	err = withWriteLock(dir, func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("withWriteLock ran while the lock was held")
	}
	if called {
		t.Fatal("write ran without the lock")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error = %q, want lock contention", err)
	}
}

func TestDispatchRequiresWriteLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	defer lock.Close()
	store, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	fn := func(_, _ string) (*wiring, error) {
		return &wiring{cfg: config.Config{StateDB: filepath.Join(dir, "state.db")}, store: store}, nil
	}
	configPath := ""
	cmd := newDispatchCommand(&configPath, fn)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"1"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("dispatch ran while the daemon lock was held")
	}
}
