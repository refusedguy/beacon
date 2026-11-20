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
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = old })
	opencodeHTTPDoer = &adapterDoer{}

	if port, err := newOpencodePort(); err == nil || port != nil {
		t.Fatalf("ожидалась ошибка без service.json, port=%v err=%v", port, err)
	}
}

func TestNewOpencodePortUsesPasswordFromServiceFile(t *testing.T) {
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

// t3152Ensurer is a stand-in for the T3.151 fallback starter. It returns a
// registration as if it had spawned opencode serve on a free port.
type t3152Ensurer struct {
	config opencode.Config
	err    error
	calls  int
}

func (e *t3152Ensurer) EnsureServer(context.Context) (opencode.Config, error) {
	e.calls++
	return e.config, e.err
}

func t3152RestorePorts(t *testing.T) {
	t.Helper()
	previousSpawn, previousDeliver, previousEnsurer := spawnSessionSpawner, deliverTarget, installedServerEnsurer
	t.Cleanup(func() {
		SetSessionSpawner(previousSpawn)
		SetDeliveryTarget(previousDeliver)
		SetServerEnsurer(previousEnsurer)
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

func TestT3152ServerEnsurerFuncAdapter(t *testing.T) {
	t3152RestorePorts(t)
	want := opencode.Config{URL: "http://127.0.0.1:60000", Password: "pw"}
	fn := serverEnsurerFunc(func(context.Context) (opencode.Config, error) { return want, nil })
	SetServerEnsurer(fn)

	got, err := resolveOpencodeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}

func TestT3152ResolveOpencodeConfigPrefersEnsurer(t *testing.T) {
	t3152RestorePorts(t)
	// No service.json: the stand-in must be the only source of the config.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	want := opencode.Config{URL: "http://127.0.0.1:61000", Password: "fallback"}
	ensurer := &t3152Ensurer{config: want}
	SetServerEnsurer(ensurer)

	got, err := resolveOpencodeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
	if ensurer.calls != 1 {
		t.Fatalf("EnsureServer вызовов %d, ожидался 1", ensurer.calls)
	}
}

func TestT3152ResolveOpencodeConfigFallsBackToServiceFile(t *testing.T) {
	t3152RestorePorts(t)
	SetServerEnsurer(nil)
	writeServiceFile(t, `{"url":"http://127.0.0.1:62000","password":"pw"}`)

	got, err := resolveOpencodeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://127.0.0.1:62000" || got.Password != "pw" {
		t.Fatalf("config = %+v, want service.json values", got)
	}
}

func TestT3152NewOpencodePortUsesInstalledEnsurer(t *testing.T) {
	t3152RestorePorts(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldDoer := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = oldDoer })

	doer := &adapterDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			Header:     http.Header{},
		}, nil
	}}
	opencodeHTTPDoer = doer
	SetServerEnsurer(&t3152Ensurer{config: opencode.Config{
		URL:      "http://127.0.0.1:63000/",
		Password: "fallback",
	}})

	port, err := newOpencodePort()
	if err != nil {
		t.Fatalf("newOpencodePort: %v", err)
	}
	if _, err := port.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("запросов %d, ожидался 1", len(doer.requests))
	}
	if got := doer.requests[0].URL.String(); got != "http://127.0.0.1:63000/api/agent" {
		t.Fatalf("адрес запроса = %q, ожидался адрес от ensurer", got)
	}
}
