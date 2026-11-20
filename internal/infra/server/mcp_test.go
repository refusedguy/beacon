package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
	beaconserver "github.com/refusedguy/beacon/internal/infra/server"
)

const testMCPToken = "sekret"

// bearerTransport adds the bearer token to every HTTP request the SDK client
// issues, including the initialize POST and the standalone SSE GET.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// TestMCPHTTPToolsListMatchesSTDIO is the BS.43 acceptance check: the tool
// names served over the streamable HTTP endpoint must equal the names the
// stdio/in-memory transport serves from the same NewServerWithDeps.
func TestMCPHTTPToolsListMatchesSTDIO(t *testing.T) {
	srv := beaconmcp.NewServerWithDeps(beaconmcp.Dependencies{})
	handler := beaconserver.NewMCPHTTPHandler(srv, testMCPToken)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	stdioNames := toolNamesOverInMemory(t)
	httpNames := toolNamesOverHTTP(t, ts.URL+beaconserver.MCPPath, testMCPToken)

	slices.Sort(stdioNames)
	slices.Sort(httpNames)
	if !slices.Equal(stdioNames, httpNames) {
		t.Fatalf("tools/list mismatch: HTTP = %v, stdio = %v", httpNames, stdioNames)
	}
	if len(httpNames) != len(beaconmcp.ToolNames) {
		t.Fatalf("tools/list returned %d tools (%v), want %d", len(httpNames), httpNames, len(beaconmcp.ToolNames))
	}
}

// toolNamesOverInMemory drives the same server the stdio command runs over an
// in-memory transport, which is the SDK equivalent of stdin/stdout for a test.
func toolNamesOverInMemory(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := beaconmcp.NewServerWithDeps(beaconmcp.Dependencies{}).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-stdio-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return collectToolNames(t, ctx, clientSession)
}

func toolNamesOverHTTP(t *testing.T, endpoint, token string) []string {
	t.Helper()
	ctx := context.Background()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-http-test", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("http connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return collectToolNames(t, ctx, session)
}

func collectToolNames(t *testing.T, ctx context.Context, session *mcpsdk.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		names = append(names, tool.Name)
	}
	return names
}

// TestMCPHTTPRequiresBearerToken proves both a missing and a wrong credential
// are rejected before the SDK sees the request.
func TestMCPHTTPRequiresBearerToken(t *testing.T) {
	srv := beaconmcp.NewServerWithDeps(beaconmcp.Dependencies{})
	ts := httptest.NewServer(beaconserver.NewMCPHTTPHandler(srv, testMCPToken))
	defer ts.Close()

	for _, tc := range []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "wrong", header: "Bearer wrong-token"},
		{name: "wrong scheme", header: testMCPToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+beaconserver.MCPPath,
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
			}
		})
	}
}

// TestMCPHTTPScopedToPath proves requests outside /mcp are never handed to the
// transport, even with a valid token.
func TestMCPHTTPScopedToPath(t *testing.T) {
	srv := beaconmcp.NewServerWithDeps(beaconmcp.Dependencies{})
	ts := httptest.NewServer(beaconserver.NewMCPHTTPHandler(srv, testMCPToken))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/other", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testMCPToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestMCPHTTPEmptyTokenRejectsAll documents the fail-closed contract for an
// omitted token.
func TestMCPHTTPEmptyTokenRejectsAll(t *testing.T) {
	srv := beaconmcp.NewServerWithDeps(beaconmcp.Dependencies{})
	ts := httptest.NewServer(beaconserver.NewMCPHTTPHandler(srv, ""))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+beaconserver.MCPPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}
