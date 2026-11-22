package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// heartbeatRecorder stands in for the durable session registry and records
// every Heartbeat call. It is safe for the heartbeat goroutine to use while
// the test reads its snapshot.
type heartbeatRecorder struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (r *heartbeatRecorder) Heartbeat(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return r.err
}

func (r *heartbeatRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func TestDaemonHeartbeatTouchesSession(t *testing.T) {
	r := &heartbeatRecorder{}
	if err := daemonHeartbeat(r, " ses_1 "); err != nil {
		t.Fatalf("daemonHeartbeat: %v", err)
	}
	got := r.snapshot()
	if len(got) != 1 || got[0] != "ses_1" {
		t.Fatalf("heartbeat ids = %v, want [ses_1]", got)
	}
}

func TestDaemonHeartbeatSkipsEmptyID(t *testing.T) {
	r := &heartbeatRecorder{}
	if err := daemonHeartbeat(r, "  "); err != nil {
		t.Fatalf("daemonHeartbeat: %v", err)
	}
	if got := r.snapshot(); len(got) != 0 {
		t.Fatalf("heartbeat ids = %v, want none", got)
	}
}

func TestDaemonHeartbeatSkipsNilPort(t *testing.T) {
	if err := daemonHeartbeat(nil, "ses_1"); err != nil {
		t.Fatalf("daemonHeartbeat with nil port: %v", err)
	}
}

func TestDaemonHeartbeatPropagatesStoreError(t *testing.T) {
	want := errors.New("touch failed")
	r := &heartbeatRecorder{err: want}
	if err := daemonHeartbeat(r, "ses_1"); !errors.Is(err, want) {
		t.Fatalf("daemonHeartbeat error = %v, want %v", err, want)
	}
}

func TestRunDaemonHeartbeatTicksAndStops(t *testing.T) {
	r := &heartbeatRecorder{}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDaemonHeartbeat(ctx, r, "ses_1", ticks)
	}()

	// An unbuffered send completes only once the loop has received the tick,
	// and Heartbeat runs in that same loop iteration before the next select.
	for i := 0; i < 2; i++ {
		select {
		case ticks <- time.Now():
		case <-done:
			cancel()
			t.Fatal("heartbeat loop exited before all ticks were delivered")
		}
	}

	cancel()
	<-done

	got := r.snapshot()
	if len(got) != 2 {
		t.Fatalf("heartbeat ids = %v, want two ticks", got)
	}
	for i, id := range got {
		if id != "ses_1" {
			t.Fatalf("heartbeat id %d = %q, want ses_1", i, id)
		}
	}

	select {
	case ticks <- time.Now():
		t.Fatal("heartbeat loop still receives ticks after cancel")
	default:
	}
}

// sessionStoreFake implements session.SessionStore without a database. It
// records Touch calls so a test can prove the registry reaches the store.
type sessionStoreFake struct {
	items   map[string]session.Session
	touched []string
}

func newSessionStoreFake() *sessionStoreFake {
	return &sessionStoreFake{items: make(map[string]session.Session)}
}

func (f *sessionStoreFake) SaveSession(s session.Session) error {
	f.items[s.ID] = s
	return nil
}

func (f *sessionStoreFake) GetSession(id string) (session.Session, bool, error) {
	s, ok := f.items[id]
	return s, ok, nil
}

func (f *sessionStoreFake) Touch(id string, _ time.Time) error {
	f.touched = append(f.touched, id)
	return nil
}

func TestDaemonHeartbeatTouchesThroughRegistry(t *testing.T) {
	st := newSessionStoreFake()
	st.items["ses_1"] = session.Session{ID: "ses_1", Status: session.SessionStatusActive}
	reg := session.NewSessionRegistry(st)

	if err := daemonHeartbeat(reg, "ses_1"); err != nil {
		t.Fatalf("daemonHeartbeat: %v", err)
	}
	if len(st.touched) != 1 || st.touched[0] != "ses_1" {
		t.Fatalf("store Touch calls = %v, want [ses_1]", st.touched)
	}
}

func TestEnsureDaemonSessionRecordsMissingSession(t *testing.T) {
	st := newSessionStoreFake()
	reg := session.NewSessionRegistry(st)

	if err := ensureDaemonSession(reg, " ses_9 "); err != nil {
		t.Fatalf("ensureDaemonSession: %v", err)
	}
	got, found, err := st.GetSession("ses_9")
	if err != nil || !found {
		t.Fatalf("session ses_9 found = %v, err = %v", found, err)
	}
	if got.Status != session.SessionStatusActive {
		t.Fatalf("status = %q, want %q", got.Status, session.SessionStatusActive)
	}
}

