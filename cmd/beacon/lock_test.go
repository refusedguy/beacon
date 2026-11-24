package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonStateLifecycle(t *testing.T) {
	dir := t.TempDir()
	tracker, err := startDaemonState(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, daemonStateFile)
	state := readStateFile(t, path)
	if state.PID != os.Getpid() {
		t.Fatalf("state pid = %d, want %d", state.PID, os.Getpid())
	}
	wantRoot, err := filepath.Abs(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if state.Root != wantRoot {
		t.Fatalf("state root = %q, want %q", state.Root, wantRoot)
	}
	if state.Heartbeat.IsZero() || state.StartedAt.IsZero() {
		t.Fatalf("state has no timestamps: %+v", state)
	}

	old := daemonState{PID: state.PID, StartedAt: state.StartedAt, Root: state.Root, Heartbeat: time.Now().UTC().Add(-time.Hour)}
	writeStateFile(t, path, old)
	if got := readDaemonStatus(dir); got.State != "stale" || got.HeartbeatAgeSecond < 3600 {
		t.Fatalf("stale state = %+v", got)
	}
	if err := tracker.refresh(); err != nil {
		t.Fatal(err)
	}
	if fresh := readStateFile(t, path); !fresh.Heartbeat.After(old.Heartbeat) {
		t.Fatalf("heartbeat did not advance: %s", fresh.Heartbeat)
	}

	tracker.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file still present after close: %v", err)
	}
}

func TestDaemonStatusReportsLiveness(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		state daemonState
		want  string
	}{
		{"live", daemonState{PID: os.Getpid(), Heartbeat: time.Now().UTC()}, "alive"},
		{"stale", daemonState{PID: os.Getpid(), Heartbeat: time.Now().UTC().Add(-time.Minute)}, "stale"},
		{"dead", daemonState{PID: 99999999, Heartbeat: time.Now().UTC()}, "dead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeStateFile(t, filepath.Join(dir, daemonStateFile), tc.state)
			got := readDaemonStatus(dir)
			if got.State != tc.want {
				t.Fatalf("state = %q, want %q", got.State, tc.want)
			}
			if got.HeartbeatAgeSecond < 0 {
				t.Fatalf("negative heartbeat age: %+v", got)
			}
		})
	}
}

func TestDaemonStatusWithoutState(t *testing.T) {
	got := readDaemonStatus(t.TempDir())
	if got.State != "absent" || got.Error != nil {
		t.Fatalf("status = %+v", got)
	}
}

func TestLockFailureLeavesVisibleError(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, err = acquireLock(dir)
	if err == nil {
		t.Fatal("second lock acquisition succeeded")
	}
	writeDaemonError(dir, "lock", err)

	got := readDaemonStatus(dir)
	if got.Error == nil || got.Error.Kind != "lock" {
		t.Fatalf("error record = %+v", got.Error)
	}
	if got.Error.At.IsZero() || !strings.Contains(got.Error.Message, "already running") {
		t.Fatalf("error record = %+v", got.Error)
	}
}

func TestStatusCommandReportsDaemonLiveness(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".beacon")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "state_db: " + filepath.Join(stateDir, "state.db") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeStateFile(t, filepath.Join(stateDir, daemonStateFile), daemonState{
		PID: os.Getpid(), StartedAt: now, Root: dir, Heartbeat: now,
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	cmd := newStatusCommand(&cfgPath)
	cmd.SetArgs([]string{"--daemon"})
	cmd.SetOut(io.Discard)
	runErr := cmd.Execute()
	w.Close()
	os.Stdout = stdout
	if runErr != nil {
		t.Fatal(runErr)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var got daemonStatus
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("status output %q: %v", out, err)
	}
	if got.State != "alive" || got.PID != os.Getpid() || got.HeartbeatAgeSecond < 0 {
		t.Fatalf("status = %+v", got)
	}
}

func TestNewDaemonClearsOldError(t *testing.T) {
	dir := t.TempDir()
	writeDaemonError(dir, "lock", errors.New("old failure"))
	tracker, err := startDaemonState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	if got := readDaemonStatus(dir); got.Error != nil {
		t.Fatalf("old error survived daemon start: %+v", got.Error)
	}
}

func readStateFile(t *testing.T, path string) daemonState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state daemonState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func writeStateFile(t *testing.T, path string, state daemonState) {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
