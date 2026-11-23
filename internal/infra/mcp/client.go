// The MCP stdio process may not own Beacon state: every tool call is forwarded
// to the server's MCP HTTP endpoint (BS.42, ADR 0189; address from ADR 0194).
// This file holds the
// transport only. The address comes from <stateDir>/connection.json and the
// wire format is JSON-RPC 2.0, the same envelope the go-sdk speaks. A server
// that is down yields a typed error; no local store is consulted as a fallback,
// because a stale local answer would be a lie.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// remoteConnectionFileName sits next to beacon.db in the state directory.
	remoteConnectionFileName = "connection.json"
	// remoteMCPPath is the endpoint added by BS.43. Until that endpoint ships,
	// AttachRemoteTools is wired behind an explicit opt-in (see cmd/beacon/mcp.go).
	remoteMCPPath = "/mcp"
	// remoteMaxFrameBytes bounds one response so a broken server cannot grow a
	// call without limit.
	remoteMaxFrameBytes = 4 << 20
)

var (
	// ErrRemoteNoConnection means connection.json is absent. Remote mode cannot
	// dial a made-up address, so the caller reports a configuration error.
	ErrRemoteNoConnection = errors.New("mcp: connection.json missing")
	// ErrServerUnavailable covers a transport failure: no listener, a dropped
	// dial or a cancelled connection. Tools wrap this so the calling agent sees
	// an honest error instead of a cached result.
	ErrServerUnavailable = errors.New("mcp: server unavailable")
	// remoteReadFile is replaceable in tests.
	remoteReadFile = os.ReadFile
)

// RemoteEndpoint is the address the server published in connection.json.
type RemoteEndpoint struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// Validate rejects a record that cannot name a listener. An empty token is
// valid: a loopback server may run without authentication.
func (e RemoteEndpoint) Validate() error {
	if strings.TrimSpace(e.Host) == "" {
		return errors.New("mcp: empty host in connection.json")
	}
	if e.Port < 1 || e.Port > 65535 {
		return fmt.Errorf("mcp: port %d out of range 1..65535", e.Port)
	}
	return nil
}

// Address is the host:port pair of the endpoint.
func (e RemoteEndpoint) Address() string {
	return net.JoinHostPort(strings.TrimSpace(e.Host), strconv.Itoa(e.Port))
}

// LoadRemoteEndpoint reads and validates <dir>/connection.json. A missing file
// is a typed error so the composition root can explain that no server is
// configured; a corrupt file is never used as a dial target.
func LoadRemoteEndpoint(dir string) (RemoteEndpoint, error) {
	path := filepath.Join(dir, remoteConnectionFileName)
	data, err := remoteReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RemoteEndpoint{}, fmt.Errorf("%w: %s", ErrRemoteNoConnection, path)
		}
		return RemoteEndpoint{}, fmt.Errorf("mcp: read %s: %w", path, err)
	}
	var ep RemoteEndpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return RemoteEndpoint{}, fmt.Errorf("mcp: decode %s: %w", path, err)
	}
	if err := ep.Validate(); err != nil {
		return RemoteEndpoint{}, fmt.Errorf("mcp: %s: %w", path, err)
	}
	return ep, nil
}

// CSx-12. Источники конфигурации MCP. SDK задаёт strict_mcp_config и
// settingSources (types.py:2477-2482, 2759-2770): пустой список отключает
// файлы проекта и окружение. Здесь explicit всегда доверен, а env и project
// читаются только когда они названы в SettingSources.
type SettingSource string

const (
	SettingSourceExplicit SettingSource = "explicit"
	SettingSourceProject  SettingSource = "project"
	SettingSourceEnv      SettingSource = "env"

	remoteEndpointEnvHost  = "BEACON_MCP_HOST"
	remoteEndpointEnvPort  = "BEACON_MCP_PORT"
	remoteEndpointEnvToken = "BEACON_MCP_TOKEN"
)

// StrictRemoteConfig задаёт строгую изоляцию источников (CSx-12). StrictMcp
// включает режим, SettingSources задаёт список разрешённых источников.
// Endpoint из явной конфигурации имеет приоритет и не зависит от каталога
// проекта или окружения.
type StrictRemoteConfig struct {
	StrictMcp      bool
	SettingSources []SettingSource
	Endpoint       *RemoteEndpoint
	RejectLogger   func(source string)
}

