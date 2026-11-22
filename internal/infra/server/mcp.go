// MCP endpoint: the asynchronous streamable HTTP transport from the go-sdk,
// serving the same advertised tools as the stdio transport behind a bearer
// token.
package server

import (
	"crypto/subtle"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPPath is the path the Beacon MCP endpoint is served at. Callers mount the
// handler returned by NewMCPHTTPHandler at this path (BS.43, ADR 0189).
const MCPPath = "/mcp"

// MCPExtraRoutes returns the route table that mounts the MCP endpoint on an
// existing HTTP listener. The composition root sets httpapi.Config.Token to
// the same token, so the listener gateway and this handler agree on the bearer
// value. Keeping the map here lets the cmd layer stay clear of net/http.
func MCPExtraRoutes(srv *mcpsdk.Server, token string) map[string]http.Handler {
	return map[string]http.Handler{
		MCPPath: NewMCPHTTPHandler(srv, token),
	}
}

// NewMCPHTTPHandler builds the HTTP handler for the Beacon MCP endpoint.
//
// Contract: the composition root passes the *mcpsdk.Server returned by
// mcp.NewServerWithDeps, together with the server bearer token. This package
// cannot import the mcp package (both are internal/infra), so wiring stays at
// the boundary: the handler only depends on the SDK server type. Because the
// same server is reused for every session, tools/list over HTTP is the exact
// list the stdio transport serves.
//
// The go-sdk streamable HTTP transport is asynchronous: one POST carries a
// JSON-RPC batch and server-to-client traffic uses a separate SSE stream. The
// handler runs in stateless mode because Beacon tools never initiate requests
// to the client, and stateless mode avoids session bookkeeping.
//
// Every request must carry "Authorization: Bearer <token>". Comparison is
// constant time and an empty token rejects every request, so the endpoint can
// never be left open by an omitted configuration value.
func NewMCPHTTPHandler(srv *mcpsdk.Server, token string) http.Handler {
	stream := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return srv
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MCPPath {
			http.NotFound(w, r)
			return
		}
		if !validMCPBearer(r.Header.Get("Authorization"), token) {
			writeMCPUnauthorized(w)
			return
		}
		stream.ServeHTTP(w, r)
	})
}

// validMCPBearer checks the Authorization header against the configured
// token. An empty configured token is never satisfied.
func validMCPBearer(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+want)) == 1
}

// writeMCPUnauthorized returns the same error body shape as the HTTP API.
func writeMCPUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"code":"UNAUTHORIZED","message":"неверный bearer-токен"}`))
}
