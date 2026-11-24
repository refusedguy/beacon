package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

func TestDoctorExitCode(t *testing.T) {
	cases := []struct {
		name   string
		checks []doctorCheck
		want   int
	}{
		{"all ok", []doctorCheck{{"a", checkOK, ""}, {"b", checkOK, ""}}, 0},
		{"warn only", []doctorCheck{{"a", checkOK, ""}, {"b", checkWarn, ""}}, 0},
		{"one fail", []doctorCheck{{"a", checkOK, ""}, {"b", checkFail, ""}}, 1},
		{"fail after warn", []doctorCheck{{"a", checkWarn, ""}, {"b", checkFail, ""}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doctorExitCode(tc.checks); got != tc.want {
				t.Fatalf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDoctorCheckLabels(t *testing.T) {
	if checkOK.label() != "[OK]" || checkWarn.label() != "[WARN]" || checkFail.label() != "[FAIL]" {
		t.Fatal("check labels do not match the doctor contract")
	}
	var out bytes.Buffer
	printDoctorChecks(&out, []doctorCheck{
		{"repo", checkOK, "ok"},
		{"token", checkWarn, "missing"},
		{"config", checkFail, "bad"},
	})
	text := out.String()
	for _, want := range []string{"[OK] repo: ok", "[WARN] token: missing", "[FAIL] config: bad"} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor output lacks %q: %s", want, text)
		}
	}
}

func TestDoctorMissingConfigFails(t *testing.T) {
	dir := initProject(t, "go.mod")
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)

	var out bytes.Buffer
	code := runDoctor(&out, "", &http.Client{})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "[FAIL] config:") {
		t.Errorf("missing config was not reported: %s", out.String())
	}
	if !strings.Contains(out.String(), "[FAIL] opencode.json:") {
		t.Errorf("missing opencode.json was not reported: %s", out.String())
	}
}

func TestDoctorFailsOnNonWorkerHarnessCommand(t *testing.T) {
	dir := initProject(t, "go.mod")
	writeDoctorProject(t, dir, config.Harness{Provider: "opencode", Endpoint: "http://127.0.0.1:9999", Command: "cli"})
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)

	var out bytes.Buffer
	code := runDoctor(&out, "", okClient())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "[FAIL] harness.command:") {
		t.Errorf("non-worker harness command was not reported: %s", out.String())
	}
}

func TestDoctorReportsHealthyProjectWithoutLeakingToken(t *testing.T) {
	const token = "doctor-leak-check-12345"
	dir := initProject(t, "go.mod")
	writeDoctorProject(t, dir, config.Harness{
		Provider: "opencode", Endpoint: "http://127.0.0.1:9999", Command: "worker",
		WorkerInstructions: config.DefaultWorkerInstructions,
	})
	t.Setenv("BEACON_TEST_SCM_TOKEN", token)
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)
	if err := mergeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := runDoctor(&out, "", okClient())
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	text := out.String()
	if strings.Contains(text, token) {
		t.Fatalf("doctor printed the SCM token: %s", text)
	}
	for _, want := range []string{"[OK] scm token:", "[OK] config:", "[OK] opencode.json:", "[OK] permissions:"} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor output lacks %q: %s", want, text)
		}
	}
}

func writeDoctorProject(t *testing.T, dir string, harness config.Harness) {
	t.Helper()
	cfg := config.Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   "main",
		PollInterval: "30s",
		Concurrency:  config.Concurrency{MaxInFlight: 10, MaxActiveAgents: 2},
		TaskSource:   config.TaskSource{Provider: "markdown", Dir: "docs/tasks", TokenEnv: "BEACON_TEST_SCM_TOKEN"},
		SCM:          config.SCM{Provider: "github", Owner: "acme", Repo: "demo", TokenEnv: "BEACON_TEST_SCM_TOKEN"},
		Harness:      harness,
		Merge:        config.Merge{RequiredChecks: []string{"test"}, MaxAttempts: 12, AutoRebase: true},
		OnBaseRed:    config.OnBaseRed{Mode: "spawn_fixer", MaxFixAttempts: 2},
	}
	path := filepath.Join(dir, ".beacon", "config.yaml")
	if err := config.WriteDefault(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
}

type okTransport struct{}

func (okTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
	}, nil
}

func okClient() *http.Client {
	return &http.Client{Transport: okTransport{}}
}
