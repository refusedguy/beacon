package wake_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Smoke-тест живёт отдельно от адаптера ACP: он проверяет внешний бинарник,
// а не код Beacon. В CI опенкод не установлен, поэтому тест пропускается.
const (
	opencodeBinEnv     = "OPENCODE_BIN"
	acpProtocolVersion = 1
)

type acpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type acpMessage struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *acpRPCError    `json:"error"`
}

type acpRead struct {
	msg acpMessage
	err error
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type acpClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *lockedBuffer
	msgs   chan acpRead
	id     int
}

func startACPServer(t *testing.T, dir string) *acpClient {
	t.Helper()
	bin := os.Getenv(opencodeBinEnv)
	if bin == "" {
		found, err := exec.LookPath("opencode")
		if err != nil {
			t.Skipf("opencode не найден в PATH (%v); задайте %s для live smoke", err, opencodeBinEnv)
		}
		bin = found
	}

	cmd := exec.Command(bin, "acp")
	cmd.Dir = dir
	cmd.Env = acpEnv(dir)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin opencode acp: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout opencode acp: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск %s acp: %v", bin, err)
	}

	client := &acpClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		msgs:   make(chan acpRead, 64),
	}
	go client.readLoop()
	t.Cleanup(client.stop)
	return client
}

// acpEnv держит процесс в временных XDG-каталогах и убирает токены GitHub.
func acpEnv(root string) []string {
	blocked := map[string]bool{
		"GITHUB_TOKEN":      true,
		"GH_TOKEN":          true,
		"GITHUB_TOKEN_FILE": true,
	}
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			env = append(env, entry)
		}
	}
	return append(env,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
	)
}

// ACP stdio передаёт по одному JSON-RPC-сообщению на строку, без Content-Length.
func (c *acpClient) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var msg acpMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.msgs <- acpRead{err: fmt.Errorf("неверный JSON-RPC от opencode: %v: %s", err, clip(string(line), 200))}
			return
		}
		c.msgs <- acpRead{msg: msg}
	}
	if err := scanner.Err(); err != nil {
		c.msgs <- acpRead{err: fmt.Errorf("чтение stdout opencode: %w", err)}
	}
}

func (c *acpClient) stop() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

func (c *acpClient) request(t *testing.T, timeout time.Duration, method string, params any) json.RawMessage {
	t.Helper()
	c.id++
	id := c.id
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("%s: сериализация запроса: %v", method, err)
	}
	if _, err := fmt.Fprintf(c.stdin, "%s\n", raw); err != nil {
		t.Fatalf("%s: запись запроса: %v", method, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case read := <-c.msgs:
			if read.err != nil {
				t.Fatalf("%s: %v; stderr: %s", method, read.err, c.diagnostics())
			}
			if read.msg.ID == nil || *read.msg.ID != id {
				continue
			}
			if read.msg.Error != nil {
				t.Fatalf("%s: RPC-ошибка %d: %s; stderr: %s", method, read.msg.Error.Code, read.msg.Error.Message, c.diagnostics())
			}
			return read.msg.Result
		case <-timer.C:
			t.Fatalf("%s: нет ответа за %s; stderr: %s", method, timeout, c.diagnostics())
		}
	}
}

func (c *acpClient) diagnostics() string {
	out := c.stderr.String()
	if out == "" {
		return "<пусто>"
	}
	return clip(out, 500)
}

func clip(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= limit {
		return s
	}
	return s[:limit-1] + "…"
}

func TestOpenCodeACPHandshakeAndSession(t *testing.T) {
	root := t.TempDir()
	client := startACPServer(t, root)

	initRaw := client.request(t, 30*time.Second, "initialize", map[string]any{
		"protocolVersion":    acpProtocolVersion,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "beacon-acp-smoke", "version": "0.0.0"},
	})
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
		AgentInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := json.Unmarshal(initRaw, &init); err != nil {
		t.Fatalf("initialize: разбор результата: %v; результат: %s", err, clip(string(initRaw), 300))
	}
	if init.ProtocolVersion != acpProtocolVersion {
		t.Fatalf("initialize: protocolVersion=%d, ожидался %d", init.ProtocolVersion, acpProtocolVersion)
	}
	if init.AgentInfo.Name == "" || init.AgentInfo.Version == "" {
		t.Fatalf("initialize: agentInfo пустой: %+v", init.AgentInfo)
	}
	t.Logf("initialize: protocolVersion=%d, agent=%s %s, loadSession=%t",
		init.ProtocolVersion, init.AgentInfo.Name, init.AgentInfo.Version, init.AgentCapabilities.LoadSession)

	newRaw := client.request(t, 30*time.Second, "session/new", map[string]any{
		"cwd":        root,
		"mcpServers": []any{},
	})
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newRaw, &created); err != nil {
		t.Fatalf("session/new: разбор результата: %v; результат: %s", err, clip(string(newRaw), 300))
	}
	if created.SessionID == "" {
		t.Fatalf("session/new: пустой sessionId; результат: %s", clip(string(newRaw), 300))
	}
	t.Logf("session/new: sessionId=%s", created.SessionID)

	if !init.AgentCapabilities.LoadSession {
		t.Fatalf("initialize: loadSession=false, session/load по spec вызывать нельзя")
	}
	loadRaw := client.request(t, 30*time.Second, "session/load", map[string]any{
		"sessionId":  created.SessionID,
		"cwd":        root,
		"mcpServers": []any{},
	})
	if len(loadRaw) == 0 || string(loadRaw) == "null" {
		t.Fatalf("session/load: пустой результат для %s", created.SessionID)
	}
	t.Logf("session/load: принят, sessionId=%s", created.SessionID)
}
