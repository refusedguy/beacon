package wake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/refusedguy/beacon/internal/domain"
)

// ACPContentBlock is one v2 prompt content block. A wake sends a single text
// block, so an event body can never arrive as an attachment or a command.
type ACPContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ACPPromptParams is the params object of an ACP session/prompt request. The
// sessionId is opaque to Beacon: the caller gets it from the host's _meta.
type ACPPromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []ACPContentBlock `json:"prompt"`
}

// ACPRequest is one JSON-RPC 2.0 request in the ACP wire format. A transport
// owns framing and delivery; ACPAdapter owns the wake semantics.
type ACPRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// ACPTransport carries one ACP request to an agent and returns the raw
// JSON-RPC response. Implementations must not reinterpret the method or the
// params: the wake envelope is built before the request reaches a transport.
type ACPTransport interface {
	Call(ctx context.Context, req ACPRequest) ([]byte, error)
}

// ACPAdapter implements domain.WakeDeliverer over Agent Client Protocol. Deliver
// sends Envelope(p), which contains only whitelisted identifiers, as the text
// of a session/prompt request. No harness name or command is baked into the
// adapter.
type ACPAdapter struct {
	Transport ACPTransport
	id        atomic.Int64
}

var _ domain.WakeDeliverer = (*ACPAdapter)(nil)

// Deliver inserts the wake envelope into an existing ACP session. ACP requires
// the session to be active, so the caller owns session/resume on a fresh
// connection; this prototype only sends the prompt.
func (a *ACPAdapter) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	if a == nil || a.Transport == nil {
		return fmt.Errorf("wake: acp: no transport")
	}
	if sessionID == "" {
		return fmt.Errorf("wake: acp: empty session id")
	}
	req := ACPRequest{
		JSONRPC: "2.0",
		ID:      a.id.Add(1),
		Method:  "session/prompt",
		Params: ACPPromptParams{
			SessionID: sessionID,
			Prompt: []ACPContentBlock{{
				Type: "text",
				Text: Envelope(p),
			}},
		},
	}
	raw, err := a.Transport.Call(ctx, req)
	if err != nil {
		return fmt.Errorf("wake: acp session/prompt: %w", err)
	}
	return verifyACPAck(raw)
}

