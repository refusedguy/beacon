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
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
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

// TestClientSessionCreatorAdaptsLiveClient proves the CLI write ports reach the
// real HTTP client: no-context calls become context calls, Session.ID becomes
// the returned string, and the wrapper is installable as every narrow port.
func TestClientSessionCreatorAdaptsLiveClient(t *testing.T) {
	writeServiceFile(t, `{"url":"http://127.0.0.1:49998/","password":"pw"}`)
	var bodies []string
	doer := &adapterDoer{fn: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(body))
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"data":{"id":"sess-live","parentID":"parent"}}`)),
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
	if _, ok := port.(sessionCreator); !ok {
		t.Fatalf("port не satisfы sessionCreator: %T", port)
	}
	if _, ok := port.(serverEnsurer); !ok {
		t.Fatalf("port не satisfы serverEnsurer: %T", port)
	}

	creator := port.(sessionCreator)
	id, err := creator.CreateSession("parent", "general", "title", map[string]string{"ai.beacon/taskID": "T3.999"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id != "sess-live" {
		t.Fatalf("session id = %q, want sess-live", id)
	}
	if err := creator.Prompt("sess-live", "go"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if len(doer.requests) != 2 {
		t.Fatalf("запросов %d, ожидалось 2", len(doer.requests))
	}
	if got := doer.requests[0].Method; got != http.MethodPost {
		t.Fatalf("метод CreateSession = %q", got)
	}
	if got := doer.requests[0].URL.String(); got != "http://127.0.0.1:49998/api/session" {
		t.Fatalf("адрес CreateSession = %q", got)
	}
	if got := doer.requests[1].URL.String(); got != "http://127.0.0.1:49998/api/session/sess-live/prompt" {
		t.Fatalf("адрес Prompt = %q", got)
	}
	if !strings.Contains(bodies[0], `"agent":"general"`) || !strings.Contains(bodies[1], `"text":"go"`) {
		t.Fatalf("тела запросов: %q", bodies)
	}
}

// TestWiredDeliverIsNotUnsupported proves the adapter installed by
// wireOpenCodePorts is the one deliver calls: a ready verdict reaches a steer
// write instead of the honest unsupported outcome of an unwired target.
func TestWiredDeliverIsNotUnsupported(t *testing.T) {
	t3145RestorePorts(t)
	t.Cleanup(func() { SetServerEnsurer(nil) })
	writeServiceFile(t, `{"url":"http://127.0.0.1:49997/","password":"pw"}`)
	doer := &adapterDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     http.Header{},
		}, nil
	}}
	oldDoer := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = oldDoer })
	opencodeHTTPDoer = doer

	wireOpenCodePorts()
	if deliverTarget == nil {
		t.Fatal("wireOpenCodePorts не установил deliverTarget")
	}

	oldSnap, oldSleep := deliverSnapshot, deliverSleep
	t.Cleanup(func() { deliverSnapshot, deliverSleep = oldSnap, oldSleep })
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-wired", domain.VerdictReady,
			deliverSub(domain.RoleOrchestrator, "sess-live")),
	}}
	deliverSnapshot = func() (mcp.StateSnapshot, error) { return snap, nil }
	deliverSleep = func(time.Duration) {}

	c, out, errOut, _ := t06NewCLI(t, t06NoChild(t))
	if code := c.Run([]string{"deliver", "w-wired"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "delivery=steer") {
		t.Fatalf("stdout = %q, want delivery=steer, not unsupported", out.String())
	}
	if len(doer.requests) != 1 || !strings.HasSuffix(doer.requests[0].URL.Path, "/sess-live/prompt") {
		t.Fatalf("запросы = %d, первый = %v", len(doer.requests), doer.requests)
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
