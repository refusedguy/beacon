package opencode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

	// reuseProbeTimeout bounds one readiness probe of a server found in
	// opencode-serve.json. A live pid with a dead HTTP API must fail fast so
	// EnsureServer can drop the stale record and spawn a fresh server (MM-03).
	reuseProbeTimeout = 2 * time.Second

	// serveStateFile is the name of the record Beacon writes for a server it
	// spawned itself. service.json stays the property of the user's OpenCode.
	serveStateFile = "opencode-serve.json"
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

// pidProcess is the optional half a real child implements. Tests keep using
// the smaller spawnedProcess seam, so a missing PID is never a failure.
type pidProcess interface {
	PID() int
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

// PID returns the OS process id of the child. Zero means it never started.
func (p *execProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
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
	// The child must not outlive Beacon. SIGTERM is delivered when the parent
	// dies, so a killed agent cannot leave an orphaned serve behind (Q-A2).
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
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

// ServeState is Beacon's own record of a server it spawned. service.json
// belongs to the user's OpenCode and is never rewritten here. The password is
// stored with the same 0600 permissions as the registration file so a later
// call can reuse the server instead of starting a second one.
type ServeState struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	PID      int    `json:"pid"`
}

// ensureMu serializes the whole resolve-or-spawn path inside one process. Two
// concurrent calls must not both decide that no server exists.
var ensureMu sync.Mutex

func stateHomeDir() (string, error) {
	return StateBaseDir()
}

func serveStatePath() (string, error) {
	base, err := stateHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "beacon", serveStateFile), nil
}

// ReadServeState returns the record of a server spawned by Beacon. The bool is
// false when no record exists or the file is not valid.
func ReadServeState() (ServeState, bool) {
	path, err := serveStatePath()
	if err != nil {
		return ServeState{}, false
	}
	data, err := readFile(path)
	if err != nil {
		return ServeState{}, false
	}
	var st ServeState
	if err := json.Unmarshal(data, &st); err != nil || strings.TrimSpace(st.URL) == "" {
		return ServeState{}, false
	}
	return st, true
}

// WriteServeState records a spawned server so later calls reuse it.
func WriteServeState(st ServeState) error {
	path, err := serveStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("opencode serve: каталог состояния: %w", err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("opencode serve: сериализация состояния: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("opencode serve: запись состояния: %w", err)
	}
	return nil
}

// ClearServeState removes a stale record. Missing file is not an error.
func ClearServeState() error {
	path, err := serveStatePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("opencode serve: удаление состояния: %w", err)
	}
	return nil
}

// ReadServicePID returns the pid of the user's main OpenCode server from
// service.json. Zero means the registration has no pid or cannot be read.
func ReadServicePID() int {
	cfg, err := Discover()
	if err != nil {
		return 0
	}
	return cfg.PID
}

// ProcessAlive mirrors the signal-0 check used elsewhere in Beacon: EPERM
// still means the process exists, only another user owns it.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TerminateServe asks a server to stop. GC uses SIGTERM so the process can
// close its listeners before exiting.
func TerminateServe(pid int) error {
	if pid <= 0 {
		return errors.New("opencode serve: неверный pid")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("opencode serve: SIGTERM pid %d: %w", pid, err)
	}
	return nil
}

// ServeProcess is one `opencode serve --hostname ...` found on this machine.
// Port comes from the --port argument and is used to protect the live
// registration even when the pid is absent from service.json.
type ServeProcess struct {
	PID  int
	Port string
}

// ListServeProcesses scans /proc for running opencode serve processes. It
// reads only cmdline and never opens a socket or changes a process.
func ListServeProcesses() ([]ServeProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("opencode serve: чтение /proc: %w", err)
	}
	out := make([]ServeProcess, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := readFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(data), "\x00")
		if proc, ok := parseServeCmdline(pid, args); ok {
			out = append(out, proc)
		}
	}
	return out, nil
}

// parseServeCmdline decides whether args belong to an opencode serve process.
// It is pure so the classification can be tested with mocks.
func parseServeCmdline(pid int, args []string) (ServeProcess, bool) {
	if len(args) == 0 || !strings.Contains(filepath.Base(args[0]), "opencode") {
		return ServeProcess{}, false
	}
	serve, hostname := false, false
	port := ""
	for i, arg := range args {
		switch arg {
		case "serve":
			serve = true
		case "--hostname":
			hostname = true
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
			}
		}
	}
	if !serve || !hostname {
		return ServeProcess{}, false
	}
	return ServeProcess{PID: pid, Port: port}, true
}

// EnsureServer returns a Config for a live opencode HTTP API. A server Beacon
// spawned earlier is reused while its pid is alive, then the user's
// service.json, and only then a new child on a free loopback port (ADR 0060 п3).
func EnsureServer(ctx context.Context) (Config, error) {
	ensureMu.Lock()
	defer ensureMu.Unlock()

	if st, ok := ReadServeState(); ok && ProcessAlive(st.PID) {
		probeCtx, cancel := context.WithTimeout(ctx, reuseProbeTimeout)
		ready := healthReady(probeCtx, st.URL, st.Password)
		cancel()
		if ready {
			return Config{URL: st.URL, Password: st.Password}, nil
		}
		// The pid is alive but the HTTP API is not answering. Drop the stale
		// record and fall through to Discover and spawn (MM-03).
		_ = ClearServeState()
	}
	if cfg, err := Discover(); err == nil {
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
	pid := 0
	if withPID, ok := proc.(pidProcess); ok {
		pid = withPID.PID()
	}
	cfg := Config{URL: baseURL, Password: password}
	if err := WriteServeState(ServeState{URL: cfg.URL, Password: cfg.Password, PID: pid}); err != nil {
		_ = proc.Kill()
		return Config{}, err
	}
	return cfg, nil
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
