package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets a test start this binary as a fake ACP agent. The child reads
// JSON-RPC from stdin and writes ACP messages to stdout.
func TestMain(m *testing.M) {
	if os.Getenv("ACP_FAKE_AGENT") == "1" {
		runFakeAgent()
		return
	}
	os.Exit(m.Run())
}

func runFakeAgent() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return
			}
			os.Exit(2)
		}
		if msg.Method == "" {
			// Response to the permission request the agent sent.
			continue
		}
		switch msg.Method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"protocolVersion": 1},
			})
		case "session/new":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"sessionId": "sess_wire"},
			})
		case "session/prompt":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 5, "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "sess_wire",
					"options": []map[string]any{
						{"optionId": "allow-once", "name": "Allow", "kind": "allow_once"},
					},
				},
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": "sess_wire",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]string{"type": "text", "text": "pull/99"},
					},
				},
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"stopReason": "end_turn"},
			})
		case "session/cancel":
			// Notification only.
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"error": map[string]any{"code": -32601, "message": "unknown method"},
			})
		}
	}
}

func TestStdioProcessSpeaksACP(t *testing.T) {
	t.Setenv("ACP_FAKE_AGENT", "1")
	p, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var init struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := p.call(ctx, "initialize", map[string]any{"protocolVersion": 1}, &init, nil); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != 1 {
		t.Fatalf("protocol version = %d, want 1", init.ProtocolVersion)
	}

	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := p.call(ctx, "session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}}, &session, nil); err != nil {
		t.Fatal(err)
	}
	if session.SessionID != "sess_wire" {
		t.Fatalf("session id = %q, want sess_wire", session.SessionID)
	}

	var chunks []string
	err = p.call(ctx, "session/prompt", map[string]any{"sessionId": session.SessionID}, nil,
		func(raw json.RawMessage) {
			if text := collectText(raw); text != "" {
				chunks = append(chunks, text)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0] != "pull/99" {
		t.Fatalf("agent chunks = %v, want [pull/99]", chunks)
	}
	if err := p.notify(ctx, "session/cancel", map[string]any{"sessionId": session.SessionID}); err != nil {
		t.Fatal(err)
	}
}

func TestStdioProcessReportsJSONRPCErrors(t *testing.T) {
	t.Setenv("ACP_FAKE_AGENT", "1")
	p, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = p.call(ctx, "unknown/method", map[string]any{}, nil, nil)
	if err == nil {
		t.Fatal("expected a JSON-RPC error")
	}
	if !strings.Contains(err.Error(), "-32601") {
		t.Fatalf("error = %q, want JSON-RPC code -32601", err)
	}
}
