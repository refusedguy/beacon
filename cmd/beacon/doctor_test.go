package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/shared"
)

func TestPrintDoctorChecksRedactsDetails(t *testing.T) {
	const secret = "opencode:deadbeef0123456789"
	var out bytes.Buffer
	printDoctorChecks(&out, []doctorCheck{
		{name: "config", level: checkFail, detail: "cannot parse token " + secret},
	})
	text := out.String()
	if strings.Contains(text, secret) {
		t.Fatalf("doctor output leaked a secret: %q", text)
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("doctor output = %q, want a redaction marker", text)
	}
}

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

func TestDoctorCommandReturnsExitError(t *testing.T) {
	dir := initProject(t, "go.mod")
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)

	cmd := newDoctorCommand(new(string))
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	err := cmd.RunE(cmd, nil)
	var coder exitCoder
	if !errors.As(err, &coder) {
		t.Fatalf("RunE error = %v, want an error with an exit code", err)
	}
	if coder.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", coder.ExitCode())
	}
}

func TestDoctorCommandSucceedsWithoutExitError(t *testing.T) {
	dir := initProject(t, "go.mod")
	writeDoctorProject(t, dir, config.Harness{Provider: "opencode", Endpoint: "http://127.0.0.1:9999", Command: "worker"})
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)
	if err := mergeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}

	cmd := newDoctorCommand(new(string))
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	// RunE builds its own HTTP client; the probes only warn when unreachable.
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE error = %v, want nil", err)
	}
}

func TestDoctorHTTPCheckUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reached := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-r.Context().Done()
	}))
	defer srv.Close()
	defer cancel()

	done := make(chan doctorCheck, 1)
	go func() {
		done <- doctorHTTPCheck(ctx, "endpoint", srv.URL, "", bearerAuth, &http.Client{})
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not reach the server")
	}
	cancel()

	got := <-done
	if got.level != checkWarn || got.detail != "unreachable" {
		t.Fatalf("check = %v %q, want WARN unreachable", got.level, got.detail)
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
	code := runDoctor(context.Background(), &out, "", &http.Client{})
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
	code := runDoctor(context.Background(), &out, "", okClient())
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
	code := runDoctor(context.Background(), &out, "", okClient())
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

func TestDoctorRejectsEnabledWebhookWithoutSecret(t *testing.T) {
	dir := initProject(t, "go.mod")
	writeDoctorProjectWithWebhook(t, dir,
		config.Harness{Provider: "opencode", Endpoint: "http://127.0.0.1:9999", Command: "worker"},
		config.Webhook{Enabled: true})
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)

	var out bytes.Buffer
	code := runDoctor(context.Background(), &out, "", okClient())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "[FAIL] config:") {
		t.Fatalf("missing secret was not reported: %s", text)
	}
	if !strings.Contains(text, "webhook.secret") {
		t.Fatalf("error does not name the missing setting: %s", text)
	}
}

func TestDoctorAcceptsEnabledWebhookWithSecret(t *testing.T) {
	const secret = "doctor-webhook-secret-67890"
	dir := initProject(t, "go.mod")
	writeDoctorProjectWithWebhook(t, dir,
		config.Harness{Provider: "opencode", Endpoint: "http://127.0.0.1:9999", Command: "worker"},
		config.Webhook{Enabled: true, Secret: secret})
	t.Setenv("BEACON_PROJECT_ROOT", dir)
	t.Chdir(dir)
	if err := mergeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := runDoctor(context.Background(), &out, "", okClient())
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	text := out.String()
	if strings.Contains(text, secret) {
		t.Fatalf("doctor printed the webhook secret: %s", text)
	}
	if !strings.Contains(text, "[OK] webhook: enabled with a secret") {
		t.Fatalf("webhook check missing from output: %s", text)
	}
}

