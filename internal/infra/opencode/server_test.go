package opencode

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProcess replaces the child handle in tests.
type fakeProcess struct {
	done     chan struct{}
	killed   chan struct{}
	killOnce sync.Once
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{done: make(chan struct{}), killed: make(chan struct{})}
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }

func (p *fakeProcess) Kill() error {
	p.killOnce.Do(func() {
		close(p.killed)
		close(p.done)
	})
	return nil
}

func installStartServer(t *testing.T, fn serveRunner) {
	t.Helper()
	old := startServer
	startServer = fn
	t.Cleanup(func() { startServer = old })
}

func writeServiceFile(t *testing.T, stateHome, body string) {
	t.Helper()
	path := filepath.Join(stateHome, "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startHealthServer binds a real HTTP server to the port that EnsureServer
// chose. The handler accepts only the password from the spawn request.
func startHealthServer(t *testing.T, req spawnRequest, onHealth func()) *fakeProcess {
	t.Helper()
	addr := net.JoinHostPort(req.host, strconv.Itoa(req.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("не удалось слушать %s: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthPath {
			http.NotFound(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != authUser || pass != req.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if onHealth != nil {
			onHealth()
		}
		w.WriteHeader(http.StatusOK)
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return newFakeProcess()
}

func TestEnsureServerUsesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeServiceFile(t, dir, `{"url":"http://127.0.0.1:49374","password":"secret"}`)

	called := false
	installStartServer(t, func(spawnRequest) (spawnedProcess, error) {
		called = true
		return nil, errors.New("спавн не должен вызываться")
	})

	cfg, err := EnsureServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if called {
		t.Fatal("spawn вызван при наличии service.json")
	}
	if cfg.URL != "http://127.0.0.1:49374" || cfg.Password != "secret" {
		t.Fatalf("неверный конфиг: %+v", cfg)
	}
}

func TestEnsureServerSpawnsOnMissingConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var got spawnRequest
	callCount := 0
	var healthCalls atomic.Int32

	installStartServer(t, func(req spawnRequest) (spawnedProcess, error) {
		callCount++
		got = req
		return startHealthServer(t, req, func() { healthCalls.Add(1) }), nil
	})

	cfg, err := EnsureServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("spawn вызван %d раз, ожидался 1", callCount)
	}
	if got.host != "127.0.0.1" || got.port == 0 || got.password == "" {
		t.Fatalf("неверный запрос на спавн: %+v", got)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d", got.port)
	if cfg.URL != wantURL || cfg.Password != got.password {
		t.Fatalf("неверный конфиг: %+v, ожидался url=%s", cfg, wantURL)
	}
	if n := healthCalls.Load(); n < 1 {
		t.Fatalf("health-запросов: %d, ожидался хотя бы 1", n)
	}
}

func TestEnsureServerFailsWhenProcessExits(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	proc := newFakeProcess()
	close(proc.done)
	installStartServer(t, func(spawnRequest) (spawnedProcess, error) {
		return proc, nil
	})

	start := time.Now()
	_, err := EnsureServer(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка при завершении процесса")
	}
	if !strings.Contains(err.Error(), "завершился до готовности") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("EnsureServer ждал дольше 5с: %v", time.Since(start))
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("процесс не убит после неудачи")
	}
}

func TestEnsureServerStopsOnContextCancel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var proc *fakeProcess
	installStartServer(t, func(req spawnRequest) (spawnedProcess, error) {
		// Сервер отвечает 503, поэтому готовность не наступает.
		ln, err := net.Listen("tcp", net.JoinHostPort(req.host, strconv.Itoa(req.port)))
		if err != nil {
			return nil, err
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		_ = srv.Listener.Close()
		srv.Listener = ln
		srv.Start()
		t.Cleanup(srv.Close)
		proc = newFakeProcess()
		return proc, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := EnsureServer(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидался context.DeadlineExceeded, получено %v", err)
	}
	if proc == nil {
		t.Fatal("spawn не вызван")
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("процесс не убит по отмене контекста")
	}
}