func TestEnsureDaemonSessionKeepsExistingSession(t *testing.T) {
	st := newSessionStoreFake()
	st.items["ses_1"] = session.Session{
		ID: "ses_1", Status: session.SessionStatusIdle,
		Metadata: map[string]string{"origin": "spawn"},
	}
	reg := session.NewSessionRegistry(st)

	if err := ensureDaemonSession(reg, "ses_1"); err != nil {
		t.Fatalf("ensureDaemonSession: %v", err)
	}
	got := st.items["ses_1"]
	if got.Status != session.SessionStatusIdle || got.Metadata["origin"] != "spawn" {
		t.Fatalf("existing session was rewritten: %+v", got)
	}
}

func TestEnsureDaemonSessionSkipsEmptyID(t *testing.T) {
	st := newSessionStoreFake()
	if err := ensureDaemonSession(session.NewSessionRegistry(st), "  "); err != nil {
		t.Fatalf("ensureDaemonSession: %v", err)
	}
	if len(st.items) != 0 {
		t.Fatalf("store has %d sessions, want 0", len(st.items))
	}
}

func TestDaemonHeartbeatZeroIntervalUsesDefault(t *testing.T) {
	previous := daemonHeartbeatInterval
	daemonHeartbeatInterval = 5 * time.Second
	defer func() { daemonHeartbeatInterval = previous }()

	for _, interval := range []time.Duration{0, -time.Second} {
		if got := daemonHeartbeatEffectiveInterval(interval); got != daemonHeartbeatInterval {
			t.Fatalf("effective interval for %v = %v, want default %v", interval, got, daemonHeartbeatInterval)
		}
	}
	if got := daemonHeartbeatEffectiveInterval(time.Second); got != time.Second {
		t.Fatalf("effective interval for a positive value = %v, want 1s", got)
	}
}

// wakePromptRecorder is the HTTP doer one deliverer uses. It records the
// request so a test can read the route and body the deliverer sent.
type wakePromptRecorder struct {
	calls  int
	method string
	path   string
	body   string
}

func (r *wakePromptRecorder) Do(req *http.Request) (*http.Response, error) {
	r.calls++
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	r.method = req.Method
	r.path = req.URL.Path
	r.body = string(data)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     http.Header{},
	}, nil
}

func TestDaemonWakeDelivererUsesHTTPWithRegistration(t *testing.T) {
	doer := &wakePromptRecorder{}
	runnerCalls := 0
	runner := func(context.Context, ...string) ([]byte, error) {
		runnerCalls++
		return nil, errors.New("CLI runner called with a live registration")
	}
	deliverer := daemonWakeDeliverer(func() (opencode.Config, error) {
		return opencode.Config{URL: "http://127.0.0.1:64000", Password: "pw"}, nil
	}, doer, runner)

	if err := deliverer(context.Background(), "ses_abc", "verdict text"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("CLI runner called %d times, want 0", runnerCalls)
	}
	if doer.calls != 1 {
		t.Fatalf("HTTP requests = %d, want 1", doer.calls)
	}
	if doer.method != http.MethodPost || doer.path != "/api/session/ses_abc/prompt" {
		t.Fatalf("request = %s %s, want POST /api/session/ses_abc/prompt", doer.method, doer.path)
	}
	var body struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   bool   `json:"resume"`
	}
	if err := json.Unmarshal([]byte(doer.body), &body); err != nil {
		t.Fatalf("decode body %q: %v", doer.body, err)
	}
	if body.Text != "verdict text" || body.Delivery != "steer" || !body.Resume {
		t.Fatalf("body = %+v, want text=verdict text, delivery=steer, resume=true", body)
	}
}

func TestDaemonWakeDelivererMarksCLIFallback(t *testing.T) {
	noRegistration := errors.New("service.json missing")
	runnerCalls := 0
	runner := func(context.Context, ...string) ([]byte, error) {
		runnerCalls++
		return nil, errors.New("opencode api failed")
	}
	deliverer := daemonWakeDeliverer(func() (opencode.Config, error) {
		return opencode.Config{}, noRegistration
	}, nil, runner)

	err := deliverer(context.Background(), "ses_abc", "verdict text")
	if err == nil {
		t.Fatal("deliver without a registration returned nil error")
	}
	if runnerCalls != 1 {
		t.Fatalf("CLI runner called %d times, want 1", runnerCalls)
	}
	if !strings.Contains(err.Error(), "CLI fallback") {
		t.Fatalf("error = %q, want a marked CLI fallback", err)
	}
	if !errors.Is(err, noRegistration) {
		t.Fatalf("error = %v, want the registration cause", err)
	}
}
