package wake

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const w10aMockEnv = "BEACON_W10A_ACP_MOCK"

func TestW10AResolveACPRuntimeCommand(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ACPRuntimeConfig
		command string
		args    []string
		wantErr string
	}{
		{"opencode", ACPRuntimeConfig{Runtime: "opencode"}, "opencode", []string{"acp"}, ""},
		{"claude", ACPRuntimeConfig{Runtime: "claude"}, "claude", []string{"--acp"}, ""},
		{"codex", ACPRuntimeConfig{Runtime: "codex"}, "codex", []string{"acp"}, ""},
		{"pi", ACPRuntimeConfig{Runtime: "pi"}, "pi", []string{"--acp"}, ""},
		{"trim and case", ACPRuntimeConfig{Runtime: "  OpenCode "}, "opencode", []string{"acp"}, ""},
		{"explicit command", ACPRuntimeConfig{Runtime: "bogus", Command: "my-wrap", Args: []string{"serve"}}, "my-wrap", []string{"serve"}, ""},
		{"arg override", ACPRuntimeConfig{Runtime: "opencode", Args: []string{"acp", "--stdio"}}, "opencode", []string{"acp", "--stdio"}, ""},
		{"empty", ACPRuntimeConfig{}, "", nil, "empty runtime and command"},
		{"unknown", ACPRuntimeConfig{Runtime: "unknown"}, "", nil, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, args, err := ResolveACPRuntimeCommand(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveACPRuntimeCommand: %v", err)
			}
			if command != tc.command || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("got (%q, %v), want (%q, %v)", command, args, tc.command, tc.args)
			}
		})
	}
}

func TestW10ASupportedACPRuntimes(t *testing.T) {
	want := []string{"claude", "codex", "gemini", "opencode", "pi"}
	if got := SupportedACPRuntimes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedACPRuntimes = %v, want %v", got, want)
	}
}

// TestW10AACPMockChild is the mock ACP process. The parent runs the same test
// binary with -test.run pointing here and the env flag set; the child speaks
// newline-delimited JSON-RPC on stdin/stdout and never touches a real runner.
func TestW10AACPMockChild(t *testing.T) {
	if os.Getenv(w10aMockEnv) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req ACPRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		var result map[string]any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion":   persistentACPProtocolVersion,
				"agentCapabilities": map[string]any{},
				"agentInfo":         map[string]any{"name": "mock", "version": "0"},
			}
		case "session/new":
			result = map[string]any{"sessionId": "ses_w10a_mock"}
		case "session/load", "session/cancel":
			result = map[string]any{}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
			notify := map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "ses_w10a_mock",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "done"},
					},
				},
			}
			if raw, err := json.Marshal(notify); err == nil {
				_, _ = fmt.Fprintln(os.Stdout, string(raw))
			}
		default:
			raw, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32601, "message": "unknown method"},
			})
			_, _ = fmt.Fprintln(os.Stdout, string(raw))
			continue
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = fmt.Fprintln(os.Stdout, string(raw))
	}
	// Avoid the testing framework's trailing "PASS" line on the wire.
	os.Exit(0)
}

// TestW10APersistentACPClientOverMockProcess drives the real PersistentACPClient
// through a child process that only speaks ACP. No external binary is required.
func TestW10APersistentACPClientOverMockProcess(t *testing.T) {
	cfg := ACPRuntimeConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestW10AACPMockChild$"},
		Env:     append(os.Environ(), w10aMockEnv+"=1"),
	}
	rt, err := NewWorkerRuntime(cfg)
	if err != nil {
		t.Fatalf("NewWorkerRuntime: %v", err)
	}
	defer func() { _ = rt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rt.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	sessionID, err := rt.NewSession(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sessionID != "ses_w10a_mock" {
		t.Fatalf("sessionID = %q, want ses_w10a_mock", sessionID)
	}
	if err := rt.Prompt(ctx, sessionID, "w10a"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	select {
	case update := <-rt.Updates():
		if update.SessionID != sessionID {
			t.Fatalf("update session = %q, want %q", update.SessionID, sessionID)
		}
		var chunk struct {
			SessionUpdate string `json:"sessionUpdate"`
		}
		if err := json.Unmarshal(update.Update, &chunk); err != nil {
			t.Fatalf("decode update: %v", err)
		}
		if chunk.SessionUpdate != "agent_message_chunk" {
			t.Fatalf("sessionUpdate = %q, want agent_message_chunk", chunk.SessionUpdate)
		}
	case <-ctx.Done():
		t.Fatal("no session/update from the mock process")
	}
}