// verifyACPAck treats a v2 insertion receipt as the delivery proof. A JSON-RPC
// error means the message was not inserted; a missing messageId means the
// response is not an ACP v2 prompt receipt.
func verifyACPAck(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("wake: acp: empty response")
	}
	var resp struct {
		Result struct {
			MessageID string `json:"messageId"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("wake: acp: decode response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("wake: acp: json-rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.MessageID == "" {
		return fmt.Errorf("wake: acp: response has no messageId")
	}
	return nil
}

// ACPStdioTransport talks to an ACP agent launched as a child process. Each
// Call starts one process, writes one newline-delimited JSON-RPC request and
// reads one response line. Exchange replaces the process in tests.
type ACPStdioTransport struct {
	Command  string
	Args     []string
	Dir      string
	Env      []string
	Exchange func(ctx context.Context, request []byte) ([]byte, error)
}

var _ ACPTransport = (*ACPStdioTransport)(nil)

func (t *ACPStdioTransport) Call(ctx context.Context, req ACPRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if t.Exchange != nil {
		return t.Exchange(ctx, raw)
	}
	if t.Command == "" {
		return nil, fmt.Errorf("wake: acp stdio: empty command")
	}
	cmd := exec.CommandContext(ctx, t.Command, t.Args...)
	cmd.Dir = t.Dir
	cmd.Env = t.Env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("wake: acp stdio: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("wake: acp stdio: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("wake: acp stdio: start: %w", err)
	}
	if _, err := stdin.Write(raw); err != nil {
		stdin.Close()
		cmd.Wait()
		return nil, fmt.Errorf("wake: acp stdio: write: %w", err)
	}
	if err := stdin.Close(); err != nil {
		cmd.Wait()
		return nil, fmt.Errorf("wake: acp stdio: close stdin: %w", err)
	}
	line, readErr := bufio.NewReader(stdout).ReadBytes('\n')
	waitErr := cmd.Wait()
	if readErr != nil && !(readErr == io.EOF && len(line) > 0) {
		return nil, fmt.Errorf("wake: acp stdio: read: %w", readErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("wake: acp stdio: wait: %w", waitErr)
	}
	return bytes.TrimSpace(line), nil
}

// ACPHTTPTransport posts ACP requests to an HTTP endpoint. The caller picks
// the URL and the client, including any credentials for that harness.
type ACPHTTPTransport struct {
	URL    string
	Client *http.Client
	Header http.Header
}

var _ ACPTransport = (*ACPHTTPTransport)(nil)

func (t *ACPHTTPTransport) Call(ctx context.Context, req ACPRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("wake: acp http: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for name, values := range t.Header {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("wake: acp http: post: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("wake: acp http: read: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("wake: acp http: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// ===== постоянный ACP-клиент (W2-B) =====
// ACPStdioTransport above starts one process per request and is kept for the
// older tests. A real worker stays alive across prompts, so this section adds
// one long-lived JSON-RPC 2.0 connection behind a transport-free interface.

const (
	persistentACPProtocolVersion = 1
	persistentACPUpdateBuffer    = 64
	persistentACPMaxFrameBytes   = 4 << 20
)

// WorkerUpdate is one inbound session/update notification, normalised so a
// caller never parses ACP JSON-RPC frames.
type WorkerUpdate struct {
	SessionID string
	Update    json.RawMessage
}

// WorkerRuntime is the clean interface Beacon uses for a long-lived worker.
// The implementation owns process lifetime and framing; callers see only
// opaque session ids and structured updates.
type WorkerRuntime interface {
	Initialize(ctx context.Context) error
	NewSession(ctx context.Context, cwd string) (string, error)
	LoadSession(ctx context.Context, sessionID, cwd string) error
	Prompt(ctx context.Context, sessionID, text string) error
	Cancel(ctx context.Context, sessionID string) error
	Updates() <-chan WorkerUpdate
	Close() error
}

type acpRPCReply struct {
	result json.RawMessage
	err    error
}

// PersistentACPClient owns one `opencode acp` process. Requests are newline
// delimited JSON-RPC 2.0; responses are routed by id to the waiting caller and
// session/update notifications go to a bounded channel (ADR 0061 п2: no replay
// of missed events, the caller re-reads state).
type PersistentACPClient struct {
	Command string
	Args    []string
	Dir     string
	Env     []string

	// Injected streams let tests drive the wire without spawning a process.
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd

	mu        sync.Mutex
	id        int64
	pending   map[int64]chan acpRPCReply
	writeMu   sync.Mutex
	updates   chan WorkerUpdate
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	started   bool
}

var _ WorkerRuntime = (*PersistentACPClient)(nil)

// NewPersistentACPClient builds a client that spawns command on Start.
func NewPersistentACPClient(command string, args ...string) *PersistentACPClient {
	c := newPersistentACPClient()
	c.Command = command
	c.Args = args
	return c
}

// newPersistentACPClient is shared by the process and injected-stream paths.
func newPersistentACPClient() *PersistentACPClient {
	return &PersistentACPClient{
		pending: make(map[int64]chan acpRPCReply),
		updates: make(chan WorkerUpdate, persistentACPUpdateBuffer),
		done:    make(chan struct{}),
	}
}

// Start launches the worker process, or attaches to injected streams. It must
// be called before any RPC method.
func (c *PersistentACPClient) Start() error {
	if c == nil {
		return fmt.Errorf("wake: acp client: nil")
	}
	if c.stdin != nil && c.stdout != nil {
		c.started = true
		go c.readLoop(c.stdout)
		return nil
	}
	if c.Command == "" {
		return fmt.Errorf("wake: acp client: empty command")
	}
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("wake: acp client: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("wake: acp client: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("wake: acp client: start: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.started = true
	go c.readLoop(stdout)
	return nil
}

// call sends one request and waits for its matching response or ctx. A
// cancelled caller never blocks the reader: the pending id is removed and a
// late reply is dropped.
func (c *PersistentACPClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c == nil || !c.started {
		return nil, fmt.Errorf("wake: acp client: not started")
	}
	c.mu.Lock()
	c.id++
	id := c.id
	reply := make(chan acpRPCReply, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	raw, err := json.Marshal(ACPRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	c.writeMu.Lock()
	_, err = c.stdin.Write(raw)
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("wake: acp write %s: %w", method, err)
	}
	select {
	case r := <-reply:
		if r.err != nil {
			return nil, r.err
		}
		return r.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("wake: acp client closed during %s", method)
	}
}

// readLoop demultiplexes one newline-delimited message at a time. A slow
// update consumer is bounded by dropping the oldest notification, so it can
// never stall prompt responses.
func (c *PersistentACPClient) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), persistentACPMaxFrameBytes)
	for scanner.Scan() {
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		line := append([]byte(nil), scanner.Bytes()...)
		if err := json.Unmarshal(line, &msg); err != nil {
			c.emitUpdate(WorkerUpdate{Update: json.RawMessage(fmt.Sprintf(`{"parseError":%q}`, err.Error()))})
			continue
		}
		if msg.ID != nil {
			c.mu.Lock()
			reply := c.pending[*msg.ID]
			c.mu.Unlock()
			if reply == nil {
				continue
			}
			if msg.Error != nil {
				select {
				case reply <- acpRPCReply{err: fmt.Errorf("wake: acp rpc %d: %s", msg.Error.Code, msg.Error.Message)}:
				default:
				}
				continue
			}
			select {
			case reply <- acpRPCReply{result: append([]byte(nil), msg.Result...)}:
			default:
			}
			continue
		}
		if msg.Method == "session/update" {
			var update struct {
				SessionID string          `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			}
			if err := json.Unmarshal(msg.Params, &update); err != nil {
				continue
			}
			c.emitUpdate(WorkerUpdate{
				SessionID: update.SessionID,
				Update:    append([]byte(nil), update.Update...),
			})
		}
	}
	c.mu.Lock()
	for _, reply := range c.pending {
		select {
		case reply <- acpRPCReply{err: fmt.Errorf("wake: acp connection closed")}:
		default:
		}
	}
	c.mu.Unlock()
	c.signalDone()
}

