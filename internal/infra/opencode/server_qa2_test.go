package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// pidFakeProcess adds the optional PID half to the shared fake.
type pidFakeProcess struct {
	*fakeProcess
	pid int
}

func (p *pidFakeProcess) PID() int { return p.pid }

func TestQA2ReusesOwnRecordedServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	want := ServeState{URL: "http://127.0.0.1:51000", Password: "secret", PID: os.Getpid()}
	if err := WriteServeState(want); err != nil {
		t.Fatal(err)
	}

	called := false
	installStartServer(t, func(spawnRequest) (spawnedProcess, error) {
		called = true
		return nil, fmt.Errorf("спавн не должен вызываться")
	})

	cfg, err := EnsureServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if called {
		t.Fatal("второй сервер создан при живой записи")
	}
	if cfg.URL != want.URL || cfg.Password != want.Password {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestQA2RecordsSpawnedPID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	const wantPID = 4242
	var got spawnRequest
	installStartServer(t, func(req spawnRequest) (spawnedProcess, error) {
		got = req
		proc := startHealthServer(t, req, nil)
		return &pidFakeProcess{fakeProcess: proc, pid: wantPID}, nil
	})

	cfg, err := EnsureServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	wantURL := fmt.Sprintf("http://%s:%d", serveHost, got.port)
	if cfg.URL != wantURL {
		t.Fatalf("url = %q, want %q", cfg.URL, wantURL)
	}
	st, ok := ReadServeState()
	if !ok {
		t.Fatal("запись о сервере не сохранена")
	}
	if st.PID != wantPID || st.URL != wantURL || st.Password != got.password {
		t.Fatalf("state = %+v, want pid=%d url=%s", st, wantPID, wantURL)
	}
}

func TestQA2ParseServeCmdline(t *testing.T) {
	tests := []struct {
		name string
		args []string
		pid  int
		want bool
		port string
	}{
		{
			name: "serve with hostname and port",
			args: []string{"/usr/local/bin/opencode", "serve", "--hostname", "127.0.0.1", "--port", "4096", ""},
			pid:  10,
			want: true,
			port: "4096",
		},
		{
			name: "wrong binary",
			args: []string{"/usr/bin/node", "serve", "--hostname", "127.0.0.1", "--port", "4096"},
			pid:  11,
		},
		{
			name: "no hostname",
			args: []string{"opencode", "serve", "--port", "4096"},
			pid:  12,
		},
		{
			name: "other opencode command",
			args: []string{"opencode", "mcp", "--hostname", "127.0.0.1"},
			pid:  13,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proc, ok := parseServeCmdline(tc.pid, tc.args)
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			if proc.PID != tc.pid || proc.Port != tc.port {
				t.Fatalf("process = %+v, want pid=%d port=%s", proc, tc.pid, tc.port)
			}
		})
	}
}

func TestQA2ReadServicePID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := filepath.Join(dir, "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	body := fmt.Sprintf(`{"url":"http://127.0.0.1:4096","pid":%d}`, pid)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadServicePID(); got != pid {
		t.Fatalf("pid = %d, want %d", got, pid)
	}
	if err := os.WriteFile(path, []byte(`{"url":"http://127.0.0.1:4096"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadServicePID(); got != 0 {
		t.Fatalf("pid without registration = %d, want 0", got)
	}
}

func TestQA2TerminateRejectsInvalidPID(t *testing.T) {
	if err := TerminateServe(0); err == nil {
		t.Fatal("pid 0 должен отклоняться")
	}
	if !ProcessAlive(os.Getpid()) {
		t.Fatalf("текущий процесс pid=%s должен быть живым", strconv.Itoa(os.Getpid()))
	}
}
