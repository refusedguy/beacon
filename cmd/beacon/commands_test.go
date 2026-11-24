package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

type fakeControl struct {
	actions    []string
	ids        []core.RunID
	approveSHA string
	err        error
}

func (f *fakeControl) Approve(_ context.Context, id core.RunID, sha string) error {
	f.actions = append(f.actions, "approve")
	f.ids = append(f.ids, id)
	f.approveSHA = sha
	return f.err
}

func (f *fakeControl) Unblock(_ context.Context, id core.RunID) error {
	f.actions = append(f.actions, "unblock")
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeControl) Cancel(_ context.Context, id core.RunID) error {
	f.actions = append(f.actions, "cancel")
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeControl) Retry(_ context.Context, id core.RunID) error {
	f.actions = append(f.actions, "retry")
	f.ids = append(f.ids, id)
	return f.err
}

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

func TestApplyControlRoutesEveryAction(t *testing.T) {
	for _, action := range []string{"approve", "unblock", "cancel", "retry"} {
		t.Run(action, func(t *testing.T) {
			ctrl := &fakeControl{}
			if err := applyControl(context.Background(), ctrl, action, "r1", "abc"); err != nil {
				t.Fatal(err)
			}
			if len(ctrl.actions) != 1 || ctrl.actions[0] != action {
				t.Fatalf("actions = %v", ctrl.actions)
			}
			if len(ctrl.ids) != 1 || ctrl.ids[0] != "r1" {
				t.Fatalf("ids = %v", ctrl.ids)
			}
			if action == "approve" && ctrl.approveSHA != "abc" {
				t.Fatalf("approve sha = %q", ctrl.approveSHA)
			}
		})
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
		t.Fatal(err)
	}
	info := connectionInfo{Addr: srv.URL, Token: "tok", PID: os.Getpid()}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)

	configPath := ""
	cmd := newControlCommand(&configPath)
	cmd.SetArgs([]string{"--action", "approve", "--run-id", "r1", "--sha", "abc", "--json"})
	var execErr error
	out := captureStdout(t, func() { execErr = cmd.Execute() })
	if execErr != nil {
		t.Fatal(execErr)
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
	cmd := newControlCommand(&configPath)
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
	err := postControl(context.Background(), &connectionInfo{Addr: srv.URL, Token: "tok"}, "retry", "r1", "")
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error = %v, want status 409", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
