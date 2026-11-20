package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// TokenFileName is the bearer token file inside the state directory.
	TokenFileName = "http.token"

	// tokenPerm is the only mode accepted for the token file. The token grants
	// full loopback access, so group or world readability is refused.
	tokenPerm = 0o600

	// loopbackAddr binds a random free port on the loopback interface only.
	// Port zero lets the OS pick the port, which Addr reports.
	loopbackAddr = "127.0.0.1:0"

	tokenBytes = 32

	// VersionPath and StatusPath are the read-only projections served by this
	// server. They never write state, so both accept GET only (BS.4).
	VersionPath = "/version"
	StatusPath  = "/status"
)

var (
	// ErrEmptyToken reports a token file with no usable content.
	ErrEmptyToken = errors.New("server: empty bearer token")
	// ErrTokenPerm reports a token file readable by group or world.
	ErrTokenPerm = errors.New("server: bearer token file must be 0600")

	// BinaryVersion is the version reported by /version and /status. A build
	// can set it with -ldflags "-X ...server.BinaryVersion=v1.2.3"; the default
	// falls back to the Go module version and finally to "dev".
	BinaryVersion = ""

	// processStart anchors the uptime reported by /status. It is set once when
	// the process starts, so uptime is a pure read of the clock.
	processStart = time.Now()
)

// TokenPath returns the path of the bearer token file in stateDir.
func TokenPath(stateDir string) string {
	return filepath.Join(stateDir, TokenFileName)
}

// LoadOrCreateToken reads the bearer token from stateDir. When no file exists
// it generates a random token and writes it atomically with mode 0600. An
// existing file must be a regular 0600 file with non-empty content; a loose
// file is refused instead of being silently trusted.
func LoadOrCreateToken(stateDir string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("server: empty state dir")
	}
	path := TokenPath(stateDir)
	token, err := readToken(path)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return createToken(path)
}

func readToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("server: token %q is not a regular file", path)
	}
	if info.Mode().Perm()&^tokenPerm != 0 {
		return "", fmt.Errorf("%w: %q", ErrTokenPerm, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("server: read token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", ErrEmptyToken
	}
	return token, nil
}

func createToken(path string) (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("server: generate token: %w", err)
	}
	token := hex.EncodeToString(buf)
	if err := writeTokenAtomic(path, token); err != nil {
		return "", err
	}
	return token, nil
}

// writeTokenAtomic writes token through a temp file and renames it into place,
// so a reader never observes a half-written file. CreateTemp uses 0600, and the
// explicit Chmod keeps the mode independent of the process umask.
func writeTokenAtomic(path, token string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("server: temp token: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("server: write token: %w", err)
	}
	if err := tmp.Chmod(tokenPerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("server: chmod token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("server: close token: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("server: install token: %w", err)
	}
	return nil
}

// Bearer returns middleware that accepts only requests carrying
// "Authorization: Bearer <token>". Every other request gets 401 without
// reaching next. The comparison is constant time so the token is not leaked
// through response timing.
func Bearer(token string) func(http.Handler) http.Handler {
	want := "Bearer " + token
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Listen opens a TCP listener on the loopback interface with an OS-assigned
// port. It never binds 0.0.0.0: the interface is fixed here, so no caller can
// expose the server on a public address by accident.
func Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", loopbackAddr)
	if err != nil {
		return nil, fmt.Errorf("server: listen %s: %w", loopbackAddr, err)
	}
	return listener, nil
}

// HTTPServer owns the loopback listener, the bearer token and the wrapped
// handler. Close stops serving; it leaves the token file in place so a restart
// keeps the same credential.
type HTTPServer struct {
	listener   net.Listener
	httpServer *http.Server
	addr       string
	closeOnce  sync.Once
	closeErr   error
}

// Serve loads (or creates) the token in stateDir, binds 127.0.0.1 on a random
// port and serves handler behind bearer authentication. The read-only /version
// and /status projections are installed in front of handler. An optional
// StatusReader supplies the dynamic /status values; without one they stay at
// stable zero values. The caller owns the returned server and must Close it.
func Serve(stateDir string, handler http.Handler, readers ...StatusReader) (*HTTPServer, error) {
	token, err := LoadOrCreateToken(stateDir)
	if err != nil {
		return nil, err
	}
	listener, err := Listen()
	if err != nil {
		return nil, err
	}
	var reader StatusReader
	if len(readers) > 0 {
		reader = readers[0]
	}
	srv := &HTTPServer{listener: listener, addr: listener.Addr().String()}
	srv.httpServer = &http.Server{
		Handler:           Bearer(token)(NewStatusProjection(handler, reader).Handler()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		_ = srv.httpServer.Serve(listener)
	}()
	return srv, nil
}

// Addr reports the bound 127.0.0.1:port endpoint.
func (s *HTTPServer) Addr() string { return s.addr }

// Close stops serving and waits for in-flight requests.
func (s *HTTPServer) Close() error {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.closeErr = s.httpServer.Shutdown(ctx)
		if errors.Is(s.closeErr, context.DeadlineExceeded) {
			s.closeErr = s.httpServer.Close()
		}
	})
	return s.closeErr
}

