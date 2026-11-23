package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// adapterDoer implements opencode.Doer with a canned response, so the cmd
// adapter can be tested without a real server. The net/http import stays in
// the test file: the capabilities gate skips _test.go (ADR 0005).
type adapterDoer struct {
	requests []*http.Request
	fn       func(*http.Request) (*http.Response, error)
}

func (a *adapterDoer) Do(req *http.Request) (*http.Response, error) {
	a.requests = append(a.requests, req)
	if a.fn == nil {
		return nil, errors.New("adapterDoer: fn не задан")
	}
	return a.fn(req)
}

func writeServiceFile(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := filepath.Join(dir, "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewOpencodePortMissingService(t *testing.T) {
	t3152RestorePorts(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = old })
	opencodeHTTPDoer = &adapterDoer{}

	if port, err := newOpencodePort(); err == nil || port != nil {
		t.Fatalf("ожидалась ошибка без service.json, port=%v err=%v", port, err)
	}
}

func TestNewOpencodePortUsesPasswordFromServiceFile(t *testing.T) {
	t3152RestorePorts(t)
	writeServiceFile(t, `{"url":"http://127.0.0.1:49374/","pid":1,"version":"2.3.0","password":"secret"}`)

	doer := &adapterDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			Header:     http.Header{},
		}, nil
	}}
	old := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = old })
	opencodeHTTPDoer = doer

	port, err := newOpencodePort()
	if err != nil {
		t.Fatalf("newOpencodePort: %v", err)
	}
	if _, err := port.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(doer.requests))
	}
	user, pass, ok := doer.requests[0].BasicAuth()
	if !ok || user != "opencode" || pass != "secret" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}
	if got := doer.requests[0].URL.String(); got != "http://127.0.0.1:49374/api/agent" {
		t.Fatalf("неверный адрес запроса: %q", got)
	}
}

// ---------------------------------------------------------------------------
// T3.152 / ADR 0060 п3 and п4: narrow POST and fallback ports with stand-ins.
// ---------------------------------------------------------------------------

// t3152Creator is a stand-in for the T3.150 POST client. It records both calls
// without opening a socket, so the CLI side can be tested before that card
// merges into main.
type t3152Creator struct {
	creates []t3142CreateCall
	prompts []t3142PromptCall
}

func (c *t3152Creator) CreateSession(parentID, agent, title string, metadata map[string]string) (string, error) {
	c.creates = append(c.creates, t3142CreateCall{parentID, agent, title, metadata})
	return "sess-post", nil
}

func (c *t3152Creator) Prompt(sessionID, text string) error {
	c.prompts = append(c.prompts, t3142PromptCall{sessionID, text})
	return nil
}

func (c *t3152Creator) PromptWithOptions(context.Context, string, string, opencode.PromptOptions) error {
	return nil
}

func t3152RestorePorts(t *testing.T) {
	t.Helper()
	previousSpawn, previousDeliver := spawnSessionSpawner, deliverTarget
	t.Cleanup(func() {
		SetSessionSpawner(previousSpawn)
		SetDeliveryTarget(previousDeliver)
	})
}

func TestT3152SetSessionCreatorInstallsBothCommands(t *testing.T) {
	t3152RestorePorts(t)
	creator := &t3152Creator{}

	SetSessionCreator(creator)

	if spawnSessionSpawner != creator {
		t.Fatalf("spawn port = %#v, want the POST stand-in", spawnSessionSpawner)
	}
	if deliverTarget != creator {
		t.Fatalf("delivery port = %#v, want the POST stand-in", deliverTarget)
	}

	// The installed port must be the one both commands actually call, not a
	// copy held by the setter.
	id, err := spawnSessionSpawner.CreateSession("parent", "builder", "title", nil)
	if err != nil || id != "sess-post" {
		t.Fatalf("CreateSession = %q, %v", id, err)
	}
	if err := deliverTarget.Prompt("sess-post", "pointer"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(creator.creates) != 1 || len(creator.prompts) != 1 {
		t.Fatalf("stand-in calls: creates=%d prompts=%d", len(creator.creates), len(creator.prompts))
	}

	SetSessionCreator(nil)
	if spawnSessionSpawner != nil || deliverTarget != nil {
		t.Fatalf("nil сеттер не очистил порты: spawn=%v deliver=%v", spawnSessionSpawner, deliverTarget)
	}
}

func TestResolveOpencodeConfigReadsServiceFile(t *testing.T) {
	writeServiceFile(t, `{"url":"http://127.0.0.1:62000","password":"pw"}`)

	got, err := resolveOpencodeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://127.0.0.1:62000" || got.Password != "pw" {
		t.Fatalf("config = %+v, want service.json values", got)
	}
}

