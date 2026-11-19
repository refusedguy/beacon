package mcp

import (
	"encoding/json"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// sessionIDMetaKey is the reserved _meta key that carries the caller's
	// opaque session identifier. The value is a wire contract, not knowledge
	// of any particular client: the core never interprets it (ADR 0001).
	sessionIDMetaKey = "ai.opencode/sessionID"

	// chunkMetaKey carries one numbered chunk of a Beacon event stream in a
	// tool result's _meta (ADR 0031 §1, Q 1.2 mcp-partials).
	chunkMetaKey = "ai.beacon/streamChunk"

	// finalMetaKey carries the terminal stream counters and the complete
	// result in a tool result's _meta.
	finalMetaKey = "ai.beacon/streamFinal"
)

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

// SetChunkMeta attaches one numbered stream chunk to result's _meta so a
// client can restore order by seq and spot gaps without parsing the body.
func SetChunkMeta(result *mcpsdk.CallToolResult, ev domain.StreamEvent) error {
	return setStreamMeta(result, chunkMetaKey, ev)
}

// ChunkMetaFromResult reads a numbered stream chunk from result's _meta. The
// bool is false when the key is absent or the value cannot be decoded.
func ChunkMetaFromResult(result *mcpsdk.CallToolResult) (domain.StreamEvent, bool) {
	var ev domain.StreamEvent
	return ev, getStreamMeta(result, chunkMetaKey, &ev)
}

// SetFinalMeta attaches the terminal stream counters and the complete result
// to result's _meta.
func SetFinalMeta(result *mcpsdk.CallToolResult, final domain.StreamFinal) error {
	return setStreamMeta(result, finalMetaKey, final)
}

// FinalMetaFromResult reads the terminal stream counters from result's _meta.
func FinalMetaFromResult(result *mcpsdk.CallToolResult) (domain.StreamFinal, bool) {
	var final domain.StreamFinal
	return final, getStreamMeta(result, finalMetaKey, &final)
}

// setStreamMeta stores value as raw JSON in the result's _meta map. Raw JSON
// keeps the value well-formed on the wire while getStreamMeta can decode it
// whether the map holds our RawMessage or a map decoded from the wire.
func setStreamMeta(result *mcpsdk.CallToolResult, key string, value any) error {
	if result == nil {
		return errors.New("mcp: nil tool result")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if result.Meta == nil {
		result.Meta = mcpsdk.Meta{}
	}
	result.Meta[key] = json.RawMessage(b)
	return nil
}

func getStreamMeta(result *mcpsdk.CallToolResult, key string, out any) bool {
	if result == nil || result.Meta == nil {
		return false
	}
	raw, ok := result.Meta[key]
	if !ok {
		return false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}
