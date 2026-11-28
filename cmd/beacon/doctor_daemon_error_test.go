package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/config"
)

// doctorDaemonConfig binds doctor's daemon check to the state records in dir.
func doctorDaemonConfig(dir string) config.Config {
	return config.Config{StateDB: filepath.Join(dir, "state.db")}
}

func TestDoctorDaemonCheckShowsRecordedError(t *testing.T) {
	dir := t.TempDir()
	const kind = "reconciler"
	const message = "reconciler exited during PR sync"
	writeDaemonError(dir, kind, errors.New(message))

	got := doctorDaemonCheck(doctorDaemonConfig(dir))
	if got.level != checkFail {
		t.Fatalf("level = %v, want FAIL (detail: %s)", got.level, got.detail)
	}
	for _, want := range []string{kind, message, "fix:", filepath.Join(dir, daemonErrorFile)} {
		if !strings.Contains(got.detail, want) {
			t.Errorf("detail %q does not contain %q", got.detail, want)
		}
	}
	if strings.Contains(got.detail, "no daemon error") {
		t.Fatalf("detail still claims there is no daemon error: %q", got.detail)
	}
}

func TestDoctorDaemonCheckNeverReportsCleanWithError(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, filepath.Join(dir, daemonStateFile), daemonState{
		PID: os.Getpid(), Heartbeat: time.Now().UTC(),
	})
	writeDaemonError(dir, "serve", errors.New("loopback listener closed"))

	got := doctorDaemonCheck(doctorDaemonConfig(dir))
	if got.level != checkFail {
		t.Fatalf("level = %v, want FAIL even with a live heartbeat (detail: %s)", got.level, got.detail)
	}
	if !strings.Contains(got.detail, "loopback listener closed") {
		t.Fatalf("detail hides the recorded error: %q", got.detail)
	}
}

func TestDoctorDaemonCheckKeepsLivenessChecks(t *testing.T) {
	cases := []struct {
		name      string
		state     *daemonState
		wantLevel checkLevel
		wantText  string
	}{
		{"alive", &daemonState{PID: os.Getpid(), Heartbeat: time.Now().UTC()}, checkOK, "no daemon error"},
		{"absent", nil, checkWarn, "no lock owner"},
		{"stale", &daemonState{PID: os.Getpid(), Heartbeat: time.Now().UTC().Add(-time.Hour)}, checkFail, "stale"},
		{"dead", &daemonState{PID: 99999999, Heartbeat: time.Now().UTC()}, checkFail, "dead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.state != nil {
				writeStateFile(t, filepath.Join(dir, daemonStateFile), *tc.state)
			}
			got := doctorDaemonCheck(doctorDaemonConfig(dir))
			if got.level != tc.wantLevel {
				t.Fatalf("level = %v, want %v (detail: %s)", got.level, tc.wantLevel, got.detail)
			}
			if !strings.Contains(got.detail, tc.wantText) {
				t.Fatalf("detail %q does not contain %q", got.detail, tc.wantText)
			}
		})
	}
}

func TestRunDoctorFailsWithRecordedDaemonError(t *testing.T) {
	dir := initProject(t, "go.mod")
	writeDoctorProject(t, dir, config.Harness{Provider: "opencode", Endpoint: "http://127.0.0.1:9999", Command: "worker"})
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)
	if err := mergeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, ".beacon", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeDaemonError(stateDir(cfg), "serve", errors.New("reconciler stopped after a fatal error"))

	var out bytes.Buffer
	code := runDoctor(context.Background(), &out, "", okClient())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "[FAIL] daemon:") {
		t.Fatalf("daemon failure missing from doctor output: %s", text)
	}
	if strings.Contains(text, "[OK] daemon:") {
		t.Fatalf("doctor reported a clean daemon while an error record exists: %s", text)
	}
	for _, want := range []string{"serve", "reconciler stopped after a fatal error", "fix:"} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor output does not contain %q: %s", want, text)
		}
	}
}
