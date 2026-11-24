package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type fakeCall struct {
	method string
	params json.RawMessage
}

type fakeProcess struct {
	mu      sync.Mutex
	calls   []fakeCall
	notes   []string
	results map[string]string
	errs    map[string]error
	closed  bool
	waited  bool
}

func (f *fakeProcess) call(
	_ context.Context, method string, params any, out any, onNote func(json.RawMessage),
) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: method, params: raw})
	notes := append([]string(nil), f.notes...)
	result := f.results[method]
	err := f.errs[method]
	f.mu.Unlock()

	if err != nil {
		return err
	}
	if onNote != nil {
		for _, note := range notes {
			onNote(json.RawMessage(note))
		}
	}
	if out != nil && result != "" {
		return json.Unmarshal([]byte(result), out)
	}
	return nil
}

func (f *fakeProcess) notify(_ context.Context, method string, params any) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: method, params: raw})
	f.mu.Unlock()
	return nil
}

func (f *fakeProcess) close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeProcess) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

func (f *fakeProcess) paramsFor(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.method == method {
			var out map[string]any
			_ = json.Unmarshal(c.params, &out)
			return out
		}
	}
	return nil
}

func (f *fakeProcess) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func testHarness(fp process) *Harness {
	h := New("fake-agent --acp")
	h.Timeout = time.Second
	h.newProcess = func(_, _ string) (process, error) { return fp, nil }
	return h
}

func standardProcess() *fakeProcess {
	return &fakeProcess{
		results: map[string]string{
			"initialize":     `{"protocolVersion":1}`,
			"session/new":    `{"sessionId":"sess_1"}`,
			"session/prompt": `{"stopReason":"end_turn"}`,
		},
		notes: []string{
			`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"opened pull/42"}}}}`,
		},
	}
}

