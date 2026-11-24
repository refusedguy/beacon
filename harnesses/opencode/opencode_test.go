package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type recorder struct {
	mu       sync.Mutex
	sessions []map[string]any
	prompts  []map[string]any
}

func (r *recorder) addSession(body map[string]any) {
	r.mu.Lock()
	r.sessions = append(r.sessions, body)
	r.mu.Unlock()
}

func (r *recorder) addPrompt(body map[string]any) {
	r.mu.Lock()
	r.prompts = append(r.prompts, body)
	r.mu.Unlock()
}

func (r *recorder) lastPrompt() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.prompts) == 0 {
		return nil
	}
	return r.prompts[len(r.prompts)-1]
}

func decodeBody(r *http.Request) map[string]any {
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body
}

func testSpawnRequest() core.SpawnRequest {
	return core.SpawnRequest{
		Task:            core.Task{ID: "T-1", Title: "fix widget"},
		Workspace:       core.Workspace{Path: "/tmp/worktrees/widget", Branch: "feat/widget"},
		RunID:           core.RunID("run-7"),
		Prompt:          "implement the widget",
		ParentSessionID: "ses_parent",
	}
}

func TestSpawnSessionAndPromptBodies(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			rec.addSession(decodeBody(r))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"ses_42"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			rec.addPrompt(decodeBody(r))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"delivery":"steer"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	h := New(srv.URL, "model-x", "fixer", "")
	sess, err := h.Spawn(context.Background(), testSpawnRequest())
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if sess.ID != "ses_42" {
		t.Fatalf("session id = %q, want ses_42", sess.ID)
	}
	if len(rec.sessions) != 1 {
		t.Fatalf("session creates = %d, want 1", len(rec.sessions))
	}
	created := rec.sessions[0]
	if created["parentID"] != "ses_parent" {
		t.Errorf("parentID = %v, want ses_parent", created["parentID"])
	}
	if created["directory"] != "/tmp/worktrees/widget" {
		t.Errorf("directory = %v, want worktree path", created["directory"])
	}
	if created["title"] != "fix widget" {
		t.Errorf("title = %v", created["title"])
	}
	if created["agent"] != "fixer" {
		t.Errorf("agent = %v", created["agent"])
	}
	prompt := rec.lastPrompt()
	if prompt == nil {
		t.Fatal("no prompt captured")
	}
	if prompt["delivery"] != "steer" {
		t.Errorf("delivery = %v, want steer", prompt["delivery"])
	}
	if prompt["resume"] != true {
		t.Errorf("resume = %v, want true", prompt["resume"])
	}
	if id, ok := prompt["id"].(string); !ok || id == "" {
		t.Errorf("prompt id missing: %v", prompt["id"])
	}
	if prompt["text"] != "implement the widget" {
		t.Errorf("prompt text = %v", prompt["text"])
	}
}

func TestPerTaskAgentOverridesGlobalAgent(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			rec.addSession(decodeBody(r))
			_, _ = io.WriteString(w, `{"id":"ses_task"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	h := New(srv.URL, "model-x", "global-agent", "")
	req := testSpawnRequest()
	req.Task.WorkerAgent = "table-driven-worker"
	if _, err := h.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(rec.sessions) != 1 {
		t.Fatalf("session creates = %d, want 1", len(rec.sessions))
	}
	if got := rec.sessions[0]["agent"]; got != "table-driven-worker" {
		t.Fatalf("agent = %v, want per-task table-driven-worker", got)
	}

	rec.sessions = nil
	plain := testSpawnRequest()
	if _, err := h.Spawn(context.Background(), plain); err != nil {
		t.Fatalf("Spawn without override: %v", err)
	}
	if len(rec.sessions) != 1 {
		t.Fatalf("session creates = %d, want 1", len(rec.sessions))
	}
	if got := rec.sessions[0]["agent"]; got != "global-agent" {
		t.Fatalf("agent = %v, want global-agent", got)
	}
}

func TestPromptConflictIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			_, _ = io.WriteString(w, `{"id":"ses_9"}`)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"duplicate message id"}`)
	}))
	defer srv.Close()

	h := New(srv.URL)
	if _, err := h.Spawn(context.Background(), testSpawnRequest()); err != nil {
		t.Fatalf("Spawn with 409 prompt: %v", err)
	}
}

