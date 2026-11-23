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
	"sort"
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

// ===== ACP capability negotiation (S3.005) =====
// An ACP agent declares optional features in the agentCapabilities object of
// its initialize result. Beacon treats every feature as optional: a feature
// the agent did not declare changes how a wake is delivered, never whether it
// is delivered. The adapter lives in wake, so the eight MCP core tools
// (ADR 0086/0094) are left untouched.

// ACPCapability is one named optional feature of an ACP agent. The dictionary
// is closed so a typo cannot silently disable a delivery mode.
type ACPCapability string

const (
	ACPCapSessionLoad ACPCapability = "loadSession"
	ACPCapFork        ACPCapability = "fork"
	ACPCapModels      ACPCapability = "models"
	ACPCapModes       ACPCapability = "modes"
	// ACPCapMCPTransport is parsed from an agent declaration. Beacon does not
	// advertise it in wake-only mode; see beaconClientCapabilities.
	ACPCapMCPTransport ACPCapability = "mcp"
)

// ACPCapabilities is the feature set negotiated with one agent. The zero
// value means "nothing declared", so callers must pick the most conservative
// mode. It is stored on the client after initialize.
type ACPCapabilities struct {
	SessionLoad  bool
	Fork         bool
	Models       bool
	Modes        bool
	MCPTransport bool
}

// Has reports whether which was negotiated. Unknown capabilities are absent.
func (c ACPCapabilities) Has(which ACPCapability) bool {
	switch which {
	case ACPCapSessionLoad:
		return c.SessionLoad
	case ACPCapFork:
		return c.Fork
	case ACPCapModels:
		return c.Models
	case ACPCapModes:
		return c.Modes
	case ACPCapMCPTransport:
		return c.MCPTransport
	default:
		return false
	}
}

// capabilityEnabled reads one declared flag. A boolean is used as-is; a
// structured value means the feature is advertised; an explicit false, null
// or a missing key means it is not.
func capabilityEnabled(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	var flag bool
	if err := json.Unmarshal(trimmed, &flag); err == nil {
		return flag
	}
	return trimmed[0] == '{' || trimmed[0] == '['
}

