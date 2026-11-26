package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	if os.Getenv("ACP_FAKE_AGENT_COLLIDE") == "1" {
		runCollisionAgent(dec, enc)
		return
	}
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
			// A gate makes this call hold its watcher slot until the test
			// creates the file. The marker tells the test the watcher is
			// already borrowed when the request reaches the agent.
			var params struct {
				Gate string `json:"gate"`
			}
			if len(msg.Params) != 0 {
				_ = json.Unmarshal(msg.Params, &params)
			}
			if params.Gate != "" {
				_ = os.WriteFile(params.Gate+".started", nil, 0o600)
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(params.Gate); err == nil {
						break
					}
					if time.Now().After(deadline) {
						os.Exit(4)
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
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

// runCollisionAgent sends an agent request whose ID matches the pending call
// ID, followed by a notification. It then reads the reply the harness wrote
// for that request and folds it into the real response so the test can see
// whether the request was answered instead of consumed as the call result.
func runCollisionAgent(dec *json.Decoder, enc *json.Encoder) {
	for {
		var incoming struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if err := dec.Decode(&incoming); err != nil {
			if err == io.EOF {
				return
			}
			os.Exit(2)
		}
		if incoming.Method == "" || incoming.ID == nil {
			continue
		}
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": *incoming.ID, "method": "session/request_permission",
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
					"content":       map[string]string{"type": "text", "text": "note-collision"},
				},
			},
		})
		var answer struct {
			ID     int64 `json:"id"`
			Result struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			} `json:"result"`
		}
		if err := dec.Decode(&answer); err != nil {
			os.Exit(3)
		}
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": *incoming.ID,
			"result": map[string]any{
				"stopReason":     "end_turn",
				"answerID":       answer.ID,
				"answerOptionID": answer.Result.Outcome.OptionID,
			},
		})
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

// TestCallRoutesCollidingRequestAndNotification drives the real transport with
// an agent request whose ID equals the pending call ID. A response envelope
// with a method field must be answered, not consumed as the call result.
func TestCallRoutesCollidingRequestAndNotification(t *testing.T) {
	t.Setenv("ACP_FAKE_AGENT", "1")
	t.Setenv("ACP_FAKE_AGENT_COLLIDE", "1")
	p, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var notes []string
	var result struct {
		StopReason     string `json:"stopReason"`
		AnswerID       int64  `json:"answerID"`
		AnswerOptionID string `json:"answerOptionID"`
	}
	err = p.call(ctx, "session/prompt", map[string]any{"sessionId": "sess_wire"}, &result,
		func(raw json.RawMessage) {
			if text := collectText(raw); text != "" {
				notes = append(notes, text)
			}
		})
	if err != nil {
		t.Fatal(err)
	}

	if result.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn (the pending call must resolve from the real response)", result.StopReason)
	}
	if result.AnswerID != 1 {
		t.Fatalf("answer id = %d, want 1 (the colliding request must be answered at its own id)", result.AnswerID)
	}
	if result.AnswerOptionID != "allow-once" {
		t.Fatalf("answer option = %q, want allow-once", result.AnswerOptionID)
	}
	if len(notes) != 1 || notes[0] != "note-collision" {
		t.Fatalf("notifications = %v, want [note-collision]", notes)
	}
}

// waitForFile blocks until path exists or timeout elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// promptWithGate runs a prompt call. A non-empty gate makes the fake agent
// wait for that file before answering, so the call holds a watcher slot until
// the test opens the gate.
func promptWithGate(ctx context.Context, p *stdioProcess, gate string) ([]string, error) {
	params := map[string]any{"sessionId": "sess_wire"}
	if gate != "" {
		params["gate"] = gate
	}
	var chunks []string
	err := p.call(ctx, "session/prompt", params, nil, func(raw json.RawMessage) {
		if text := collectText(raw); text != "" {
			chunks = append(chunks, text)
		}
	})
	return chunks, err
}