func TestWakeUsesStructuredRedactedPrompt(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			_, _ = io.WriteString(w, `{"id":"ses_9"}`)
			return
		}
		rec.addPrompt(decodeBody(r))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	h := New(srv.URL)
	secret := "ghp_" + strings.Repeat("a", 30)
	wake := core.WakeContext{
		RunID:     "run-7",
		PRNumber:  12,
		HeadSHA:   "abc123",
		Reason:    "checks failed",
		Excerpt:   secret + " " + strings.Repeat("x", 2000),
		Workspace: "/tmp/worktrees/widget",
		Branch:    "feat/widget",
		RebaseTry: 1,
		FixTry:    2,
	}
	if err := h.Wake(context.Background(), "ses_9", wake); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	prompt := rec.lastPrompt()
	if prompt == nil {
		t.Fatal("no prompt captured")
	}
	if prompt["delivery"] != "steer" || prompt["resume"] != true {
		t.Errorf("delivery/resume = %v/%v", prompt["delivery"], prompt["resume"])
	}
	text, _ := prompt["text"].(string)
	for _, want := range []string{"run=run-7", "pr=12", "checks failed", "rebase=1", "fix=2"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, secret) {
		t.Error("prompt text leaked secret")
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Error("prompt text missing redaction marker")
	}
	if len(text) > 1600 {
		t.Errorf("prompt text not truncated: %d bytes", len(text))
	}
}

