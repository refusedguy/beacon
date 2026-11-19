package mcp

import mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

// sessionIDMetaKey is the reserved _meta key that carries the caller's opaque
// session identifier. The value is a wire contract, not knowledge of any
// particular client: the core never interprets it (ADR 0001).
const sessionIDMetaKey = "ai.opencode/sessionID"

// SessionIDFromRequest reads the caller's session identifier from the tool
// request's _meta and returns it as an opaque string. It returns nil when the
// request has no _meta, the key is missing, or the value is empty. The value
// is passed through verbatim: no format parsing, no validation and no effect
// on access (WORKER-PROMPT §2).
func SessionIDFromRequest(req *mcpsdk.CallToolRequest) *string {
	if req == nil || req.Params == nil || req.Params.Meta == nil {
		return nil
	}
	raw, ok := req.Params.Meta[sessionIDMetaKey]
	if !ok {
		return nil
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return nil
	}
	return &value
}