func (c StrictRemoteConfig) allows(source SettingSource) bool {
	if !c.StrictMcp || source == SettingSourceExplicit {
		return true
	}
	for _, allowed := range c.SettingSources {
		if allowed == source {
			return true
		}
	}
	return false
}

func (c StrictRemoteConfig) reject(source SettingSource) {
	if c.RejectLogger != nil {
		c.RejectLogger(string(source))
	}
}

func remoteEndpointFromEnv() (RemoteEndpoint, bool) {
	host := strings.TrimSpace(os.Getenv(remoteEndpointEnvHost))
	if host == "" {
		return RemoteEndpoint{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(os.Getenv(remoteEndpointEnvPort)))
	if err != nil {
		return RemoteEndpoint{}, false
	}
	ep := RemoteEndpoint{Host: host, Port: port, Token: os.Getenv(remoteEndpointEnvToken)}
	if err := ep.Validate(); err != nil {
		return RemoteEndpoint{}, false
	}
	return ep, true
}

// HTTPDoer is the single HTTP seam. *http.Client implements it; tests pass a
// fake transport that records the request and returns a canned response.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RemoteClient forwards tools/call to the Beacon server. It holds no watches,
// no obligations and no store handle: the server owns all of that state.
type RemoteClient struct {
	baseURL string
	token   string
	doer    HTTPDoer
	nextID  atomic.Int64
}

// NewRemoteClient builds a client for ep. A nil doer uses a real *http.Client
// with no global timeout: a long beacon_wait_result is bounded by the caller's
// context, not by an arbitrary client-side deadline.
func NewRemoteClient(ep RemoteEndpoint, doer HTTPDoer) (*RemoteClient, error) {
	if err := ep.Validate(); err != nil {
		return nil, err
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &RemoteClient{
		baseURL: "http://" + ep.Address() + remoteMCPPath,
		token:   ep.Token,
		doer:    doer,
	}, nil
}

// Connection returns the endpoint this client was built for.
func (c *RemoteClient) Connection() RemoteEndpoint {
	if c == nil {
		return RemoteEndpoint{}
	}
	// The base URL is the only place the address is stored; parse it back so a
	// caller can report the exact endpoint without keeping a second copy.
	host, port, err := net.SplitHostPort(strings.TrimSuffix(strings.TrimPrefix(c.baseURL, "http://"), remoteMCPPath))
	if err != nil {
		return RemoteEndpoint{Token: c.token}
	}
	n, _ := strconv.Atoi(port)
	return RemoteEndpoint{Host: host, Port: n, Token: c.token}
}

type remoteRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type remoteRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type remoteRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *remoteRPCError `json:"error"`
}

// CallTool posts one tools/call to the server and returns its result verbatim.
// arguments and meta are forwarded as received, so the caller's session id and
// task dialect reach the server unchanged. A transport failure is wrapped in
// ErrServerUnavailable; a server-side JSON-RPC error keeps its own message.
func (c *RemoteClient) CallTool(ctx context.Context, name string, arguments, meta json.RawMessage) (mcpsdk.CallToolResult, error) {
	if c == nil {
		return mcpsdk.CallToolResult{}, errors.New("mcp: nil remote client")
	}
	if strings.TrimSpace(name) == "" {
		return mcpsdk.CallToolResult{}, errors.New("mcp: empty tool name")
	}
	params := map[string]any{"name": name}
	if remoteValidRaw(arguments) {
		params["arguments"] = arguments
	}
	if remoteValidRaw(meta) {
		params["_meta"] = meta
	}
	payload, err := json.Marshal(remoteRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		return mcpsdk.CallToolResult{}, fmt.Errorf("mcp: encode tools/call: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return mcpsdk.CallToolResult{}, fmt.Errorf("mcp: tools/call request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token := strings.TrimSpace(c.token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		return mcpsdk.CallToolResult{}, fmt.Errorf("%w: POST %s: %v", ErrServerUnavailable, remoteMCPPath, err)
	}
	data, err := remoteResponseData(resp)
	if err != nil {
		return mcpsdk.CallToolResult{}, err
	}

	var rpc remoteRPCResponse
	if err := json.Unmarshal(data, &rpc); err != nil {
		return mcpsdk.CallToolResult{}, fmt.Errorf("mcp: decode tools/call response: %w", err)
	}
	if rpc.Error != nil {
		detail := rpc.Error.Message
		if detail == "" {
			detail = "server rejected tools/call"
		}
		return mcpsdk.CallToolResult{}, fmt.Errorf("mcp: tools/call %s: %s (code %d)", name, detail, rpc.Error.Code)
	}
	if len(rpc.Result) == 0 || string(rpc.Result) == "null" {
		return mcpsdk.CallToolResult{}, fmt.Errorf("mcp: tools/call %s: empty result", name)
	}
	var result mcpsdk.CallToolResult
	if err := json.Unmarshal(rpc.Result, &result); err != nil {
		return mcpsdk.CallToolResult{}, fmt.Errorf("mcp: decode tools/call result: %w", err)
	}
	return result, nil
}

func remoteValidRaw(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && string(trimmed) != "null"
}

// remoteResponseData reads one bounded response and unwraps an SSE data frame
// when the server chooses event-stream mode. Both forms carry the same JSON-RPC
// envelope, so a caller sees one shape regardless of the server's response mode.
func remoteResponseData(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, remoteMaxFrameBytes))
	if err != nil {
		return nil, fmt.Errorf("mcp: read tools/call response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("mcp: tools/call: %s: %s", resp.Status, detail)
	}
	return remoteUnwrapSSE(body), nil
}

func remoteUnwrapSSE(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	isSSE := bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("data:")) ||
		bytes.HasPrefix(trimmed, []byte(":")) ||
		bytes.Contains(trimmed, []byte("\ndata:"))
	if !isSSE {
		return trimmed
	}
	var lines []string
	for _, line := range strings.Split(string(trimmed), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" {
				lines = append(lines, payload)
			}
		}
	}
	if len(lines) == 0 {
		return trimmed
	}
	return []byte(strings.Join(lines, "\n"))
}