func (c *PersistentACPClient) emitUpdate(u WorkerUpdate) {
	select {
	case c.updates <- u:
	default:
		select {
		case <-c.updates:
		default:
		}
		select {
		case c.updates <- u:
		default:
		}
	}
}

func (c *PersistentACPClient) signalDone() {
	c.doneOnce.Do(func() { close(c.done) })
}

// Initialize performs the ACP handshake on the shared connection.
func (c *PersistentACPClient) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion":    persistentACPProtocolVersion,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "beacon", "version": "0.0.0"},
	})
	return err
}

// NewSession creates a worker session and returns its opaque id.
func (c *PersistentACPClient) NewSession(ctx context.Context, cwd string) (string, error) {
	raw, err := c.call(ctx, "session/new", map[string]any{
		"cwd": cwd, "mcpServers": []any{},
	})
	if err != nil {
		return "", err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("wake: acp session/new: decode: %w", err)
	}
	if result.SessionID == "" {
		return "", fmt.Errorf("wake: acp session/new: empty sessionId")
	}
	return result.SessionID, nil
}

// LoadSession resumes an existing worker session on the same connection.
func (c *PersistentACPClient) LoadSession(ctx context.Context, sessionID, cwd string) error {
	_, err := c.call(ctx, "session/load", map[string]any{
		"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{},
	})
	return err
}

// Prompt sends one text block into a loaded session.
func (c *PersistentACPClient) Prompt(ctx context.Context, sessionID, text string) error {
	_, err := c.call(ctx, "session/prompt", ACPPromptParams{
		SessionID: sessionID,
		Prompt:    []ACPContentBlock{{Type: "text", Text: text}},
	})
	return err
}

// Cancel asks the worker to stop the in-flight turn of sessionID.
func (c *PersistentACPClient) Cancel(ctx context.Context, sessionID string) error {
	_, err := c.call(ctx, "session/cancel", map[string]any{"sessionId": sessionID})
	return err
}

// Updates is the stream of inbound session/update notifications.
func (c *PersistentACPClient) Updates() <-chan WorkerUpdate { return c.updates }

// Close stops the worker and unblocks every waiting caller. It is idempotent.
func (c *PersistentACPClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.signalDone()
		if c.stdin != nil {
			err = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			if waitErr := c.cmd.Wait(); err == nil {
				err = waitErr
			}
		}
	})
	return err
}
