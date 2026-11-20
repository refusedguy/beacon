package wake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

// w2bACPServer speaks just enough ACP over pipes to drive one long-lived
// client. It never spawns a process.
type w2bACPServer struct {
	in  *bufio.Scanner
	out io.Writer

	hangCancel bool

	mu           sync.Mutex
	methods      []string
	promptText   string
	promptTarget string
}

func (s *w2bACPServer) serve() {
	for s.in.Scan() {
		var req ACPRequest
		if err := json.Unmarshal(s.in.Bytes(), &req); err != nil {
			continue
		}
		s.mu.Lock()
		s.methods = append(s.methods, req.Method)
		s.mu.Unlock()
		if req.Method == "session/cancel" && s.hangCancel {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion":   persistentACPProtocolVersion,
				"agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo":         map[string]any{"name": "stub", "version": "0.0.0"},
			}
		case "session/new":
			result = map[string]any{"sessionId": "ses_w2b_acp"}
		case "session/prompt":
			raw, err := json.Marshal(req.Params)
			if err == nil {
				var params ACPPromptParams
				if err := json.Unmarshal(raw, &params); err == nil {
					s.mu.Lock()
					s.promptTarget = params.SessionID
					if len(params.Prompt) > 0 {
						s.promptText = params.Prompt[0].Text
					}
					s.mu.Unlock()
				}
			}
			s.notify("ses_w2b_acp")
		}
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(s.out, "%s\n", raw)
	}
}

func (s *w2bACPServer) notify(sessionID string) {
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": "working"},
			},
		},
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(s.out, "%s\n", raw)
}

func (s *w2bACPServer) snapshot() ([]string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...), s.promptTarget, s.promptText
}

// newW2BACPPipe wires an injected client to a stub on two pipes.
func newW2BACPPipe(hangCancel bool) (*PersistentACPClient, *w2bACPServer) {
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	srv := &w2bACPServer{in: bufio.NewScanner(inReader), out: outWriter, hangCancel: hangCancel}
	go srv.serve()

	client := newPersistentACPClient()
	client.stdin = inWriter
	client.stdout = outReader
	if err := client.Start(); err != nil {
		panic(err)
	}
	return client, srv
}

func TestW2BPersistentACPFullSessionOverOneProcess(t *testing.T) {
	client, srv := newW2BACPPipe(false)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	sessionID, err := client.NewSession(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sessionID != "ses_w2b_acp" {
		t.Fatalf("sessionID = %q, want ses_w2b_acp", sessionID)
	}
	if err := client.LoadSession(ctx, sessionID, t.TempDir()); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if err := client.Prompt(ctx, sessionID, "wake"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	select {
	case update := <-client.Updates():
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
		t.Fatal("no session/update arrived")
	}

	if err := client.Cancel(ctx, sessionID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	methods, target, text := srv.snapshot()
	want := []string{"initialize", "session/new", "session/load", "session/prompt", "session/cancel"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
	if target != sessionID || text != "wake" {
		t.Fatalf("prompt = (%q, %q), want (%q, wake)", target, text, sessionID)
	}
}

func TestW2BPersistentACPCallHonoursContext(t *testing.T) {
	client, _ := newW2BACPPipe(true)
	defer client.Close()

	initCtx, initCancel := context.WithTimeout(context.Background(), time.Second)
	defer initCancel()
	if err := client.Initialize(initCtx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := client.Cancel(ctx, "ses_w2b_acp")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Cancel error = %v, want a deadline error", err)
	}
}