// ---------------------------------------------------------------------------
// T3.153: the real client has context-aware POST methods, so the CLI ports are
// installed through clientSessionCreator instead of a type assertion.
// ---------------------------------------------------------------------------

type recordedPost struct {
	method string
	path   string
	body   string
}

func TestW254ClientSessionCreatorPostsAndReturnsID(t *testing.T) {
	var posts []recordedPost
	doer := &adapterDoer{fn: func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		posts = append(posts, recordedPost{req.Method, req.URL.Path, string(body)})
		if req.URL.Path == "/api/session" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"id":"sess-created","agent":"general"}`)),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     http.Header{},
		}, nil
	}}
	client, err := opencode.New(opencode.Config{URL: "http://127.0.0.1:64000", Password: "pw"}, doer)
	if err != nil {
		t.Fatalf("opencode.New: %v", err)
	}
	creator := &livePort{client: client}

	id, err := creator.CreateSession("parent", "general", "T3.999", map[string]string{"task": "T3.999"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id != "sess-created" {
		t.Fatalf("id = %q, ожидался sess-created", id)
	}
	if err := creator.Prompt("sess-created", "pointer"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if len(posts) != 2 {
		t.Fatalf("POST-запросов %d, ожидалось 2", len(posts))
	}
	if posts[0].method != http.MethodPost || posts[0].path != "/api/session" {
		t.Fatalf("первый запрос = %+v", posts[0])
	}
	for _, field := range []string{`"parentID":"parent"`, `"agent":"general"`, `"title":"T3.999"`, `"task":"T3.999"`} {
		if !strings.Contains(posts[0].body, field) {
			t.Fatalf("в теле CreateSession нет %s: %s", field, posts[0].body)
		}
	}
	if posts[1].method != http.MethodPost || posts[1].path != "/api/session/sess-created/prompt" {
		t.Fatalf("второй запрос = %+v", posts[1])
	}
	if !strings.Contains(posts[1].body, `"text":"pointer"`) {
		t.Fatalf("в теле Prompt нет text: %s", posts[1].body)
	}
}

func TestW254NewOpencodePortInstallsClientPorts(t *testing.T) {
	t3152RestorePorts(t)
	writeServiceFile(t, `{"url":"http://127.0.0.1:65000/","password":"pw"}`)
	SetSessionSpawner(nil)
	SetDeliveryTarget(nil)
	oldDoer := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = oldDoer })
	doer := &adapterDoer{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"id":"sess-live"}`)),
			Header:     http.Header{},
		}, nil
	}}
	opencodeHTTPDoer = doer

	port, err := newOpencodePort()
	if err != nil {
		t.Fatalf("newOpencodePort: %v", err)
	}
	SetOpenCodePort(port)
	if spawnSessionSpawner == nil || deliverTarget == nil {
		t.Fatalf("порты не установлены: spawn=%v deliver=%v", spawnSessionSpawner, deliverTarget)
	}
	id, err := spawnSessionSpawner.CreateSession("parent", "general", "T3.999", nil)
	if err != nil || id != "sess-live" {
		t.Fatalf("CreateSession через установленный порт = %q, %v", id, err)
	}
	if err := deliverTarget.Prompt("sess-live", "pointer"); err != nil {
		t.Fatalf("Prompt через установленный порт: %v", err)
	}
	if len(doer.requests) != 2 {
		t.Fatalf("запросов %d, ожидалось 2", len(doer.requests))
	}
	if got := doer.requests[0].Method + " " + doer.requests[0].URL.String(); got != "POST http://127.0.0.1:65000/api/session" {
		t.Fatalf("первый запрос = %q", got)
	}
}
