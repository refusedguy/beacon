package opencode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// serveHost is the loopback address of a spawned server. Beacon never
	// exposes it outside the local machine (ADR 0060 п3).
	serveHost = "127.0.0.1"

	// healthPath is an authenticated route that answers 200 only after the
	// API is live. GET /api/config works on opencode v2.3.0 (ADR 0060 п1).
	healthPath = "/api/config"

	// healthTimeout bounds the wait for a spawned server (ADR 0060 п3).
	healthTimeout = 30 * time.Second

	// healthPollInterval is the pause between health requests.
	healthPollInterval = 100 * time.Millisecond
)

// spawnRequest describes the child process to start. port comes from a short
// lived net.Listen on :0, so no port is hardcoded (ADR 0060 п3).
type spawnRequest struct {
	host     string
	port     int
	password string
}

// spawnedProcess is the handle to a started server. Done closes when the
// process exits. Kill stops it when readiness fails.
type spawnedProcess interface {
	Done() <-chan struct{}
	Kill() error
}

// serveRunner starts `opencode serve`. Tests replace it so no child process
// is spawned during a test.
type serveRunner func(req spawnRequest) (spawnedProcess, error)

// startServer is the injectable seam. Production uses startServeProcess.
var startServer serveRunner = startServeProcess

// healthDoer sends the health requests. A test can point it at a local
// httptest.Server without changing the readiness loop.
var healthDoer Doer = &http.Client{Timeout: 2 * time.Second}

// execProcess adapts *exec.Cmd to spawnedProcess.
type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *execProcess) Done() <-chan struct{} { return p.done }

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return errors.New("opencode serve: процесс не запущен")
	}
	return p.cmd.Process.Kill()
}