func TestDoctorWebhookCheck(t *testing.T) {
	cases := []struct {
		name   string
		cfg    config.Config
		level  checkLevel
		detail string
	}{
		{"disabled", config.Config{}, checkOK, "disabled"},
		{"enabled with secret", config.Config{Webhook: config.Webhook{Enabled: true, Secret: "s"}}, checkOK, "enabled with a secret"},
		{"enabled without secret", config.Config{Webhook: config.Webhook{Enabled: true}}, checkFail, "enabled without a secret; set webhook.secret or webhook.secret_env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := doctorWebhookCheck(tc.cfg)
			if got.level != tc.level || got.detail != tc.detail {
				t.Fatalf("check = %v %q, want %v %q", got.level, got.detail, tc.level, tc.detail)
			}
		})
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
	writeDoctorProjectWithWebhook(t, dir, harness, config.Webhook{})
}

func writeDoctorProjectWithWebhook(t *testing.T, dir string, harness config.Harness, webhook config.Webhook) {
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
		Webhook:      webhook,
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
		{"user password", "opencode:deadbeef", shared.ApplyAuth, "opencode", "deadbeef", ""},
		{"plain token", "plain-token", shared.ApplyAuth, "", "", "plain-token"},
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

			got := doctorHTTPCheck(context.Background(), "endpoint", srv.URL, tc.token, tc.auth, &http.Client{})
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

func doctorNetworkTestConfig(endpoint, baseURL string) config.Config {
	return config.Config{
		BaseBranch: "main",
		SCM: config.SCM{
			Provider: "gitlab",
			BaseURL:  baseURL,
			Owner:    "acme",
			Repo:     "demo",
			TokenEnv: "BEACON_DOCTOR_NETWORK_TOKEN",
		},
		Harness: config.Harness{
			Endpoint: endpoint,
			Command:  "worker",
			TokenEnv: "BEACON_DOCTOR_NETWORK_HARNESS_TOKEN",
		},
	}
}

func TestDoctorNetworkChecksRunConcurrently(t *testing.T) {
	received := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer unblock()

	done := make(chan []doctorCheck, 1)
	go func() {
		done <- doctorNetworkChecks(context.Background(), doctorNetworkTestConfig(srv.URL, srv.URL), doctorHTTPClient())
	}()

	// Sequential probes would block on the first request, so the second would
	// never reach the server before this deadline.
	for i := 0; i < 2; i++ {
		select {
		case <-received:
		case <-time.After(2 * time.Second):
			t.Fatal("network probes did not run concurrently")
		}
	}
	unblock()

	checks := <-done
	wantNames := []string{"opencode endpoint", "scm base branch"}
	if len(checks) != len(wantNames) {
		t.Fatalf("checks = %d, want %d", len(checks), len(wantNames))
	}
	for i, c := range checks {
		if c.name != wantNames[i] {
			t.Errorf("check %d name = %q, want %q", i, c.name, wantNames[i])
		}
		if c.level != checkOK || c.detail != "HTTP 200" {
			t.Errorf("%s check = %v %q, want OK HTTP 200", c.name, c.level, c.detail)
		}
	}
}

func TestDoctorNetworkChecksShareCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reached := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	done := make(chan []doctorCheck, 1)
	go func() {
		done <- doctorNetworkChecks(ctx, doctorNetworkTestConfig(srv.URL, srv.URL), doctorHTTPClient())
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-reached:
		case <-time.After(2 * time.Second):
			t.Fatal("network probe did not reach the server")
		}
	}
	cancel()

	checks := <-done
	if len(checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(checks))
	}
	for _, c := range checks {
		if c.level != checkWarn || c.detail != "unreachable" {
			t.Errorf("%s check = %v %q, want WARN unreachable", c.name, c.level, c.detail)
		}
	}
}