// TestNinthConcurrentCallPastWatcherPool fills the eight-slot package-global
// watcher pool with held calls, then starts a ninth. It checks that cancelling
// a held call frees its slot, that the ninth call still carries the response
// and progress to its caller, and that no watcher or semaphore token is left
// behind.
func TestNinthConcurrentCallPastWatcherPool(t *testing.T) {
	t.Setenv("ACP_FAKE_AGENT", "1")

	// Start the pool goroutines before measuring the baseline.
	warm, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	werr := warm.call(wctx, "initialize", map[string]any{"protocolVersion": 1}, nil, nil)
	wcancel()
	if werr != nil {
		t.Fatal(werr)
	}
	_ = warm.close()
	baseline := runtime.NumGoroutine()

	type result struct {
		chunks []string
		err    error
	}
	const calls = maxWatchers + 1
	results := make([]chan result, calls)
	for i := range results {
		results[i] = make(chan result, 1)
	}
	ctxs := make([]context.Context, calls)
	cancels := make([]context.CancelFunc, calls)
	procs := make([]*stdioProcess, calls)
	gates := make([]string, maxWatchers)
	root := t.TempDir()
	for i := range gates {
		gates[i] = filepath.Join(root, "gate-"+fmt.Sprint(i))
	}

	for i := 0; i < maxWatchers; i++ {
		p, err := startProcess(os.Args[0], t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		procs[i] = p
		ctxs[i], cancels[i] = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancels[i]()
		go func(i int, p *stdioProcess, ctx context.Context, gate string) {
			chunks, err := promptWithGate(ctx, p, gate)
			results[i] <- result{chunks: chunks, err: err}
		}(i, p, ctxs[i], gates[i])
	}

	// Every held agent writes a marker before waiting on its gate. It can only
	// have received the request after its call borrowed a watcher, so all
	// eight slots are known to be busy at this point.
	for _, gate := range gates {
		waitForFile(t, gate+".started", 5*time.Second)
	}

	// Start the ninth call only after the pool is full. It has no gate and
	// would answer at once, so it must be waiting on the pool semaphore.
	p9, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	procs[maxWatchers] = p9
	ctxs[maxWatchers], cancels[maxWatchers] = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancels[maxWatchers]()
	go func() {
		chunks, err := promptWithGate(ctxs[maxWatchers], p9, "")
		results[maxWatchers] <- result{chunks: chunks, err: err}
	}()

	select {
	case got := <-results[maxWatchers]:
		t.Fatalf("ninth call finished before a slot was freed: %+v", got)
	default:
	}

	// Cancelling a held call makes its watcher kill the agent. The call then
	// returns an error to its caller and releases the slot.
	cancels[0]()
	select {
	case got := <-results[0]:
		if got.err == nil {
			t.Fatal("cancelled held call returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled held call did not return")
	}

	// The ninth call now borrows the freed watcher and reaches its caller with
	// both the response and the progress chunk.
	select {
	case got := <-results[maxWatchers]:
		if got.err != nil {
			t.Fatalf("ninth call: %v", got.err)
		}
		if len(got.chunks) != 1 || got.chunks[0] != "pull/99" {
			t.Fatalf("ninth call chunks = %v, want [pull/99]", got.chunks)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ninth call did not finish after a slot was freed")
	}

	// Open the other gates and check that progress reaches those callers too.
	for i, gate := range gates {
		if i == 0 {
			continue
		}
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < maxWatchers; i++ {
		select {
		case got := <-results[i]:
			if got.err != nil {
				t.Fatalf("held call %d: %v", i, got.err)
			}
			if len(got.chunks) != 1 || got.chunks[0] != "pull/99" {
				t.Fatalf("held call %d chunks = %v, want [pull/99]", i, got.chunks)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("held call %d did not finish", i)
		}
	}

	for _, p := range procs {
		_ = p.close()
	}

	// A fresh call must still borrow a watcher after the cancelled call, which
	// proves no semaphore token was stranded.
	fresh, err := startProcess(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.close() }()
	fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer fcancel()
	if err := fresh.call(fctx, "initialize", map[string]any{"protocolVersion": 1}, nil, nil); err != nil {
		t.Fatalf("call after saturation: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.Gosched()
		grew := runtime.NumGoroutine() - baseline
		if grew <= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines grew by %d, want at most 2", grew)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
