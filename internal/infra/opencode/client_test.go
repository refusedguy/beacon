package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDoer replaces the HTTP transport. No real server is started.
type fakeDoer struct {
	mu       sync.Mutex
	requests []*http.Request
	fn       func(*http.Request) (*http.Response, error)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.fn == nil {
		return nil, errors.New("fakeDoer: fn не задан")
	}
	return f.fn(req)
}

func (f *fakeDoer) snapshot() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

func fakeResponse(body string, code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d test", code),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func newTestClient(t *testing.T, doer Doer) *Client {
	t.Helper()
	c, err := New(Config{URL: "http://127.0.0.1:49374", Password: "pw"}, doer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := filepath.Join(dir, "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"url":"http://127.0.0.1:49374","pid":123,"version":"2.3.0","password":"secret"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.URL != "http://127.0.0.1:49374" || cfg.Password != "secret" {
		t.Fatalf("неверный конфиг: %+v", cfg)
	}
}

func TestDiscoverErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		if _, err := Discover(); err == nil {
			t.Fatal("ожидалась ошибка для отсутствующего файла")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_STATE_HOME", dir)
		path := filepath.Join(dir, "opencode", "service.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"url":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Discover(); err == nil {
			t.Fatal("ожидалась ошибка для неверного JSON")
		}
	})

	t.Run("empty url", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		old := readFile
		t.Cleanup(func() { readFile = old })
		readFile = func(string) ([]byte, error) {
			return []byte(`{"url":"  ","password":"p"}`), nil
		}
		if _, err := Discover(); err == nil {
			t.Fatal("ожидалась ошибка для пустого url")
		}
	})
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, raw := range []string{"", "ftp://127.0.0.1:1", "http://"} {
		if _, err := New(Config{URL: raw}, &fakeDoer{}); err == nil {
			t.Fatalf("ожидалась ошибка для url %q", raw)
		}
	}
}

func TestListAgentsUsesBasicAuthAndEnvelope(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		body := `{"location":{"directory":"/w"},"data":[{"id":"build","name":"Build","mode":"subagent"}]}`
		return fakeResponse(body, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "build" || agents[0].Mode != "subagent" {
		t.Fatalf("неверные агенты: %+v", agents)
	}

	reqs := doer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(reqs))
	}
	if reqs[0].URL.Path != "/api/agent" {
		t.Fatalf("неверный путь %q", reqs[0].URL.Path)
	}
	user, pass, ok := reqs[0].BasicAuth()
	if !ok || user != "opencode" || pass != "pw" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}
}

func TestSessionsTTL(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	calls := 0
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		calls++
		return fakeResponse(`{"data":[{"id":"s1","agent":"build","parentID":"root"}]}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)
	c.now = func() time.Time { return now }
	c.sessionTTL = 2 * time.Second

	first, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	second, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ожидался 1 HTTP-запрос в пределах TTL, получено %d", calls)
	}
	if len(first) != 1 || first[0].Agent != "build" || first[0].ParentID != "root" {
		t.Fatalf("неверные сессии: %+v", first)
	}
	first[0].Agent = "mutated"
	if second[0].Agent != "build" {
		t.Fatal("кэш вернул тот же срез, а не копию")
	}

	now = now.Add(3 * time.Second)
	if _, err := c.Sessions(context.Background()); err != nil {
		t.Fatalf("Sessions после TTL: %v", err)
	}
	if calls != 2 {
		t.Fatalf("ожидался повторный HTTP-запрос после TTL, получено %d", calls)
	}
}

// T3.148: GET /api/session reports model either as a v2 object or as a legacy
// string. Both must parse into the same model id through the real Sessions path.
func TestSessionsModelObjectAndString(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string // raw JSON value of the "model" field
		want  string
	}{
		{
			name:  "v2 object",
			model: `{"id":"hy4-flash-free","providerID":"opencode","variant":"default"}`,
			want:  "hy4-flash-free",
		},
		{
			name:  "legacy string",
			model: `"hy4-flash-free"`,
			want:  "hy4-flash-free",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"data":[{"id":"s1","agent":"build","model":%s}]}`, tc.model)
			doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
				return fakeResponse(body, http.StatusOK), nil
			}}
			c := newTestClient(t, doer)
			c.sessionTTL = 0

			sessions, err := c.Sessions(context.Background())
			if err != nil {
				t.Fatalf("Sessions: %v", err)
			}
			if len(sessions) != 1 || string(sessions[0].ModelID) != tc.want {
				t.Fatalf("неверные сессии: %+v (ожидали model=%q)", sessions, tc.want)
			}
		})
	}
}

