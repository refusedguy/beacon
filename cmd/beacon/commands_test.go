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