// parseACPCapabilities decodes agentCapabilities (or the serverCapabilities
// alias some harnesses use) from an initialize result. This is a boundary
// parse: a missing, malformed or partial object is not an error, it yields the
// zero value so a wake degrades instead of failing the handshake.
func parseACPCapabilities(result json.RawMessage) ACPCapabilities {
	var envelope struct {
		AgentCapabilities  map[string]json.RawMessage `json:"agentCapabilities"`
		ServerCapabilities map[string]json.RawMessage `json:"serverCapabilities"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return ACPCapabilities{}
	}
	declared := envelope.AgentCapabilities
	if declared == nil {
		declared = envelope.ServerCapabilities
	}
	var caps ACPCapabilities
	for name, raw := range declared {
		if !capabilityEnabled(raw) {
			continue
		}
		switch ACPCapability(name) {
		case ACPCapSessionLoad:
			caps.SessionLoad = true
		case ACPCapFork:
			caps.Fork = true
		case ACPCapModels:
			caps.Models = true
		case ACPCapModes:
			caps.Modes = true
		case ACPCapMCPTransport, "mcpServers":
			caps.MCPTransport = true
		}
	}
	return caps
}

// beaconClientCapabilities is the non-empty declaration Beacon sends at
// initialize. Wake delivery is text-only: Beacon inserts the wake through
// session/prompt and does not pass MCP server definitions to the agent. It
// advertises session resume, but not the mcp capability, so the declaration
// agrees with the empty mcpServers sent in session/new and session/load.
func beaconClientCapabilities() map[string]any {
	return map[string]any{
		string(ACPCapSessionLoad): true,
	}
}

// BeaconClientCapabilities reports the capabilities Beacon declares in an ACP
// initialize request. It is exported so a test can compare the declaration
// with the session arguments. A fresh map is returned on every call.
func BeaconClientCapabilities() map[string]any {
	return beaconClientCapabilities()
}

// ACPSessionNewParams builds the JSON-RPC params Beacon sends in session/new.
// Wake-only mode passes no MCP server definitions.
func ACPSessionNewParams(cwd string) map[string]any {
	return map[string]any{"cwd": cwd, "mcpServers": []any{}}
}

// ACPSessionLoadParams builds the JSON-RPC params Beacon sends in session/load.
// Wake-only mode passes no MCP server definitions.
func ACPSessionLoadParams(sessionID, cwd string) map[string]any {
	return map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{}}
}

// ACPDeliveryMode is how Beacon inserts a wake once capabilities are known.
type ACPDeliveryMode string

const (
	// ACPDeliveryResume reuses an existing session through session/load.
	ACPDeliveryResume ACPDeliveryMode = "resume"
	// ACPDeliveryNewRun starts a fresh session through session/new. It is the
	// degraded mode used whenever the agent cannot resume.
	ACPDeliveryNewRun ACPDeliveryMode = "newRun"
)

// ACPDeliveryPlan is the concrete set of choices for one wake. Every optional
// field is dropped unless the agent advertised it, so a thin agent never gets
// a request field it cannot handle.
type ACPDeliveryPlan struct {
	Mode       ACPDeliveryMode
	UseFork    bool
	SendModels bool
	SendModes  bool
	SendMCP    bool
}

// ACPDeliveryPolicy turns negotiated capabilities into a delivery plan.
// PreferResume asks to reuse an existing session; when session/load was not
// declared the plan degrades to a new run instead of failing. That downgrade
// is the acceptance rule of S3.005: a missing capability changes the mode, it
// does not crash the wake.
type ACPDeliveryPolicy struct {
	PreferResume bool
}

// Plan is pure: the same policy and capabilities always yield the same plan.
func (p ACPDeliveryPolicy) Plan(caps ACPCapabilities) ACPDeliveryPlan {
	mode := ACPDeliveryNewRun
	if p.PreferResume && caps.SessionLoad {
		mode = ACPDeliveryResume
	}
	return ACPDeliveryPlan{
		Mode:       mode,
		UseFork:    p.PreferResume && caps.Fork && caps.SessionLoad,
		SendModels: caps.Models,
		SendModes:  caps.Modes,
		SendMCP:    caps.MCPTransport,
	}
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
	// Done closes when the worker process exits. A spawn command uses it to
	// return without waiting for a signal when the runner has finished.
	Done() <-chan struct{}
	Close() error
}

// ===== ACP-раннеры (W10-A) =====
// ACPRuntimeCommand is the executable plus arguments that launch one ACP
// runner. The wire protocol is identical for every runner; only the command
// line changes.
type ACPRuntimeCommand struct {
	Command string
	Args    []string
}

// acpRuntimes are the default command lines for the runners Beacon can start.
// They are defaults, not hard-coded choices: cfg.Command and cfg.Args replace
// them so a harness can wrap a runner without changing Beacon.
var acpRuntimes = map[string]ACPRuntimeCommand{
	"opencode": {Command: "opencode", Args: []string{"acp"}},
	"claude":   {Command: "claude", Args: []string{"--acp"}},
	"codex":    {Command: "codex", Args: []string{"acp"}},
	"pi":       {Command: "pi", Args: []string{"--acp"}},
	"gemini":   {Command: "gemini", Args: []string{"--acp"}},
}

// ACPRuntimeConfig selects a runner and optionally overrides its command line.
type ACPRuntimeConfig struct {
	Runtime string   // logical runner name: opencode, claude, codex, pi, gemini
	Command string   // explicit executable; overrides Runtime when set
	Args    []string // explicit args; overrides the runner defaults when set
	Dir     string
	Env     []string
}

// SupportedACPRuntimes returns the configured runner names in stable order.
func SupportedACPRuntimes() []string {
	names := make([]string, 0, len(acpRuntimes))
	for name := range acpRuntimes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolveACPRuntimeCommand returns the command line for cfg. An explicit
// Command wins over the runner name; Args replace the runner defaults only
// when the caller supplied them.
func ResolveACPRuntimeCommand(cfg ACPRuntimeConfig) (string, []string, error) {
	if command := strings.TrimSpace(cfg.Command); command != "" {
		return command, append([]string(nil), cfg.Args...), nil
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Runtime))
	if name == "" {
		return "", nil, fmt.Errorf("wake: acp runtime: empty runtime and command")
	}
	built, ok := acpRuntimes[name]
	if !ok {
		return "", nil, fmt.Errorf("wake: acp runtime: unsupported %q (supported: %s)",
			cfg.Runtime, strings.Join(SupportedACPRuntimes(), ", "))
	}
	args := built.Args
	if len(cfg.Args) > 0 {
		args = cfg.Args
	}
	return built.Command, append([]string(nil), args...), nil
}

// NewWorkerRuntime builds a WorkerRuntime for cfg and starts the runner, so a
// caller can invoke Initialize immediately. The returned runtime owns the
// process and must be closed by the caller.
func NewWorkerRuntime(cfg ACPRuntimeConfig) (WorkerRuntime, error) {
	command, args, err := ResolveACPRuntimeCommand(cfg)
	if err != nil {
		return nil, err
	}
	client := NewPersistentACPClient(command, args...)
	client.Dir = cfg.Dir
	client.Env = cfg.Env
	if err := client.Start(); err != nil {
		return nil, fmt.Errorf("wake: acp runtime %q: %w", cfg.Runtime, err)
	}
	return client, nil
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
	caps      ACPCapabilities // negotiated at initialize; guarded by mu
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
	if c.started {
		return nil
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
	if c == nil {
		return fmt.Errorf("wake: acp client: nil")
	}
	if err := c.Start(); err != nil {
		return err
	}
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion":    persistentACPProtocolVersion,
		"clientCapabilities": beaconClientCapabilities(),
		"clientInfo":         map[string]any{"name": "beacon", "version": "0.0.0"},
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.caps = parseACPCapabilities(raw)
	c.mu.Unlock()
	return nil
}

// Capabilities returns the feature set negotiated at initialize. Before
// Initialize it is the zero value, which selects the most conservative mode.
func (c *PersistentACPClient) Capabilities() ACPCapabilities {
	if c == nil {
		return ACPCapabilities{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// DeliveryPlan resolves a policy against the negotiated capabilities. Callers
// use it before deciding whether to resume or start a new session.
func (c *PersistentACPClient) DeliveryPlan(policy ACPDeliveryPolicy) ACPDeliveryPlan {
	return policy.Plan(c.Capabilities())
}

// NewSession creates a worker session and returns its opaque id.
func (c *PersistentACPClient) NewSession(ctx context.Context, cwd string) (string, error) {
	raw, err := c.call(ctx, "session/new", ACPSessionNewParams(cwd))
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
	_, err := c.call(ctx, "session/load", ACPSessionLoadParams(sessionID, cwd))
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

// Done closes when the reader sees the worker connection close. A spawn
// command waits on it instead of on a signal when the runner exits on its own.
func (c *PersistentACPClient) Done() <-chan struct{} { return c.done }

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

// ===== ACP-сервер (T3.279) =====
// Beacon can also sit on the other side of ACP. A host that runs
// `rig acp serve --to` sees a command as one agent, so Beacon owns the
// session list and answers session/list itself. This section only adds
// inbound handling; the client types above stay unchanged.

// ACPServerSession is one active session an ACP host can see.
type ACPServerSession struct {
	ID    string `json:"sessionId"`
	Cwd   string `json:"cwd,omitempty"`
	Title string `json:"title,omitempty"`
}

// ACPServerError is a JSON-RPC error from an inbound request. A caller that
// frames ACP responses maps Code and Message into its error object.
type ACPServerError struct {
	Code    int
	Message string
}

func (e *ACPServerError) Error() string {
	return fmt.Sprintf("wake: acp server rpc %d: %s", e.Code, e.Message)
}

// ACPServer handles inbound ACP requests and keeps the active session list.
// Transport is only the outbound channel used for server-initiated requests
// such as elicitation. Framing stays with the caller: Handle takes a decoded
// ACPRequest and returns the result object to encode.
type ACPServer struct {
	Transport ACPTransport

	mu          sync.Mutex
	sessions    map[string]ACPServerSession
	nextSession int64
	nextID      int64
}

// NewACPServer returns a server with no active sessions.
func NewACPServer() *ACPServer {
	return &ACPServer{sessions: make(map[string]ACPServerSession)}
}

// AddSession registers a session so session/list can report it. The id is
// opaque to the host; Beacon generates it when it creates a session.
func (s *ACPServer) AddSession(id, cwd string) error {
	if s == nil {
		return fmt.Errorf("wake: acp server: nil")
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("wake: acp server: empty session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]ACPServerSession)
	}
	if _, exists := s.sessions[id]; exists {
		return fmt.Errorf("wake: acp server: duplicate session %q", id)
	}
	s.sessions[id] = ACPServerSession{ID: id, Cwd: cwd}
	return nil
}

// Sessions returns the active sessions in stable id order. A cancelled or
// completed turn does not remove a session: only the host ends it.
func (s *ACPServer) Sessions() []ACPServerSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ACPServerSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Handle dispatches one inbound ACP request. session/list reports the active
// sessions, session/new creates one and session/load resumes a known one.
// An unknown method or an unknown session is an ACPServerError, so the caller
// can frame a JSON-RPC error instead of a successful result.
func (s *ACPServer) Handle(_ context.Context, req ACPRequest) (json.RawMessage, error) {
	if s == nil {
		return nil, fmt.Errorf("wake: acp server: nil")
	}
	switch req.Method {
	case "session/list":
		return acpEncodeResult(map[string]any{"sessions": s.Sessions()})
	case "session/new":
		var params struct {
			Cwd string `json:"cwd"`
		}
		if err := acpDecodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.nextSession++
		id := fmt.Sprintf("sess_%d", s.nextSession)
		s.mu.Unlock()
		if err := s.AddSession(id, params.Cwd); err != nil {
			return nil, err
		}
		return acpEncodeResult(map[string]any{"sessionId": id})
	case "session/load":
		var params struct {
			SessionID string `json:"sessionId"`
		}
		if err := acpDecodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		s.mu.Lock()
		_, exists := s.sessions[params.SessionID]
		s.mu.Unlock()
		if !exists {
			return nil, &ACPServerError{Code: -32002, Message: "Unknown session"}
		}
		return acpEncodeResult(struct{}{})
	default:
		return nil, &ACPServerError{Code: -32601, Message: "Method not found: " + req.Method}
	}
}

func acpDecodeParams(params any, dst any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("wake: acp server: encode params: %w", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("wake: acp server: decode params: %w", err)
	}
	return nil
}

func acpEncodeResult(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("wake: acp server: encode result: %w", err)
	}
	return raw, nil
}

// ACPElicitationParams asks the host for one set of answers. The schema is
// passed through as raw JSON: Beacon does not interpret the requested fields.
type ACPElicitationParams struct {
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
}

// ACPElicitationResult is the host's answer. Action is accept, decline or
// cancel; content is only meaningful when the host accepted.
type ACPElicitationResult struct {
	Action  string                     `json:"action"`
	Content map[string]json.RawMessage `json:"content,omitempty"`
}

// Elicit sends one elicitation/create request to the ACP host and returns its
// answer. A decline or cancel is a valid answer, not a transport error. The
// host can ignore an unknown action only by failing, so Beacon rejects it.
func (s *ACPServer) Elicit(ctx context.Context, message string, schema json.RawMessage) (ACPElicitationResult, error) {
	if s == nil || s.Transport == nil {
		return ACPElicitationResult{}, fmt.Errorf("wake: acp server: no transport for elicitation")
	}
	if strings.TrimSpace(message) == "" {
		return ACPElicitationResult{}, fmt.Errorf("wake: acp server: empty elicitation message")
	}
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()

	raw, err := s.Transport.Call(ctx, ACPRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "elicitation/create",
		Params:  ACPElicitationParams{Message: message, RequestedSchema: schema},
	})
	if err != nil {
		return ACPElicitationResult{}, fmt.Errorf("wake: acp elicitation/create: %w", err)
	}
	var envelope struct {
		Result ACPElicitationResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ACPElicitationResult{}, fmt.Errorf("wake: acp elicitation/create: decode: %w", err)
	}
	if envelope.Error != nil {
		return ACPElicitationResult{}, &ACPServerError{Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	switch envelope.Result.Action {
	case "accept", "decline", "cancel":
		return envelope.Result, nil
	default:
		return ACPElicitationResult{}, fmt.Errorf("wake: acp elicitation/create: unknown action %q", envelope.Result.Action)
	}
}
