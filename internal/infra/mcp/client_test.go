package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// remoteFakeDoer is the mock HTTP transport: every CallTool becomes one Do
// call that the test can inspect and answer without a listener.
type remoteFakeDoer struct {
	fn func(*http.Request) (*http.Response, error)
}

func (d remoteFakeDoer) Do(req *http.Request) (*http.Response, error) { return d.fn(req) }

func remoteJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func remoteTestEndpoint() RemoteEndpoint {
	return RemoteEndpoint{Host: "127.0.0.1", Port: 17432, Token: "secret"}
}

func remoteTestClient(t *testing.T, doer HTTPDoer) *RemoteClient {
	t.Helper()
	client, err := NewRemoteClient(remoteTestEndpoint(), doer)
	if err != nil {
		t.Fatalf("NewRemoteClient: %v", err)
	}
	return client
}

func TestRemoteEndpointValidationAndAddress(t *testing.T) {
	ep := remoteTestEndpoint()
	if err := ep.Validate(); err != nil {
		t.Fatalf("valid endpoint: %v", err)
	}
	if got := ep.Address(); got != "127.0.0.1:17432" {
		t.Fatalf("Address() = %q", got)
	}
	for _, bad := range []RemoteEndpoint{
		{Host: "", Port: 1},
		{Host: "127.0.0.1", Port: 0},
		{Host: "127.0.0.1", Port: 70000},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("%+v should not validate", bad)
		}
	}
}

func TestLoadRemoteEndpoint(t *testing.T) {
	previous := remoteReadFile
	t.Cleanup(func() { remoteReadFile = previous })

	dir := t.TempDir()
	_, err := LoadRemoteEndpoint(dir)
	if !errors.Is(err, ErrRemoteNoConnection) {
		t.Fatalf("missing file: got %v, want ErrRemoteNoConnection", err)
	}

	want := remoteTestEndpoint()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	remoteReadFile = func(string) ([]byte, error) { return data, nil }
	got, err := LoadRemoteEndpoint(dir)
	if err != nil || got != want {
		t.Fatalf("valid file: got %+v, %v", got, err)
	}

	remoteReadFile = func(string) ([]byte, error) { return []byte("{"), nil }
	if _, err := LoadRemoteEndpoint(dir); err == nil {
		t.Fatal("broken JSON was accepted")
	}
	remoteReadFile = func(string) ([]byte, error) { return []byte(`{"host":"","port":1}`), nil }
	if _, err := LoadRemoteEndpoint(dir); err == nil {
		t.Fatal("invalid endpoint was accepted")
	}
}

func TestRemoteClientCallToolForwardsEnvelope(t *testing.T) {
	var saw *http.Request
	var sawBody []byte
	client := remoteTestClient(t, remoteFakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		saw = req
		sawBody, _ = io.ReadAll(req.Body)
		return remoteJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"status":"ready","verdict":"ready","revision":3},"isError":false}}`), nil
	}})

	result, err := client.CallTool(
		context.Background(),
		"beacon_get_result",
		json.RawMessage(`{"watchID":"w1"}`),
		json.RawMessage(`{"ai.opencode/sessionID":"ses_1"}`),
	)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if saw == nil {
		t.Fatal("no HTTP request was made")
	}
	if saw.Method != http.MethodPost || saw.URL.Path != remoteMCPPath {
		t.Fatalf("request = %s %s", saw.Method, saw.URL.Path)
	}
	if got := saw.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := saw.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}

	var envelope remoteRPCRequest
	if err := json.Unmarshal(sawBody, &envelope); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if envelope.JSONRPC != "2.0" || envelope.Method != "tools/call" || envelope.ID == 0 {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Params["name"] != "beacon_get_result" {
		t.Fatalf("params = %+v", envelope.Params)
	}
	if !bytes.Contains(sawBody, []byte(`"watchID":"w1"`)) || !bytes.Contains(sawBody, []byte(`"ai.opencode/sessionID":"ses_1"`)) {
		t.Fatalf("arguments or _meta lost: %s", sawBody)
	}

	if result.IsError {
		t.Fatal("successful result marked as error")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"verdict":"ready"`)) {
		t.Fatalf("structured content = %s", raw)
	}
}