func TestModelIDUnmarshalRejectsOtherTypes(t *testing.T) {
	for _, raw := range []string{`123`, `true`, `[]`} {
		var m modelID
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			t.Fatalf("ожидалась ошибка для %s", raw)
		}
	}
}

func TestSessionsNoCacheWhenTTLZero(t *testing.T) {
	calls := 0
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		calls++
		return fakeResponse(`{"data":[]}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)
	c.sessionTTL = 0
	for i := 0; i < 2; i++ {
		if _, err := c.Sessions(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("ожидалось 2 запроса при выключенном кэше, получено %d", calls)
	}
}

func TestConfigReturnsRawBody(t *testing.T) {
	const body = `{"data":{"theme":"dark"}}`
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(body, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	raw, err := c.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("неверное тело: %s", raw)
	}
	reqs := doer.snapshot()
	if len(reqs) != 1 || reqs[0].URL.Path != "/api/config" {
		t.Fatalf("неверный путь: %+v", reqs)
	}
}

func TestNon2xxIsError(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{"error":"unauthorized"}`, http.StatusUnauthorized), nil
	}}
	c := newTestClient(t, doer)

	_, err := c.ListAgents(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка для 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("в ошибке нет кода: %v", err)
	}
}

func TestEventStreamParsesAndUsesTypes(t *testing.T) {
	sse := "event: session.status\n" +
		`data: {"type":"session.status","properties":{"sessionID":"s1"}}` + "\n\n" +
		`data: {"type":"session.idle","properties":{}}` + "\n\n"
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(sse, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)
	c.reconnectWait = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := c.EventStream(ctx, []string{"session.status", "session.execution.started"})

	var got []Event
	for i := 0; i < 2; i++ {
		select {
		case r := <-ch:
			if r.Err != nil {
				t.Fatalf("event error: %v", r.Err)
			}
			got = append(got, r.Event)
		case <-time.After(2 * time.Second):
			t.Fatal("таймаут ожидания события")
		}
	}
	if got[0].Type != "session.status" || got[1].Type != "session.idle" {
		t.Fatalf("неверные события: %+v", got)
	}
	if !strings.Contains(string(got[0].Properties), `"sessionID":"s1"`) {
		t.Fatalf("неверные properties: %s", got[0].Properties)
	}

	reqs := doer.snapshot()
	if len(reqs) == 0 || reqs[0].URL.Path != "/api/event" {
		t.Fatalf("неверный путь SSE: %+v", reqs)
	}
	if got := reqs[0].URL.Query().Get("types"); got != "session.status,session.execution.started" {
		t.Fatalf("неверный types: %q", got)
	}

	cancel()
	waitClosed(t, ch)
}

func TestEventStreamReconnects(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		name := "first"
		if n > 1 {
			name = "second"
		}
		return fakeResponse(fmt.Sprintf(`data: {"type":%q,"properties":{}}`+"\n\n", name), http.StatusOK), nil
	}}
	c := newTestClient(t, doer)
	c.reconnectWait = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := c.EventStream(ctx, nil)

	var types []string
	for len(types) < 2 {
		select {
		case r := <-ch:
			if r.Err != nil {
				t.Fatalf("event error: %v", r.Err)
			}
			types = append(types, r.Event.Type)
		case <-time.After(2 * time.Second):
			t.Fatal("переподключение не дало второе событие")
		}
	}
	if types[0] != "first" || types[1] != "second" {
		t.Fatalf("неверный порядок после реконнекта: %v", types)
	}
}

func TestEventStreamGracefulCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       pr,
			Header:     http.Header{},
		}, nil
	}}
	c := newTestClient(t, doer)

	ctx, cancel := context.WithCancel(context.Background())
	ch := c.EventStream(ctx, nil)
	cancel()
	waitClosed(t, ch)
}

