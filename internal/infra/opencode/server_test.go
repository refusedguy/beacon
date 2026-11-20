package opencode

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeProcess replaces the child handle in tests.
type fakeProcess struct {
	done     chan struct{}
	killed   chan struct{}
	doneOnce sync.Once
	killOnce sync.Once
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{done: make(chan struct{}), killed: make(chan struct{})}
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }

// exit simulates the child ending on its own.
func (p *fakeProcess) exit() {
	p.doneOnce.Do(func() { close(p.done) })
}

func (p *fakeProcess) Kill() error {
	p.killOnce.Do(func() { close(p.killed) })
	p.exit()
	return nil
}

func installStartServer(t *testing.T, fn serveRunner) {
	t.Helper()
	old := startServer
	startServer = fn
	t.Cleanup(func() { startServer = old })
}

func writeServiceFile(t *testing.T, stateHome, body string) {
	t.Helper()
	path := filepath.Join(stateHome, "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startHealthServer binds a real HTTP server to the port that EnsureServer
// chose. The handler accepts only the password from the spawn request.
func startHealthServer(t *testing.T, req spawnRequest, onHealth func()) *fakeProcess {
	t.Helper()
	addr := net.JoinHostPort(req.host, strconv.Itoa(req.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("не удалось слушать %s: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthPath {
			http.NotFound(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != authUser || pass != req.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if onHealth != nil {
			onHealth()
		}
		w.WriteHeader(http.StatusOK)
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return newFakeProcess()
}

func TestEnsureServerUsesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	writeServiceFile(t, dir, `{"url":"http://127.0.0.1:49374","password":"secret"}`)

	called := false
	installStartServer(t, func(spawnRequest) (spawnedProcess, error) {
		called = true
		return nil, errors.New("спавн не должен вызываться")
	})

	cfg, err := EnsureServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if called {
		t.Fatal("spawn вызван при наличии service.json")
	}
	if cfg.URL != "http://127.0.0.1:49374" || cfg.Password != "secret" {
		t.Fatalf("неверный конфиг: %+v", cfg)
	}
}

func TestEnsureServerSpawnsOnMissingConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var got spawnRequest
	callCount := 0
	var healthCalls atomic.Int32

	installStartServer(t, func(req spawnRequest) (spawnedProcess, error) {
		callCount++
		got = req
		return startHealthServer(t, req, func() { healthCalls.Add(1) }), nil
	})

	cfg, err := EnsureServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("spawn вызван %d раз, ожидался 1", callCount)
	}
	if got.host != "127.0.0.1" || got.port == 0 || got.password == "" {
		t.Fatalf("неверный запрос на спавн: %+v", got)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d", got.port)
	if cfg.URL != wantURL || cfg.Password != got.password {
		t.Fatalf("неверный конфиг: %+v, ожидался url=%s", cfg, wantURL)
	}
	if n := healthCalls.Load(); n < 1 {
		t.Fatalf("health-запросов: %d, ожидался хотя бы 1", n)
	}
}

func TestEnsureServerFailsWhenProcessExits(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	proc := newFakeProcess()
	proc.exit()
	installStartServer(t, func(spawnRequest) (spawnedProcess, error) {
		return proc, nil
	})

	start := time.Now()
	_, err := EnsureServer(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка при завершении процесса")
	}
	if !strings.Contains(err.Error(), "завершился до готовности") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("EnsureServer ждал дольше 5с: %v", time.Since(start))
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("процесс не убит после неудачи")
	}
}

func TestEnsureServerStopsOnContextCancel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var proc *fakeProcess
	installStartServer(t, func(req spawnRequest) (spawnedProcess, error) {
		// Сервер отвечает 503, поэтому готовность не наступает.
		ln, err := net.Listen("tcp", net.JoinHostPort(req.host, strconv.Itoa(req.port)))
		if err != nil {
			return nil, err
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		_ = srv.Listener.Close()
		srv.Listener = ln
		srv.Start()
		t.Cleanup(srv.Close)
		proc = newFakeProcess()
		return proc, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := EnsureServer(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидался context.DeadlineExceeded, получено %v", err)
	}
	if proc == nil {
		t.Fatal("spawn не вызван")
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("процесс не убит по отмене контекста")
	}
}

// scopeTestClock is an injectable domain.Clock so TTL and idle expiry are
// deterministic and never depend on a sleeping test.
type scopeTestClock struct {
	mu sync.Mutex
	at time.Time
}

func newScopeTestClock() *scopeTestClock {
	return &scopeTestClock{at: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *scopeTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *scopeTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func scopeTestBinding(session string) ScopeBinding {
	return ScopeBinding{
		Environment:  "prod",
		ParentThread: "thread-1",
		Provider:     "opencode-1",
		Session:      session,
	}
}

func scopeTestCeiling(scopes ...domain.Scope) domain.ScopeMask {
	var m domain.ScopeMask
	for _, sc := range scopes {
		m |= sc.Flag()
	}
	return m
}

func TestScopeTokenRightsSeparation(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)

	raw, err := store.Issue(ScopeTokenIssue{
		Binding: scopeTestBinding("ses_a"),
		Ceiling: scopeTestCeiling(domain.ScopeWake, domain.ScopeRead),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if binding, err := store.AuthorizeWake(raw, "ses_a"); err != nil {
		t.Errorf("wake must be allowed: %v", err)
	} else if binding.Session != "ses_a" || binding.Environment != "prod" {
		t.Errorf("wrong binding returned: %+v", binding)
	}
	if _, err := store.AuthorizeRead(raw, "ses_a"); err != nil {
		t.Errorf("read must be allowed: %v", err)
	}
	if _, err := store.AuthorizeStop(raw, "ses_a"); !errors.Is(err, ErrScopeDenied) {
		t.Errorf("stop without the right = %v, want ErrScopeDenied", err)
	}

	stopOnly, err := store.Issue(ScopeTokenIssue{
		Binding: scopeTestBinding("ses_b"),
		Ceiling: scopeTestCeiling(domain.ScopeStop),
	})
	if err != nil {
		t.Fatalf("Issue(stop only): %v", err)
	}
	if _, err := store.AuthorizeStop(stopOnly, "ses_b"); err != nil {
		t.Errorf("stop must be allowed for stop-only token: %v", err)
	}
	if _, err := store.AuthorizeWake(stopOnly, "ses_b"); !errors.Is(err, ErrScopeDenied) {
		t.Errorf("wake for stop-only token = %v, want ErrScopeDenied", err)
	}
	if _, err := store.AuthorizeRead(stopOnly, "ses_b"); !errors.Is(err, ErrScopeDenied) {
		t.Errorf("read for stop-only token = %v, want ErrScopeDenied", err)
	}
}

func TestScopeTokenSessionBinding(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)
	all := scopeTestCeiling(domain.ScopeWake, domain.ScopeRead, domain.ScopeStop)

	raw, err := store.Issue(ScopeTokenIssue{Binding: scopeTestBinding("ses_a"), Ceiling: all})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// A token from another session cannot act on this session's obligation.
	if _, err := store.AuthorizeStop(raw, "ses_other"); !errors.Is(err, ErrScopeSessionMismatch) {
		t.Errorf("cross-session stop = %v, want ErrScopeSessionMismatch", err)
	}
	if _, err := store.Authorize(raw, domain.Scope("delete"), "ses_a"); !errors.Is(err, ErrScopeDenied) {
		t.Errorf("unknown scope = %v, want ErrScopeDenied", err)
	}
	if _, err := store.Authorize(raw, domain.ScopeWake, "ses_a"); err != nil {
		t.Errorf("own-session wake must be allowed: %v", err)
	}
}

func TestScopeTokenTTLExpiry(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)
	all := scopeTestCeiling(domain.ScopeWake, domain.ScopeRead, domain.ScopeStop)

	raw, err := store.Issue(ScopeTokenIssue{
		Binding: scopeTestBinding("ses_a"),
		Ceiling: all,
		TTL:     time.Minute,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clock.Advance(59 * time.Second)
	if _, err := store.AuthorizeWake(raw, "ses_a"); err != nil {
		t.Fatalf("token still inside TTL was rejected: %v", err)
	}

	clock.Advance(2 * time.Second)
	if _, err := store.AuthorizeWake(raw, "ses_a"); !errors.Is(err, ErrScopeTokenExpired) {
		t.Fatalf("expired token = %v, want ErrScopeTokenExpired", err)
	}
	if len(store.tokens) != 0 {
		t.Fatalf("expired token remained in store: %d records", len(store.tokens))
	}
	if _, err := store.AuthorizeWake(raw, "ses_a"); !errors.Is(err, ErrScopeTokenUnknown) {
		t.Fatalf("removed token = %v, want ErrScopeTokenUnknown", err)
	}
}

func TestScopeTokenIdleExpiry(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)
	all := scopeTestCeiling(domain.ScopeWake, domain.ScopeRead, domain.ScopeStop)

	raw, err := store.Issue(ScopeTokenIssue{
		Binding: scopeTestBinding("ses_a"),
		Ceiling: all,
		TTL:     time.Hour,
		IdleTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Activity inside the idle window refreshes lastUsed.
	clock.Advance(50 * time.Second)
	if _, err := store.AuthorizeRead(raw, "ses_a"); err != nil {
		t.Fatalf("token inside idle window was rejected: %v", err)
	}
	clock.Advance(50 * time.Second)
	if _, err := store.AuthorizeWake(raw, "ses_a"); err != nil {
		t.Fatalf("token after refresh was rejected: %v", err)
	}

	// More than the idle window without activity expires it, even though the
	// absolute TTL is still far away.
	clock.Advance(61 * time.Second)
	if _, err := store.AuthorizeWake(raw, "ses_a"); !errors.Is(err, ErrScopeTokenIdle) {
		t.Fatalf("idle token = %v, want ErrScopeTokenIdle", err)
	}
}

func TestScopeTokenRevokeBySession(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)
	all := scopeTestCeiling(domain.ScopeWake, domain.ScopeRead, domain.ScopeStop)

	a1, err := store.Issue(ScopeTokenIssue{Binding: scopeTestBinding("ses_a"), Ceiling: all})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := store.Issue(ScopeTokenIssue{Binding: scopeTestBinding("ses_a"), Ceiling: all})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Issue(ScopeTokenIssue{Binding: scopeTestBinding("ses_b"), Ceiling: all})
	if err != nil {
		t.Fatal(err)
	}

	if n := store.RevokeSession("ses_a"); n != 2 {
		t.Fatalf("RevokeSession(ses_a) = %d, want 2", n)
	}
	if n := store.RevokeSession("ses_a"); n != 0 {
		t.Fatalf("second RevokeSession = %d, want 0", n)
	}
	for _, raw := range []string{a1, a2} {
		if _, err := store.AuthorizeWake(raw, "ses_a"); !errors.Is(err, ErrScopeTokenUnknown) {
			t.Errorf("revoked token = %v, want ErrScopeTokenUnknown", err)
		}
	}
	if _, err := store.AuthorizeWake(b, "ses_b"); err != nil {
		t.Errorf("other session's token was revoked too: %v", err)
	}
}

func TestScopeTokenRawNeverStored(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)

	raw, err := store.Issue(ScopeTokenIssue{
		Binding: scopeTestBinding("ses_a"),
		Ceiling: scopeTestCeiling(domain.ScopeWake, domain.ScopeRead, domain.ScopeStop),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if raw == "" {
		t.Fatal("Issue returned an empty token")
	}

	if _, ok := store.tokens[raw]; ok {
		t.Fatal("raw token was used as a store key")
	}
	if _, ok := store.tokens[scopeTokenDigest(raw)]; !ok {
		t.Fatal("digest key was not stored")
	}
	if dump := fmt.Sprintf("%+v", store.tokens); strings.Contains(dump, raw) {
		t.Fatalf("raw token leaked into state: %s", dump)
	}
	diags := store.Diagnostics()
	if len(diags) != 1 {
		t.Fatalf("Diagnostics() = %d entries, want 1", len(diags))
	}
	if dump := fmt.Sprintf("%+v", diags); strings.Contains(dump, raw) {
		t.Fatalf("raw token leaked into diagnostics: %s", dump)
	}
	if len(diags[0].Scopes) != 3 {
		t.Fatalf("diagnostic scopes = %v, want all three", diags[0].Scopes)
	}
}

func TestScopeTokenIssueValidation(t *testing.T) {
	clock := newScopeTestClock()
	store := NewScopeTokenStore(clock, time.Hour, time.Hour)
	ceiling := scopeTestCeiling(domain.ScopeWake)

	for _, in := range []ScopeTokenIssue{
		{Binding: scopeTestBinding("  "), Ceiling: ceiling},
		{Binding: scopeTestBinding("ses_a"), Ceiling: domain.ScopeMask(0xff)},
	} {
		if _, err := store.Issue(in); !errors.Is(err, ErrScopeInvalidIssue) {
			t.Errorf("Issue(%+v) = %v, want ErrScopeInvalidIssue", in, err)
		}
	}
	if len(store.tokens) != 0 {
		t.Fatalf("invalid issues wrote %d records", len(store.tokens))
	}
}