func TestSpawnRunsHandshakeSessionAndPrompt(t *testing.T) {
	fp := standardProcess()
	h := testHarness(fp)

	sess, err := h.Spawn(context.Background(), core.SpawnRequest{
		RunID: "run_1", Task: core.Task{ID: "T-1"},
		Workspace: core.Workspace{Path: "/ws/repo", Branch: "main"},
		Prompt:    "do the work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "sess_1" || sess.PR != 42 {
		t.Fatalf("session = %+v, want id sess_1 and PR 42", sess)
	}

	want := []string{"initialize", "session/new", "session/prompt"}
	got := fp.methods()
	if len(got) != len(want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("methods = %v, want %v", got, want)
		}
	}

	init := fp.paramsFor("initialize")
	if init["protocolVersion"] != float64(1) {
		t.Errorf("initialize protocolVersion = %v, want 1", init["protocolVersion"])
	}
	if info, ok := init["clientInfo"].(map[string]any); !ok || info["name"] != "beacon" {
		t.Errorf("initialize clientInfo = %v, want beacon", init["clientInfo"])
	}
	if cwd := fp.paramsFor("session/new")["cwd"]; cwd != "/ws/repo" {
		t.Errorf("session/new cwd = %v, want /ws/repo", cwd)
	}
	prompt := fp.paramsFor("session/prompt")
	blocks, _ := prompt["prompt"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("prompt blocks = %v, want one text block", prompt["prompt"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "do the work" {
		t.Errorf("prompt block = %v, want the task prompt", block)
	}

	select {
	case ev := <-h.Events():
		if ev.RunID != "run_1" || ev.SessionID != "sess_1" || ev.Kind != "idle" {
			t.Errorf("event = %+v, want idle for run_1/sess_1", ev)
		}
	default:
		t.Fatal("no idle event after prompt")
	}
	if alive, _ := h.IsAlive(context.Background(), "sess_1"); !alive {
		t.Fatal("session is not alive after spawn")
	}
}

func TestSpawnRejectsUnsupportedProtocolVersion(t *testing.T) {
	fp := &fakeProcess{results: map[string]string{"initialize": `{"protocolVersion":2}`}}
	h := testHarness(fp)
	if _, err := h.Spawn(context.Background(), core.SpawnRequest{}); err == nil {
		t.Fatal("spawn accepted an unsupported protocol version")
	}
	if !fp.isClosed() {
		t.Fatal("agent was not closed after version mismatch")
	}
}

func TestSpawnRequiresCommand(t *testing.T) {
	h := New("")
	h.newProcess = func(_, _ string) (process, error) {
		t.Fatal("process started with empty command")
		return nil, nil
	}
	if _, err := h.Spawn(context.Background(), core.SpawnRequest{}); err == nil {
		t.Fatal("spawn accepted an empty command")
	}
}

func TestSpawnKeepsHandshakeMinimalWithModelConfigured(t *testing.T) {
	fp := standardProcess()
	h := testHarness(fp)
	h.Model = "sonnet"
	if _, err := h.Spawn(context.Background(), core.SpawnRequest{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"initialize", "session/new", "session/prompt"}
	if got := fp.methods(); !reflect.DeepEqual(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

func TestWakeReusesSessionAndSendsCorrection(t *testing.T) {
	fp := standardProcess()
	h := testHarness(fp)
	if _, err := h.Spawn(context.Background(), core.SpawnRequest{}); err != nil {
		t.Fatal(err)
	}
	w := core.WakeContext{RunID: "run_1", PRNumber: 7, HeadSHA: "abc", Reason: "ci red", Excerpt: "log"}
	if err := h.Wake(context.Background(), "sess_1", w); err != nil {
		t.Fatal(err)
	}
	var prompts []map[string]any
	fp.mu.Lock()
	for _, c := range fp.calls {
		if c.method == "session/prompt" {
			var p map[string]any
			_ = json.Unmarshal(c.params, &p)
			prompts = append(prompts, p)
		}
	}
	fp.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("prompt calls = %d, want 2", len(prompts))
	}
	blocks, _ := prompts[1]["prompt"].([]any)
	block, _ := blocks[0].(map[string]any)
	text, _ := block["text"].(string)
	if prompts[1]["sessionId"] != "sess_1" || !strings.Contains(text, "ci red") || !strings.Contains(text, "PR #7") {
		t.Fatalf("wake prompt = %v, want a correction on sess_1", prompts[1])
	}
}

func TestWakeRejectsUnknownSession(t *testing.T) {
	h := testHarness(standardProcess())
	if err := h.Wake(context.Background(), "missing", core.WakeContext{}); err == nil {
		t.Fatal("wake accepted an unknown session")
	}
}

func TestStopCancelsClosesAndIsIdempotent(t *testing.T) {
	fp := standardProcess()
	h := testHarness(fp)
	if _, err := h.Spawn(context.Background(), core.SpawnRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Stop(context.Background(), "sess_1"); err != nil {
		t.Fatal(err)
	}
	if !fp.isClosed() {
		t.Fatal("agent was not closed")
	}
	if params := fp.paramsFor("session/cancel"); params == nil || params["sessionId"] != "sess_1" {
		t.Fatalf("cancel params = %v, want sess_1", params)
	}
	if alive, _ := h.IsAlive(context.Background(), "sess_1"); alive {
		t.Fatal("stopped session still reports alive")
	}
	if err := h.Stop(context.Background(), "sess_1"); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestFailedPromptClosesSession(t *testing.T) {
	fp := standardProcess()
	fp.errs = map[string]error{"session/prompt": errors.New("turn failed")}
	h := testHarness(fp)
	if _, err := h.Spawn(context.Background(), core.SpawnRequest{}); err == nil {
		t.Fatal("spawn succeeded after prompt failure")
	}
	if !fp.isClosed() {
		t.Fatal("failed session was not closed")
	}
	if alive, _ := h.IsAlive(context.Background(), "sess_1"); alive {
		t.Fatal("failed session still reports alive")
	}
}

func TestNotifyParentIsNoop(t *testing.T) {
	h := testHarness(standardProcess())
	if err := h.NotifyParent(context.Background(), "sess_1", "hello"); err != nil {
		t.Fatalf("notify parent: %v", err)
	}
}

func TestCollectTextAndPRExtraction(t *testing.T) {
	note := `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"see pull/17"}}}}`
	if got := collectText(json.RawMessage(note)); got != "see pull/17" {
		t.Fatalf("collectText = %q, want see pull/17", got)
	}
	other := `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","content":{"type":"text","text":"noise"}}}}`
	if got := collectText(json.RawMessage(other)); got != "" {
		t.Fatalf("collectText for tool call = %q, want empty", got)
	}
	if got := prFromText("opened pull/17 for review"); got != 17 {
		t.Fatalf("prFromText = %d, want 17", got)
	}
	if got := prFromText("no pr here"); got != 0 {
		t.Fatalf("prFromText = %d, want 0", got)
	}
}
