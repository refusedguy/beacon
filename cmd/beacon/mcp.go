package main

import (
	"context"
	"fmt"
	"io"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// mcpRunner serves MCP over stdio. RegisterMCP replaces it, for example in tests.
// The real implementation calls the package-level mcp.Run, which builds the
// server with mcp.NewServer and serves it on StdioTransport. Run needs a
// context, so this composition root supplies one without changing server.go.
var mcpRunner = func(errOut io.Writer) int {
	if err := mcp.Run(context.Background()); err != nil {
		fmt.Fprintf(errOut, "beacon mcp: %v\n", err)
		return 1
	}
	return 0
}

// RegisterMCP installs the mcp command implementation.
func RegisterMCP(fn func(io.Writer) int) {
	mcpRunner = fn
}
