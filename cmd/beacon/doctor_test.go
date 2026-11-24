package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/harnesses/opencode"
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

func TestDoctorFileShapeCheckMCPShapes(t *testing.T) {
	agents := map[string]any{
		"orchestrator": map[string]any{},
		"worker":       map[string]any{},
	}
	withAgents := func(extra map[string]any) map[string]any {
		doc := map[string]any{"agents": agents}
		for k, v := range extra {
			doc[k] = v
		}
		return doc
	}

	cases := []struct {
		name       string
		doc        map[string]any
		parent     map[string]any
		wantLevel  checkLevel
		wantDetail string
	}{
		{
			name: "root mcp.beacon",
			doc: withAgents(map[string]any{"mcp": map[string]any{
				"beacon": map[string]any{},
			}}),
			wantLevel:  checkOK,
			wantDetail: "agents and mcp.beacon present",
		},
		{
			name: "root mcp.servers.beacon",
			doc: withAgents(map[string]any{"mcp": map[string]any{
				"servers": map[string]any{"beacon": map[string]any{}},
			}}),
			wantLevel:  checkOK,
			wantDetail: "agents and mcp.servers.beacon present",
		},
		{
			name: "parent mcp.servers.beacon inherited",
			doc:  withAgents(nil),
			parent: map[string]any{"mcp": map[string]any{
				"servers": map[string]any{"beacon": map[string]any{}},
			}},
			wantLevel:  checkOK,
			wantDetail: "agents and mcp.servers.beacon present (workspace parent)",
		},
		{
			name:       "missing everywhere",
			doc:        withAgents(nil),
			wantLevel:  checkFail,
			wantDetail: "missing mcp.beacon",
		},
		{
			name: "agents missing",
			doc: map[string]any{"mcp": map[string]any{
				"beacon": map[string]any{},
			}},
			wantLevel:  checkFail,
			wantDetail: "missing orchestrator, worker",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "project")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.parent != nil {
				writeDoctorJSON(t, filepath.Join(base, "opencode.json"), tc.parent)
			}
			got := doctorFileShapeCheck(tc.doc, root)
			if got.level != tc.wantLevel {
				t.Errorf("level = %v, want %v (detail: %s)", got.level, tc.wantLevel, got.detail)
			}
			if got.detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", got.detail, tc.wantDetail)
			}
		})
	}
}

func writeDoctorJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
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

func TestDoctorHTTPCheckAuth(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		auth       func(*http.Request, string)
		wantUser   string
		wantPass   string
		wantBearer string
	}{
		{"opencode user password", "opencode:deadbeef", opencode.ApplyAuth, "opencode", "deadbeef", ""},
		{"opencode plain", "plain-token", opencode.ApplyAuth, "", "", "plain-token"},
		{"scm bearer", "ghp_secret", bearerAuth, "", "", "ghp_secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				mu.Lock()
				gotAuth = auth
				mu.Unlock()
				if tc.wantUser != "" {
					user, pass, ok := r.BasicAuth()
					if !ok || user != tc.wantUser || pass != tc.wantPass {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				} else if auth != "Bearer "+tc.wantBearer {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			got := doctorHTTPCheck("endpoint", srv.URL, tc.token, tc.auth, &http.Client{})
			if got.level != checkOK {
				t.Fatalf("level = %v, detail = %q; want OK", got.level, got.detail)
			}

			mu.Lock()
			defer mu.Unlock()
			if tc.wantUser != "" {
				if !strings.HasPrefix(gotAuth, "Basic ") || strings.Contains(gotAuth, "Bearer") {
					t.Fatalf("Authorization = %q, want Basic without Bearer", gotAuth)
				}
			} else if gotAuth != "Bearer "+tc.wantBearer {
				t.Fatalf("Authorization = %q, want Bearer %q", gotAuth, tc.wantBearer)
			}
		})
	}
}