// Readiness gates the health endpoint on completed initialization. A process
// that has not yet opened its database, applied migrations and started its
// workers must not answer 200 on /healthz: a caller would otherwise treat a
// half-booted instance as ready for traffic. Until MarkReady is called the
// gate answers 503 (BS.3, E01).
type Readiness struct {
	ready atomic.Bool
}

// MarkReady records that initialization finished. It is safe to call more
// than once and safe under concurrent use.
func (r *Readiness) MarkReady() { r.ready.Store(true) }

// IsReady reports whether MarkReady has been called.
func (r *Readiness) IsReady() bool { return r.ready.Load() }

// Health returns the readiness-gated /healthz handler. Before MarkReady a
// valid request gets 503; afterwards it gets 200. Other paths get 404 and
// non-GET methods 405, matching the liveness handler this gates. The path is
// HealthPath so the gate drops into the existing listener without a route
// change.
func (r *Readiness) Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != HealthPath {
			http.NotFound(w, req)
			return
		}
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !r.IsReady() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			http.Error(w, "not ready\n", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

// VersionResponse is the stable payload served by GET /version. It is a
// transport-owned shape so adding fields cannot change the wire contract for
// an older client.
type VersionResponse struct {
	Version string `json:"version"`
}

// StatusResponse is the stable read-only payload served by GET /status. Every
// field is either a constant, a clock read or a value from a StatusReader; no
// field is computed by writing to the store.
type StatusResponse struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	ActiveWatches int    `json:"activeWatches"`
	Lease         string `json:"lease"`
}

// StatusReader is the read-only port for the two dynamic /status values. A
// nil reader is allowed: the projection then reports zero watches and an
// "unknown" lease, so a server without a store still answers /status without
// a write.
type StatusReader interface {
	ActiveWatches(ctx context.Context) (int, error)
	LeaseState(ctx context.Context) (string, error)
}

// StatusProjection installs the /version and /status read-only routes in front of
// next. Every other path is delegated to next unchanged. Both routes accept
// GET only and answer 405 with an Allow header for any other method, so they
// can never be used to change state.
type StatusProjection struct {
	next   http.Handler
	reader StatusReader
	start  time.Time
}

// NewStatusProjection builds a projection. A nil next leaves unknown paths to the
// mux's 404; a nil reader yields stable zero values for the dynamic fields.
func NewStatusProjection(next http.Handler, reader StatusReader) *StatusProjection {
	return &StatusProjection{next: next, reader: reader, start: processStart}
}

// Handler returns the read-only HTTP handler for both projection routes.
func (p *StatusProjection) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(VersionPath, p.handleVersion)
	mux.HandleFunc(StatusPath, p.handleStatus)
	if p.next != nil {
		mux.Handle("/", p.next)
	}
	return mux
}

func (p *StatusProjection) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeProjectionError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET is supported")
		return
	}
	writeProjectionJSON(w, http.StatusOK, VersionResponse{Version: p.version()})
}

func (p *StatusProjection) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeProjectionError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET is supported")
		return
	}

	watches, lease := 0, "unknown"
	if p.reader != nil {
		if n, err := p.reader.ActiveWatches(r.Context()); err == nil && n >= 0 {
			watches = n
		}
		if state, err := p.reader.LeaseState(r.Context()); err == nil && strings.TrimSpace(state) != "" {
			lease = state
		}
	}

	uptime := int64(0)
	if p.start.Before(time.Now()) {
		uptime = int64(time.Since(p.start).Seconds())
	}
	writeProjectionJSON(w, http.StatusOK, StatusResponse{
		Version:       p.version(),
		UptimeSeconds: uptime,
		ActiveWatches: watches,
		Lease:         lease,
	})
}

// version resolves the binary version without a store read. An explicit
// BinaryVersion wins; otherwise the Go module version is used; "dev" is the
// stable fallback for local builds.
func (p *StatusProjection) version() string {
	if v := strings.TrimSpace(BinaryVersion); v != "" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func writeProjectionJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type projectionErrorBody struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeProjectionError(w http.ResponseWriter, status int, code, message string) {
	writeProjectionJSON(w, status, projectionErrorBody{Code: code, Message: message})
}