// ---------------------------------------------------------------------------
// Tool adapters.
//
// Each contracted tool is re-registered with the same name, annotations and
// output schema as NewServerWithDeps, but its handler only calls RemoteClient.
// The instructions attached at server construction are untouched, so the agent
// keeps the same six-step cycle contract (BS.41, BS.44).
// ---------------------------------------------------------------------------

// remoteToolDescriptions and remoteInputSchemaFactories are read-only projections
// of the descriptor fields. The catalog tests in contract_test.go use them to
// check that every advertised name has a remote entry; production registration
// reads the descriptor fields directly. Deriving both tables means a new tool is
// added in one place, not in three hand-written name lists (BP-18).
var remoteToolDescriptions = func() map[string]string {
	out := make(map[string]string, len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		out[descriptor.Name] = descriptor.RemoteDescription
	}
	return out
}()

var remoteInputSchemaFactories = func() map[string]func() (*jsonschema.Schema, error) {
	out := make(map[string]func() (*jsonschema.Schema, error), len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		if descriptor.InputType != nil {
			out[descriptor.Name] = descriptor.InputType
		}
	}
	return out
}()

func remoteInputSchema(name string) *jsonschema.Schema {
	factory, ok := remoteInputSchemaFactories[name]
	if !ok {
		return &jsonschema.Schema{Type: "object"}
	}
	schema, err := factory()
	if err != nil || schema == nil || schema.Type != "object" {
		// AddTool panics on a non-object input schema. The real tools all take
		// objects, so a failed derivation is a broken optional schema, not a
		// reason to lose the tool.
		return &jsonschema.Schema{Type: "object"}
	}
	return schema
}

// AttachRemoteTools replaces every advertised local handler with a thin forward
// to client. It is idempotent: removing each name before adding it keeps repeated
// calls from registering a tool twice. CycleInstructions stay as constructed.
func AttachRemoteTools(s *mcpsdk.Server, client *RemoteClient) error {
	if s == nil {
		return errors.New("mcp: nil server")
	}
	if client == nil {
		return errors.New("mcp: nil remote client")
	}
	outputs, err := AllOutputSchemas()
	if err != nil || outputs == nil {
		// Same fallback as NewServerWithDeps: a broken optional schema must not
		// leave a contracted tool unregistered.
		outputs = map[string]*jsonschema.Schema{
			"beacon_list_running": {Type: "object"},
			"beacon_stats":        {Type: "object"},
		}
	}
	for _, descriptor := range toolDescriptors {
		name := descriptor.Name
		output := outputs[name]
		if output == nil {
			output = &jsonschema.Schema{Type: "object"}
		}
		annotations := toolAnnotations[name]
		s.RemoveTools(name)
		s.AddTool(&mcpsdk.Tool{
			Name:         name,
			Description:  descriptor.RemoteDescription,
			InputSchema:  remoteInputSchema(name),
			OutputSchema: output,
			Annotations:  &annotations,
		}, remoteToolHandler(client, name))
	}
	return nil
}