// startServeProcess starts opencode with the password in the environment.
// Output goes to io.Discard so a full pipe cannot block the child.
func startServeProcess(req spawnRequest) (spawnedProcess, error) {
	args := []string{"serve", "--hostname", req.host, "--port", strconv.Itoa(req.port)}
	cmd := exec.Command("opencode", args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "OPENCODE_SERVER_PASSWORD=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "OPENCODE_SERVER_PASSWORD="+req.password)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode serve: %w", err)
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// freePort reserves a loopback port and releases it. The caller then starts a
// server on that number.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", serveHost+":0")
	if err != nil {
		return 0, fmt.Errorf("не удалось выбрать свободный порт: %w", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("не удалось определить выбранный порт")
	}
	return addr.Port, nil
}

// randomPassword creates a password for the spawned server.
func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("не удалось сгенерировать пароль: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// healthReady performs one authenticated health request.
func healthReady(ctx context.Context, baseURL, password string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+healthPath, nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(authUser, password)
	resp, err := healthDoer.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// waitHealthy polls until the server answers, the process exits, ctx ends,
// or the 30 second budget runs out.
func waitHealthy(ctx context.Context, baseURL, password string, done <-chan struct{}) error {
	deadline := time.NewTimer(healthTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()

	for {
		if healthReady(ctx, baseURL, password) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("opencode serve завершился до готовности")
		case <-deadline.C:
			return fmt.Errorf("opencode serve не стал готов за %s", healthTimeout)
		case <-ticker.C:
		}
	}
}

// EnsureServer returns a Config for a live opencode HTTP API. An existing
// service.json wins. Without it, EnsureServer starts opencode serve on a free
// loopback port with a fresh password and waits for health (ADR 0060 п3).
func EnsureServer(ctx context.Context) (Config, error) {
	if cfg, err := ReadConfig(); err == nil {
		return cfg, nil
	}

	port, err := freePort()
	if err != nil {
		return Config{}, err
	}
	password, err := randomPassword()
	if err != nil {
		return Config{}, err
	}

	proc, err := startServer(spawnRequest{host: serveHost, port: port, password: password})
	if err != nil {
		return Config{}, err
	}
	baseURL := fmt.Sprintf("http://%s:%d", serveHost, port)
	if err := waitHealthy(ctx, baseURL, password, proc.Done()); err != nil {
		_ = proc.Kill()
		return Config{}, err
	}
	return Config{URL: baseURL, Password: password}, nil
}

// Scope-token store (T3.235). A token is a bearer credential bound to one
// environment, parent thread, provider instance and provider session. It has a
// privilege ceiling (the domain.ScopeMask it may never exceed), an absolute
// TTL and an idle TTL. The store keeps only a SHA-256 digest of the raw token:
// the raw value is returned by Issue once and never written to state or shown
// in diagnostics.

const (
	defaultScopeTTL     = 30 * time.Minute
	defaultScopeIdleTTL = 10 * time.Minute
)

// Scope-token validation errors. They are sentinels so a handler can map a
// failure to a status without parsing a message.
var (
	ErrScopeTokenUnknown    = errors.New("scope token: неизвестный токен")
	ErrScopeTokenExpired    = errors.New("scope token: истёк срок действия")
	ErrScopeTokenIdle       = errors.New("scope token: истёк срок неактивности")
	ErrScopeSessionMismatch = errors.New("scope token: сессия не совпадает")
	ErrScopeDenied          = errors.New("scope token: право не выдано")
	ErrScopeInvalidIssue    = errors.New("scope token: неверные параметры выдачи")
)

// ScopeBinding is the identity a token is bound to. All four dimensions are
// recorded at issue time and returned by a successful check. Session is also
// the revocation key.
type ScopeBinding struct {
	Environment  string
	ParentThread string
	Provider     string
	Session      string
}

// ScopeTokenIssue describes the token to issue. Ceiling is the maximal set of
// rights the token may ever exercise; zero or negative TTL and IdleTTL fall
// back to the store defaults.
type ScopeTokenIssue struct {
	Binding ScopeBinding
	Ceiling domain.ScopeMask
	TTL     time.Duration
	IdleTTL time.Duration
}

// scopeTokenRecord is the only per-token state the store keeps. The raw token
// is absent by design; the map key is its digest.
type scopeTokenRecord struct {
	binding   ScopeBinding
	ceiling   domain.ScopeMask
	issuedAt  time.Time
	expiresAt time.Time
	lastUsed  time.Time
	idleTTL   time.Duration
}

// scopeWallClock adapts the wall clock to domain.Clock.
type scopeWallClock struct{}

func (scopeWallClock) Now() time.Time { return time.Now().UTC() }

// ScopeTokenStore issues, validates and revokes scope tokens. It is safe for
// concurrent use by handlers.
type ScopeTokenStore struct {
	mu        sync.Mutex
	clock     domain.Clock
	ttl       time.Duration
	idleTTL   time.Duration
	tokens    map[string]*scopeTokenRecord
	bySession map[string]map[string]struct{}
}

// NewScopeTokenStore builds an empty store. A nil clock uses the wall clock;
// non-positive durations use the package defaults. Tests inject a clock and
// short durations so TTL and idle expiry are deterministic.
func NewScopeTokenStore(clock domain.Clock, ttl, idleTTL time.Duration) *ScopeTokenStore {
	if clock == nil {
		clock = scopeWallClock{}
	}
	if ttl <= 0 {
		ttl = defaultScopeTTL
	}
	if idleTTL <= 0 {
		idleTTL = defaultScopeIdleTTL
	}
	return &ScopeTokenStore{
		clock:     clock,
		ttl:       ttl,
		idleTTL:   idleTTL,
		tokens:    make(map[string]*scopeTokenRecord),
		bySession: make(map[string]map[string]struct{}),
	}
}

// Issue creates a token for in.Binding and returns its raw value once. Only
// the digest is stored. A non-empty Session and a mask containing only known
// scope bits are required; a zero ceiling is valid but grants no right.
func (s *ScopeTokenStore) Issue(in ScopeTokenIssue) (string, error) {
	if strings.TrimSpace(in.Binding.Session) == "" || !in.Ceiling.Valid() {
		return "", ErrScopeInvalidIssue
	}
	ttl, idleTTL := s.ttl, s.idleTTL
	if in.TTL > 0 {
		ttl = in.TTL
	}
	if in.IdleTTL > 0 {
		idleTTL = in.IdleTTL
	}
	raw, err := randomPassword()
	if err != nil {
		return "", err
	}
	digest := scopeTokenDigest(raw)
	now := s.clock.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[digest] = &scopeTokenRecord{
		binding:   in.Binding,
		ceiling:   in.Ceiling,
		issuedAt:  now,
		expiresAt: now.Add(ttl),
		lastUsed:  now,
		idleTTL:   idleTTL,
	}
	if s.bySession[in.Binding.Session] == nil {
		s.bySession[in.Binding.Session] = make(map[string]struct{})
	}
	s.bySession[in.Binding.Session][digest] = struct{}{}
	return raw, nil
}

// Authorize validates raw for the requested scope and session. On success it
// refreshes the idle window and returns the binding. Absolute expiry, idle
// expiry, a session mismatch or a right outside the ceiling all fail closed;
// an expired or idle token is also removed so it cannot linger in state.
func (s *ScopeTokenStore) Authorize(raw string, scope domain.Scope, session string) (ScopeBinding, error) {
	digest := scopeTokenDigest(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.tokens[digest]
	if rec == nil {
		return ScopeBinding{}, ErrScopeTokenUnknown
	}
	now := s.clock.Now().UTC()
	if !now.Before(rec.expiresAt) {
		s.dropLocked(digest)
		return ScopeBinding{}, ErrScopeTokenExpired
	}
	if now.Sub(rec.lastUsed) >= rec.idleTTL {
		s.dropLocked(digest)
		return ScopeBinding{}, ErrScopeTokenIdle
	}
	if rec.binding.Session != session {
		return ScopeBinding{}, ErrScopeSessionMismatch
	}
	if !rec.ceiling.Allows(scope) {
		return ScopeBinding{}, ErrScopeDenied
	}
	rec.lastUsed = now
	return rec.binding, nil
}

// AuthorizeWake, AuthorizeRead and AuthorizeStop are the handler-facing checks.
// They differ only in the right they require, which is what keeps wake, status
// reads and stop separate.
func (s *ScopeTokenStore) AuthorizeWake(raw, session string) (ScopeBinding, error) {
	return s.Authorize(raw, domain.ScopeWake, session)
}

func (s *ScopeTokenStore) AuthorizeRead(raw, session string) (ScopeBinding, error) {
	return s.Authorize(raw, domain.ScopeRead, session)
}

func (s *ScopeTokenStore) AuthorizeStop(raw, session string) (ScopeBinding, error) {
	return s.Authorize(raw, domain.ScopeStop, session)
}

// RevokeSession removes every token bound to session and returns how many
// were removed. It is idempotent: an unknown session revokes nothing.
func (s *ScopeTokenStore) RevokeSession(session string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.bySession[session]
	n := len(set)
	for digest := range set {
		delete(s.tokens, digest)
	}
	delete(s.bySession, session)
	return n
}

// ScopeTokenDiagnostic is a sanitized view of one stored token. It carries no
// raw token and no digest, so it is safe to log.
type ScopeTokenDiagnostic struct {
	Environment  string
	ParentThread string
	Provider     string
	Session      string
	Scopes       []domain.Scope
	IssuedAt     time.Time
	ExpiresAt    time.Time
	LastUsed     time.Time
}

// Diagnostics returns one sanitized entry per live token. The raw token is
// held only by the caller that received it from Issue, never by the store.
func (s *ScopeTokenStore) Diagnostics() []ScopeTokenDiagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ScopeTokenDiagnostic, 0, len(s.tokens))
	for _, rec := range s.tokens {
		scopes := make([]domain.Scope, 0, 3)
		for _, sc := range domain.ValidScopes() {
			if rec.ceiling.Allows(sc) {
				scopes = append(scopes, sc)
			}
		}
		out = append(out, ScopeTokenDiagnostic{
			Environment:  rec.binding.Environment,
			ParentThread: rec.binding.ParentThread,
			Provider:     rec.binding.Provider,
			Session:      rec.binding.Session,
			Scopes:       scopes,
			IssuedAt:     rec.issuedAt,
			ExpiresAt:    rec.expiresAt,
			LastUsed:     rec.lastUsed,
		})
	}
	return out
}

// dropLocked removes a record by digest. The caller must hold s.mu.
func (s *ScopeTokenStore) dropLocked(digest string) {
	rec := s.tokens[digest]
	if rec == nil {
		return
	}
	delete(s.tokens, digest)
	if set := s.bySession[rec.binding.Session]; set != nil {
		delete(set, digest)
		if len(set) == 0 {
			delete(s.bySession, rec.binding.Session)
		}
	}
}

// scopeTokenDigest hashes a raw token so state never holds a usable bearer.
func scopeTokenDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