func TestRemoteClientHonestErrors(t *testing.T) {
	t.Run("transport down", func(t *testing.T) {
		client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: connection refused")
		}})
		_, err := client.CallTool(context.Background(), "beacon_stats", nil, nil)
		if !errors.Is(err, ErrServerUnavailable) {
			t.Fatalf("got %v, want ErrServerUnavailable", err)
		}
	})
	t.Run("http status", func(t *testing.T) {
		client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return remoteJSONResponse(http.StatusServiceUnavailable, "server is restarting"), nil
		}})
		_, err := client.CallTool(context.Background(), "beacon_stats", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "restarting") {
			t.Fatalf("got %v, want status and reason", err)
		}
	})
	t.Run("json rpc error", func(t *testing.T) {
		client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return remoteJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"tool not found"}}`), nil
		}})
		_, err := client.CallTool(context.Background(), "beacon_stats", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "tool not found") || !strings.Contains(err.Error(), "-32601") {
			t.Fatalf("got %v, want rpc error", err)
		}
	})
	t.Run("sse response", func(t *testing.T) {
		client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
			body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1," +
				"\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"structuredContent\":{\"ok\":true}}}\n\n"
			return remoteJSONResponse(http.StatusOK, body), nil
		}})
		result, err := client.CallTool(context.Background(), "beacon_ack", nil, nil)
		if err != nil {
			t.Fatalf("SSE response: %v", err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		if !bytes.Contains(raw, []byte(`"ok":true`)) {
			t.Fatalf("structured content = %s", raw)
		}
	})
}

// connectRemoteTestServer drives the composed server over an in-memory MCP
// transport, so a test exercises the real tools/call path without stdio.
func connectRemoteTestServer(t *testing.T, s *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := s.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "remote-client-test", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func remoteErrorText(result *mcpsdk.CallToolResult) string {
	var out strings.Builder
	for _, c := range result.Content {
		if text, ok := c.(*mcpsdk.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

func TestAttachRemoteToolsReportsServerStoppedHonestly(t *testing.T) {
	client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	}})
	s := NewServer()
	if err := AttachRemoteTools(s, client); err != nil {
		t.Fatalf("AttachRemoteTools: %v", err)
	}
	session := connectRemoteTestServer(t, s)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_stats",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call returned a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("stopped server produced a successful tool result")
	}
	text := remoteErrorText(result)
	if !strings.Contains(text, "server unavailable") || !strings.Contains(text, "beacon_stats") {
		t.Fatalf("honest error missing: %q", text)
	}
}

func TestAttachRemoteToolsPreservesContract(t *testing.T) {
	client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return remoteJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"content":[],"structuredContent":{},"isError":false}}`), nil
	}})
	s := NewServer()
	for i := 0; i < 2; i++ {
		if err := AttachRemoteTools(s, client); err != nil {
			t.Fatalf("AttachRemoteTools #%d: %v", i, err)
		}
	}
	session := connectRemoteTestServer(t, s)

	tools, _ := listAllTools(t, session)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		if tool.OutputSchema == nil {
			t.Errorf("%s: output schema lost", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s: description lost", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s: input schema lost", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("%s: annotations lost", tool.Name)
			continue
		}
		want := toolAnnotations[tool.Name]
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != *want.DestructiveHint {
			t.Errorf("%s: destructive hint changed", tool.Name)
		}
		if tool.Annotations.ReadOnlyHint != want.ReadOnlyHint {
			t.Errorf("%s: read-only hint changed", tool.Name)
		}
	}
	wantNames := slices.Clone(ToolNames)
	slices.Sort(wantNames)
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tool names = %v, want %v", names, wantNames)
	}

	init := session.InitializeResult()
	if init == nil || init.Instructions != CycleInstructions {
		t.Fatal("cycle instructions were changed by remote wiring")
	}
}

// TestRemoteContractCoversEveryTool guards the remote side of the tool contract.
// toolDescriptors owns the advertised names through ToolNames. Both remote maps
// must cover exactly that set: a missing name reaches AttachRemoteTools as an
// empty description and a generic {"type":"object"} input schema.
func TestRemoteContractCoversEveryTool(t *testing.T) {
	want := slices.Clone(ToolNames)
	slices.Sort(want)

	described := make([]string, 0, len(remoteToolDescriptions))
	for name := range remoteToolDescriptions {
		described = append(described, name)
	}
	slices.Sort(described)
	if !slices.Equal(described, want) {
		t.Fatalf("remoteToolDescriptions names = %v, want %v", described, want)
	}

	factories := make([]string, 0, len(remoteInputSchemaFactories))
	for name := range remoteInputSchemaFactories {
		factories = append(factories, name)
	}
	slices.Sort(factories)
	if !slices.Equal(factories, want) {
		t.Fatalf("remoteInputSchemaFactories names = %v, want %v", factories, want)
	}

	for _, name := range want {
		if strings.TrimSpace(remoteToolDescriptions[name]) == "" {
			t.Errorf("%s: empty remote description", name)
		}
		schema, err := remoteInputSchemaFactories[name]()
		if err != nil {
			t.Errorf("%s: input schema factory: %v", name, err)
			continue
		}
		if schema == nil || schema.Type != "object" {
			t.Errorf("%s: input schema is not an object: %+v", name, schema)
		}
	}
}

func TestRemoteClientForwardedResultKeepsIsError(t *testing.T) {
	client := remoteTestClient(t, remoteFakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return remoteJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"no watches"}],"structuredContent":{"status":"watching"},"isError":true}}`), nil
	}})
	result, err := client.CallTool(context.Background(), "beacon_wait_result", nil, nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("server's isError flag was dropped")
	}
	if text := remoteErrorText(&result); !strings.Contains(text, "no watches") {
		t.Fatalf("content = %q", text)
	}
}

func TestLoadRemoteEndpointUsesDirectoryFile(t *testing.T) {
	dir := t.TempDir()
	want := remoteTestEndpoint()
	data, _ := json.Marshal(want)
	if err := os.WriteFile(filepath.Join(dir, remoteConnectionFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRemoteEndpoint(dir)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}