func TestDoctorNetworkChecksShareOneTimeoutWindow(t *testing.T) {
	reached := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	// No client timeout: the shared context in doctorNetworkChecks is the only
	// thing that can end these two hung requests.
	client := &http.Client{}
	type result struct {
		checks  []doctorCheck
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		checks := doctorNetworkChecks(context.Background(), doctorNetworkTestConfig(srv.URL, srv.URL), client)
		done <- result{checks, time.Since(start)}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(doctorHTTPTimeout + 3*time.Second):
		t.Fatal("hung network probes did not finish within one timeout window")
	}

	// Both probes must actually reach a hung endpoint, or the bound is vacuous.
	for i := 0; i < 2; i++ {
		select {
		case <-reached:
		default:
			t.Fatal("both probes did not reach a hung endpoint")
		}
	}

	if len(got.checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(got.checks))
	}
	for _, c := range got.checks {
		if c.level != checkWarn || c.detail != "unreachable" {
			t.Errorf("%s check = %v %q, want WARN unreachable", c.name, c.level, c.detail)
		}
	}

	// Two hung probes cost one timeout window, not two. Allow scheduling slack
	// but reject anything approaching 2*doctorHTTPTimeout.
	if got.elapsed > doctorHTTPTimeout+3*time.Second {
		t.Fatalf("two hung probes took %s, want within roughly one %s window", got.elapsed, doctorHTTPTimeout)
	}
}

func TestDoctorSCMCheckToleratesSlowServer(t *testing.T) {
	received := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(received)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer unblock()

	cfg := config.Config{
		BaseBranch: "main",
		SCM: config.SCM{
			Provider: "gitlab",
			BaseURL:  srv.URL,
			Owner:    "acme",
			Repo:     "demo",
			TokenEnv: "BEACON_DOCTOR_SLOW_TOKEN",
		},
	}
	done := make(chan []doctorCheck, 1)
	go func() { done <- doctorNetworkChecks(context.Background(), cfg, doctorHTTPClient()) }()

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("SCM check did not reach the server")
	}
	unblock()

	checks := <-done
	var scm doctorCheck
	for _, c := range checks {
		if c.name == "scm base branch" {
			scm = c
		}
	}
	if scm.level != checkOK || scm.detail != "HTTP 200" {
		t.Fatalf("scm base branch check = %v %q, want OK HTTP 200", scm.level, scm.detail)
	}
}

func TestDoctorSCMCheckTimeout(t *testing.T) {
	received := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(received)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer unblock()

	cfg := config.Config{
		BaseBranch: "main",
		SCM: config.SCM{
			Provider: "gitlab",
			BaseURL:  srv.URL,
			Owner:    "acme",
			Repo:     "demo",
			TokenEnv: "BEACON_DOCTOR_TIMEOUT_TOKEN",
		},
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	done := make(chan []doctorCheck, 1)
	go func() { done <- doctorNetworkChecks(context.Background(), cfg, client) }()

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("SCM check did not reach the server")
	}
	checks := <-done

	var scm doctorCheck
	for _, c := range checks {
		if c.name == "scm base branch" {
			scm = c
		}
	}
	if scm.level != checkWarn || scm.detail != "unreachable" {
		t.Fatalf("scm base branch check = %v %q, want WARN unreachable", scm.level, scm.detail)
	}
}

func TestDoctorSCMCheckUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	cfg := config.Config{
		BaseBranch: "main",
		SCM: config.SCM{
			Provider: "gitlab",
			BaseURL:  url,
			Owner:    "acme",
			Repo:     "demo",
			TokenEnv: "BEACON_DOCTOR_UNREACHABLE_TOKEN",
		},
	}
	checks := doctorNetworkChecks(context.Background(), cfg, doctorHTTPClient())

	var scm doctorCheck
	for _, c := range checks {
		if c.name == "scm base branch" {
			scm = c
		}
	}
	if scm.level != checkWarn || scm.detail != "unreachable" {
		t.Fatalf("scm base branch check = %v %q, want WARN unreachable", scm.level, scm.detail)
	}
}

func TestDoctorSCMCheckHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.Config{
		BaseBranch: "main",
		SCM: config.SCM{
			Provider: "gitlab",
			BaseURL:  srv.URL,
			Owner:    "acme",
			Repo:     "demo",
			TokenEnv: "BEACON_DOCTOR_HTTP_ERROR_TOKEN",
		},
	}
	checks := doctorNetworkChecks(context.Background(), cfg, doctorHTTPClient())

	var scm doctorCheck
	for _, c := range checks {
		if c.name == "scm base branch" {
			scm = c
		}
	}
	if scm.level != checkWarn || scm.detail != "HTTP 500" {
		t.Fatalf("scm base branch check = %v %q, want WARN HTTP 500", scm.level, scm.detail)
	}
}
