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
			options := []map[string]any{
				{"optionId": "allow-once", "name": "Allow", "kind": "allow_once"},
			}
			rejectOnly := os.Getenv("ACP_FAKE_AGENT_REJECT_ONLY") == "1"
			if rejectOnly {
				options = []map[string]any{
					{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
				}
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 5, "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "sess_wire",
					"options":   options,
				},
			})
			chunkText := "pull/99"
			if rejectOnly {
				var resp struct {
					Result struct {
						Outcome struct {
							Outcome string `json:"outcome"`
						} `json:"outcome"`
					} `json:"result"`
				}
				if err := dec.Decode(&resp); err != nil {
					os.Exit(3)
				}
				chunkText = "permission:" + resp.Result.Outcome.Outcome
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": "sess_wire",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]string{"type": "text", "text": chunkText},
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

func TestFirstAllowOption(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   string
	}{
		{
			name:   "allow option",
			params: `{"options":[{"optionId":"allow-once","kind":"allow_once"}]}`,
			want:   "allow-once",
		},
		{
			name:   "reject only",
			params: `{"options":[{"optionId":"reject-once","kind":"reject_once"},{"optionId":"reject-always","kind":"reject_always"}]}`,
			want:   "",
		},
		{
			name:   "reject before allow",
			params: `{"options":[{"optionId":"reject-once","kind":"reject_once"},{"optionId":"allow-once","kind":"allow_once"}]}`,
			want:   "allow-once",
		},
		{
			name:   "unknown kind",
			params: `{"options":[{"optionId":"mystery","kind":"custom"}]}`,
			want:   "",
		},
		{
			name:   "malformed",
			params: `{`,
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstAllowOption(json.RawMessage(tt.params)); got != tt.want {
				t.Fatalf("firstAllowOption(%s) = %q, want %q", tt.params, got, tt.want)
			}
		})
	}
}

func TestPermissionWithOnlyRejectOptionsCancels(t *testing.T) {
	t.Setenv("ACP_FAKE_AGENT", "1")
	t.Setenv("ACP_FAKE_AGENT_REJECT_ONLY", "1")
	p, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var chunks []string
	err = p.call(ctx, "session/prompt", map[string]any{"sessionId": "sess_wire"}, nil,
		func(raw json.RawMessage) {
			if text := collectText(raw); text != "" {
				chunks = append(chunks, text)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0] != "permission:cancelled" {
		t.Fatalf("agent chunks = %v, want [permission:cancelled]", chunks)
	}
}

func TestNotifyReachesAgentDuringInFlightCall(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &stdioProcess{
		enc: json.NewEncoder(inW), dec: json.NewDecoder(outR),
		stdin: inW, stdout: outR,
	}

	requests := make(chan string, 2)
	go func() {
		dec := json.NewDecoder(inR)
		for {
			var msg struct {
				Method string `json:"method"`
			}
			if err := dec.Decode(&msg); err != nil {
				return
			}
			requests <- msg.Method
		}
	}()

	callDone := make(chan error, 1)
	go func() {
		callDone <- p.call(context.Background(), "session/prompt", nil, nil, nil)
	}()
	select {
	case method := <-requests:
		if method != "session/prompt" {
			t.Fatalf("first request = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("call did not write its request")
	}

	notifyDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { notifyDone <- p.notify(ctx, "session/cancel", nil) }()

	select {
	case err := <-notifyDone:
		if err != nil {
			t.Fatalf("notify: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		_ = outW.Close()
		<-callDone
		t.Fatal("notify blocked behind the in-flight call")
	}
	select {
	case method := <-requests:
		if method != "session/cancel" {
			t.Fatalf("notification = %q, want session/cancel", method)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel notification was not written")
	}
	_ = outW.Close()
	<-callDone
}