func TestCreateSessionSendsPostWithAgent(t *testing.T) {
	var body []byte
	doer := &fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		const out = `{"data":{"id":"s2","parentID":"root","agent":"build","title":"task","location":{"directory":"/w"}}}`
		return fakeResponse(out, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	session, err := c.CreateSession(context.Background(), "root", "build", "task",
		map[string]string{"watchID": "w1", "obligationID": "o1"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID != "s2" || session.ParentID != "root" || session.Agent != "build" || session.Title != "task" {
		t.Fatalf("неверная сессия: %+v", session)
	}
	if session.Location.Directory != "/w" {
		t.Fatalf("неверная location: %+v", session.Location)
	}

	reqs := doer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodPost || req.URL.Path != "/api/session" {
		t.Fatalf("неверный запрос: %s %s", req.Method, req.URL.Path)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("неверный Content-Type: %q", ct)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "opencode" || pass != "pw" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("неверный JSON тела: %v (%s)", err, body)
	}
	if payload["agent"] != "build" {
		t.Fatalf("agent в теле: %v", payload["agent"])
	}
	if payload["parentID"] != "root" || payload["title"] != "task" {
		t.Fatalf("неверные поля тела: %v", payload)
	}
	meta, ok := payload["metadata"].(map[string]any)
	if !ok || meta["watchID"] != "w1" || meta["obligationID"] != "o1" {
		t.Fatalf("неверный metadata: %v", payload["metadata"])
	}
}

// T3.999: v2.3.0 wraps POST /api/session in {"data":{...}}. Without unwrapping
// the envelope the adapter gets a zero session and reports an empty sessionID.
func TestCreateSessionUnwrapsDataEnvelope(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		const out = `{"data":{"id":"ses_abc123","parentID":"ses_parent","agent":"general","title":"T3.999","location":{"directory":"/w"}},"location":{"directory":"/w"}}`
		return fakeResponse(out, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	session, err := c.CreateSession(context.Background(), "ses_parent", "general", "T3.999", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID != "ses_abc123" {
		t.Fatalf("ID из envelope data = %q, хочется ses_abc123", session.ID)
	}
	if session.ParentID != "ses_parent" || session.Agent != "general" || session.Title != "T3.999" {
		t.Fatalf("неверная сессия из envelope: %+v", session)
	}
}

func TestCreateSessionErrors(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return fakeResponse(`{"error":"bad request"}`, http.StatusBadRequest), nil
		}}
		c := newTestClient(t, doer)
		if _, err := c.CreateSession(context.Background(), "", "build", "", nil); err == nil {
			t.Fatal("ожидалась ошибка для 400")
		} else if !strings.Contains(err.Error(), "400") {
			t.Fatalf("в ошибке нет кода: %v", err)
		}
	})

	t.Run("transport", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connect refused")
		}}
		c := newTestClient(t, doer)
		if _, err := c.CreateSession(context.Background(), "", "build", "", nil); err == nil {
			t.Fatal("ожидалась ошибка транспорта")
		}
	})

	t.Run("bad json", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return fakeResponse(`not json`, http.StatusOK), nil
		}}
		c := newTestClient(t, doer)
		if _, err := c.CreateSession(context.Background(), "", "build", "", nil); err == nil {
			t.Fatal("ожидалась ошибка разбора ответа")
		}
	})
}

func TestDeleteSessionSendsDeleteWithPathAndAuth(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	if err := c.DeleteSession(context.Background(), "ses_42"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	reqs := doer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodDelete || req.URL.Path != "/api/session/ses_42" {
		t.Fatalf("неверный запрос: %s %s", req.Method, req.URL.Path)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "opencode" || pass != "pw" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}
}

func TestDeleteSessionEscapesSessionID(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	if err := c.DeleteSession(context.Background(), "s/1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	reqs := doer.snapshot()
	if len(reqs) != 1 || !strings.Contains(reqs[0].URL.EscapedPath(), "%2F") {
		t.Fatalf("sessionID не экранирован: %+v", reqs)
	}
}

func TestDeleteSessionErrors(t *testing.T) {
	t.Run("404", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return fakeResponse(`{"error":"session not found"}`, http.StatusNotFound), nil
		}}
		c := newTestClient(t, doer)
		err := c.DeleteSession(context.Background(), "ses_missing")
		if err == nil {
			t.Fatal("ожидалась ошибка для 404")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Fatalf("в ошибке нет кода: %v", err)
		}
	})

	t.Run("empty session id", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			t.Fatal("пустой sessionID не должен вызывать HTTP")
			return nil, nil
		}}
		c := newTestClient(t, doer)
		if err := c.DeleteSession(context.Background(), ""); err == nil {
			t.Fatal("ожидалась ошибка для пустого sessionID")
		}
		if got := len(doer.snapshot()); got != 0 {
			t.Fatalf("ожидалось 0 HTTP-запросов, получено %d", got)
		}
	})
}

