package opencode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// serveHost is the loopback address of a spawned server. Beacon never
	// exposes it outside the local machine (ADR 0060 п3).
	serveHost = "127.0.0.1"

	// healthPath is an authenticated route that answers 200 only after the
	// API is live. GET /api/config works on opencode v2.3.0 (ADR 0060 п1).
	healthPath = "/api/config"

	// healthTimeout bounds the wait for a spawned server (ADR 0060 п3).
	healthTimeout = 30 * time.Second

	// healthPollInterval is the pause between health requests.
	healthPollInterval = 100 * time.Millisecond
)

// spawnRequest describes the child process to start. port comes from a short
// lived net.Listen on :0, so no port is hardcoded (ADR 0060 п3).
type spawnRequest struct {
	host     string
	port     int
	password string
}

// spawnedProcess is the handle to a started server. Done closes when the
// process exits. Kill stops it when readiness fails.
type spawnedProcess interface {
	Done() <-chan struct{}
	Kill() error
}

// serveRunner starts `opencode serve`. Tests replace it so no child process
// is spawned during a test.
type serveRunner func(req spawnRequest) (spawnedProcess, error)

// startServer is the injectable seam. Production uses startServeProcess.
var startServer serveRunner = startServeProcess

// healthDoer sends the health requests. A test can point it at a local
// httptest.Server without changing the readiness loop.
var healthDoer Doer = &http.Client{Timeout: 2 * time.Second}

// execProcess adapts *exec.Cmd to spawnedProcess.
type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *execProcess) Done() <-chan struct{} { return p.done }

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return errors.New("opencode serve: процесс не запущен")
	}
	return p.cmd.Process.Kill()
}

// startServeProcess starts opencode with the password in the environment.
// Output goes to io.Discard so a full pipe cannot block the child.
func startServeProcess(req spawnRequest) (spawnedProcess, error) {
	args := []string{"serve", "--hostname", req.host, "--port", strconv.Itoa(req.port)}
	cmd := exec.Command("opencode", args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "OPENCODE_SERVER_PASSWORD=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "OPENCODE_SERVER_PASSWORD="+req.password)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode serve: %w", err)
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// freePort reserves a loopback port and releases it. The caller then starts a
// server on that number.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", serveHost+":0")
	if err != nil {
		return 0, fmt.Errorf("не удалось выбрать свободный порт: %w", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("не удалось определить выбранный порт")
	}
	return addr.Port, nil
}

// randomPassword creates a password for the spawned server.
func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("не удалось сгенерировать пароль: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// healthReady performs one authenticated health request.
func healthReady(ctx context.Context, baseURL, password string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+healthPath, nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(authUser, password)
	resp, err := healthDoer.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// waitHealthy polls until the server answers, the process exits, ctx ends,
// or the 30 second budget runs out.
func waitHealthy(ctx context.Context, baseURL, password string, done <-chan struct{}) error {
	deadline := time.NewTimer(healthTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()

	for {
		if healthReady(ctx, baseURL, password) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("opencode serve завершился до готовности")
		case <-deadline.C:
			return fmt.Errorf("opencode serve не стал готов за %s", healthTimeout)
		case <-ticker.C:
		}
	}
}

// EnsureServer returns a Config for a live opencode HTTP API. An existing
// service.json wins. Without it, EnsureServer starts opencode serve on a free
// loopback port with a fresh password and waits for health (ADR 0060 п3).
func EnsureServer(ctx context.Context) (Config, error) {
	if cfg, err := ReadConfig(); err == nil {
		return cfg, nil
	}

	port, err := freePort()
	if err != nil {
		return Config{}, err
	}
	password, err := randomPassword()
	if err != nil {
		return Config{}, err
	}

	proc, err := startServer(spawnRequest{host: serveHost, port: port, password: password})
	if err != nil {
		return Config{}, err
	}
	baseURL := fmt.Sprintf("http://%s:%d", serveHost, port)
	if err := waitHealthy(ctx, baseURL, password, proc.Done()); err != nil {
		_ = proc.Kill()
		return Config{}, err
	}
	return Config{URL: baseURL, Password: password}, nil
}