func remoteToolHandler(client *RemoteClient, name string) mcpsdk.ToolHandler {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var arguments, meta json.RawMessage
		if req != nil && req.Params != nil {
			arguments = req.Params.Arguments
			if len(req.Params.Meta) > 0 {
				if raw, err := json.Marshal(req.Params.Meta); err == nil {
					meta = raw
				}
			}
		}
		result, err := client.CallTool(ctx, name, arguments, meta)
		if err != nil {
			// A tool failure belongs in Content with IsError set, so the agent
			// can see why the call failed and correct its next turn.
			out := &mcpsdk.CallToolResult{}
			out.SetError(fmt.Errorf("mcp: %s через сервер: %w", name, err))
			return out, nil
		}
		return &result, nil
	}
}

// ---------------------------------------------------------------------------
// CSx-05. Tool transport selection.
//
// The remote client above is one implementation of ToolCaller. A local bridge
// is the other: it satisfies the same call seam with no HTTP doer, so a caller
// that depends on the interface does not change when the transport switches.
// ---------------------------------------------------------------------------

// ToolCaller is the narrow call seam shared by RemoteClient and InProcessBridge.
type ToolCaller interface {
	CallTool(ctx context.Context, name string, arguments, meta json.RawMessage) (mcpsdk.CallToolResult, error)
}

// InProcessBridge routes tools/call to a local handler. It is the in-process
// counterpart of RemoteClient: no base URL, no token, no HTTP doer, so a local
// call cannot reach the network or increment a remote request counter.
type InProcessBridge struct {
	handler InProcessHandler
}

// NewInProcessBridge builds a bridge over h. A nil handler is rejected so a
// misconfigured local path fails at construction, not on the first tool call.
func NewInProcessBridge(h InProcessHandler) (*InProcessBridge, error) {
	if h == nil {
		return nil, errors.New("mcp: nil in-process handler")
	}
	return &InProcessBridge{handler: h}, nil
}

// NewInProcessBridgeFunc wraps a function as a bridge handler.
func NewInProcessBridgeFunc(f InProcessHandleFunc) (*InProcessBridge, error) {
	return NewInProcessBridge(f)
}

// CallTool dispatches to the local handler. It mirrors RemoteClient.CallTool
// with the HTTP hop removed: name and raw arguments go straight to Handle.
func (b *InProcessBridge) CallTool(ctx context.Context, name string, arguments, meta json.RawMessage) (mcpsdk.CallToolResult, error) {
	if b == nil || b.handler == nil {
		return mcpsdk.CallToolResult{}, errors.New("mcp: nil in-process bridge")
	}
	if strings.TrimSpace(name) == "" {
		return mcpsdk.CallToolResult{}, errors.New("mcp: empty tool name")
	}
	return b.handler.Handle(ctx, name, arguments, meta)
}

// LocalMCPConfig selects the tool transport (CSx-05). Remote HTTP is the
// default: the zero value and Local=false both keep the AttachRemoteTools
// path. Local=true with a Handler opts into the in-process bridge.
type LocalMCPConfig struct {
	Local   bool
	Handler InProcessHandler
}

// AttachMCPTools wires exactly one transport for every advertised name. When
// cfg opts into the local path it registers the in-process handlers; otherwise
// it keeps the remote forwarders. Only one attach runs, so no name can carry a
// local and a remote handler at the same time.
func AttachMCPTools(s *mcpsdk.Server, client *RemoteClient, cfg LocalMCPConfig) error {
	if cfg.Local {
		bridge, err := NewInProcessBridge(cfg.Handler)
		if err != nil {
			return err
		}
		return AttachInProcessTools(s, bridge)
	}
	return AttachRemoteTools(s, client)
}