func TestPromptSendsPostWithoutAgent(t *testing.T) {
	var body []byte
	doer := &fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return fakeResponse(`{}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	if err := c.Prompt(context.Background(), "s2", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	reqs := doer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodPost || req.URL.Path != "/api/session/s2/prompt" {
		t.Fatalf("неверный запрос: %s %s", req.Method, req.URL.Path)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "opencode" || pass != "pw" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("неверный JSON тела: %v (%s)", err, body)
	}
	if payload["text"] != "hello" {
		t.Fatalf("неверный text: %v", payload["text"])
	}
	if _, present := payload["agent"]; present {
		t.Fatalf("agent не должен присутствовать в теле prompt: %s", body)
	}
}

func TestStartPromptOptions(t *testing.T) {
	opts := StartPromptOptions("obligation-42")
	if opts.MessageID != "msg_obligation-42" {
		t.Fatalf("MessageID = %q, want msg_obligation-42", opts.MessageID)
	}
	if opts.MessageID != MessageIDForObligation("obligation-42") {
		t.Fatalf("MessageID не детерминирован: %q", opts.MessageID)
	}
	if opts.Delivery != "steer" || !opts.Resume {
		t.Fatalf("стартовые опции = %+v, want delivery=steer resume=true", opts)
	}
}

func TestPromptWithOptionsPostsMessageIDDeliveryAndResume(t *testing.T) {
	var body []byte
	doer := &fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return fakeResponse(`{}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	// Один и тот же messageID указывает на одну работу: id выводится из
	// obligationID, а не создаётся заново при каждом вызове (AUTOSP-02).
	opts := StartPromptOptions("obligation-42")
	if err := c.PromptWithOptions(context.Background(), "ses_created", "first turn", opts); err != nil {
		t.Fatalf("PromptWithOptions: %v", err)
	}

	reqs := doer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodPost || req.URL.Path != "/api/session/ses_created/prompt" {
		t.Fatalf("неверный запрос: %s %s", req.Method, req.URL.Path)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("неверный JSON тела: %v (%s)", err, body)
	}
	if payload["text"] != "first turn" {
		t.Fatalf("неверный text: %v", payload["text"])
	}
	id, ok := payload["id"].(string)
	if !ok || id != "msg_obligation-42" {
		t.Fatalf("id = %v, want msg_obligation-42", payload["id"])
	}
	if payload["delivery"] != "steer" {
		t.Fatalf("delivery = %v, want steer", payload["delivery"])
	}
	if payload["resume"] != true {
		t.Fatalf("resume = %v, want true", payload["resume"])
	}
	if _, present := payload["agent"]; present {
		t.Fatalf("agent не должен присутствовать в теле prompt: %s", body)
	}
	if _, present := payload["messageID"]; present {
		t.Fatalf("поле должно называться id, а не messageID: %s", body)
	}
}

func TestPromptEscapesSessionID(t *testing.T) {
	doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(`{}`, http.StatusOK), nil
	}}
	c := newTestClient(t, doer)

	if err := c.Prompt(context.Background(), "s/1", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	reqs := doer.snapshot()
	if len(reqs) != 1 || !strings.Contains(reqs[0].URL.EscapedPath(), "%2F") {
		t.Fatalf("sessionID не экранирован: %+v", reqs)
	}
}

func TestPromptErrors(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return fakeResponse(`{"error":"unauthorized"}`, http.StatusUnauthorized), nil
		}}
		c := newTestClient(t, doer)
		if err := c.Prompt(context.Background(), "s2", "hello"); err == nil {
			t.Fatal("ожидалась ошибка для 401")
		} else if !strings.Contains(err.Error(), "401") {
			t.Fatalf("в ошибке нет кода: %v", err)
		}
	})

	t.Run("transport", func(t *testing.T) {
		doer := &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connect refused")
		}}
		c := newTestClient(t, doer)
		if err := c.Prompt(context.Background(), "s2", "hello"); err == nil {
			t.Fatal("ожидалась ошибка транспорта")
		}
	})
}

func waitClosed(t *testing.T, ch <-chan EventResult) {
	t.Helper()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("канал не закрылся после отмены контекста")
		}
	}
}