func TestWatchIdleEmitsIdleEvent(t *testing.T) {
	connected := make(chan struct{})
	send := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			_, _ = io.WriteString(w, `{"id":"ses_42"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/event":
			once.Do(func() { close(connected) })
			<-send
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: session.idle\ndata: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses_42\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	h := New(srv.URL)
	if _, err := h.Spawn(context.Background(), testSpawnRequest()); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WatchIdle(ctx)

	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE subscription not established")
	}
	close(send)

	select {
	case ev := <-h.Events():
		if ev.Kind != "idle" {
			t.Errorf("kind = %q, want idle", ev.Kind)
		}
		if ev.RunID != core.RunID("run-7") {
			t.Errorf("runID = %q, want run-7", ev.RunID)
		}
		if ev.SessionID != "ses_42" {
			t.Errorf("sessionID = %q, want ses_42", ev.SessionID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no idle event received")
	}
}

func TestNotifyParentUsesQueueDeliveryWithoutResume(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_parent/prompt" {
			rec.addPrompt(decodeBody(r))
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	h := New(srv.URL)
	secret := "ghp_" + strings.Repeat("a", 30)
	if err := h.NotifyParent(context.Background(), "ses_parent", "Beacon: PR #42 run r1 state merged token="+secret); err != nil {
		t.Fatalf("NotifyParent: %v", err)
	}
	prompt := rec.lastPrompt()
	if prompt == nil {
		t.Fatal("no prompt captured")
	}
	if prompt["delivery"] != "queue" {
		t.Errorf("delivery = %v, want queue", prompt["delivery"])
	}
	if prompt["resume"] != false {
		t.Errorf("resume = %v, want false", prompt["resume"])
	}
	if id, ok := prompt["id"].(string); !ok || !strings.HasPrefix(id, "msg_") {
		t.Errorf("id = %v, want msg_ prefix", prompt["id"])
	}
	text, _ := prompt["text"].(string)
	if strings.Contains(text, secret) {
		t.Error("notification leaked secret")
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Error("notification missing redaction marker")
	}
}

func TestStopAndIsAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ses_42" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, `{"id":"ses_42"}`)
	}))
	defer srv.Close()

	h := New(srv.URL)
	alive, err := h.IsAlive(context.Background(), "ses_42")
	if err != nil || !alive {
		t.Fatalf("IsAlive = %v, %v", alive, err)
	}
	if err := h.Stop(context.Background(), "ses_42"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	alive, err = h.IsAlive(context.Background(), "missing")
	if err != nil || alive {
		t.Fatalf("IsAlive(missing) = %v, %v", alive, err)
	}
}

// TestSpawnCreatesOneSessionWithResolvedModel records every session create and
// model switch. A prompt that carries a model makes the fake server spin up a
// second, model-less session, the failure observed in the smoke test.
func TestSpawnUsesDispatchModelRef(t *testing.T) {
	var mu sync.Mutex
	var sessions, models, prompts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			mu.Lock()
			sessions = append(sessions, decodeBody(r))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"ses_worker"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/model"):
			mu.Lock()
			models = append(models, decodeBody(r))
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			mu.Lock()
			prompts = append(prompts, decodeBody(r))
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	want := map[string]any{"id": "hy4-flash-free", "providerID": "opencode", "variant": "default"}
	h := New(srv.URL, "paid-startup", "worker", "ses_parent")
	h.ModelRef = map[string]string{"id": "paid-startup", "providerID": "opencode"}
	req := testSpawnRequest()
	req.ModelRef = map[string]string{"id": "hy4-flash-free", "providerID": "opencode", "variant": "default"}
	sess, err := h.Spawn(context.Background(), req)
	if err != nil || sess.ID != "ses_worker" {
		t.Fatalf("Spawn = %q, %v", sess.ID, err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session creates = %d, want 1: %v", len(sessions), sessions)
	}
	if !reflect.DeepEqual(sessions[0]["model"], want) {
		t.Errorf("session model = %v, want %v", sessions[0]["model"], want)
	}
	if sessions[0]["parentID"] != "ses_parent" {
		t.Errorf("parentID = %v, want ses_parent", sessions[0]["parentID"])
	}
	if len(models) != 1 || !reflect.DeepEqual(models[0]["model"], want) {
		t.Fatalf("model switches = %v, want one with %v", models, want)
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	if _, ok := prompts[0]["model"]; ok {
		t.Errorf("prompt must not carry a model: %v", prompts[0])
	}
}

func TestSpawnCreatesOneSessionWithResolvedModel(t *testing.T) {
	var mu sync.Mutex
	var sessions, prompts []map[string]any
	var models []struct {
		path string
		body map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			body := decodeBody(r)
			mu.Lock()
			sessions = append(sessions, body)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"ses_worker"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/model"):
			body := decodeBody(r)
			mu.Lock()
			models = append(models, struct {
				path string
				body map[string]any
			}{r.URL.Path, body})
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			body := decodeBody(r)
			mu.Lock()
			prompts = append(prompts, body)
			if _, ok := body["model"]; ok {
				sessions = append(sessions, map[string]any{"model": nil})
			}
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ref := map[string]any{"id": "hy4-flash-free", "providerID": "opencode", "variant": "default"}
	h := New(srv.URL, "", "worker", "ses_parent")
	h.ModelRef = map[string]string{"id": "hy4-flash-free", "providerID": "opencode", "variant": "default"}
	sess, err := h.Spawn(context.Background(), testSpawnRequest())
	if err != nil || sess.ID != "ses_worker" {
		t.Fatalf("Spawn = %q, %v", sess.ID, err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session creates = %d, want 1: %v", len(sessions), sessions)
	}
	created := sessions[0]
	if got := created["model"]; !reflect.DeepEqual(got, ref) {
		t.Errorf("session model = %v, want %v", got, ref)
	}
	if created["parentID"] != "ses_parent" {
		t.Errorf("parentID = %v, want ses_parent", created["parentID"])
	}
	if len(models) != 1 || models[0].path != "/api/session/ses_worker/model" {
		t.Fatalf("model switches = %v, want one on ses_worker", models)
	}
	if !reflect.DeepEqual(models[0].body["model"], ref) {
		t.Errorf("model switch body = %v, want %v", models[0].body, ref)
	}
	if len(prompts) != 1 || prompts[0]["model"] != nil {
		t.Errorf("prompt must not carry a model: %v", prompts)
	}
}
