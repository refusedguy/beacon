// Package httpapi serves a loopback-only HTTP and SSE view of Beacon state.
// It owns no store handle: the composition root injects a read port and a
// command sink, so the transport stays independent of the persistence layer.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// loopbackAddr is used when the caller does not set BEACON_HTTP_ADDR. Port
	// zero lets the OS pick a free port, which the connection file reports.
	loopbackAddr = "127.0.0.1:0"
	// AddrEnv overrides the listen address. The host part is forced to
	// loopback, so a bad setting cannot expose the API on a public interface.
	AddrEnv = "BEACON_HTTP_ADDR"

	connectionFileName = "connection.json"
	connectionPerm     = 0o600

	defaultEventCapacity = 256
	subscriberBuffer     = 64
	maxReplay            = 1000
	maxRequestBody       = 1 << 20
	shutdownTimeout      = 3 * time.Second
)

// heartbeatInterval is a variable so a test can shorten it without waiting a
// real quarter minute for the keepalive comment. The production value is 15s.
var heartbeatInterval = 15 * time.Second

// PointStats is the hourly point budget already computed by the CI provider.
type PointStats struct {
	Used   int `json:"used"`
	Budget int `json:"budget"`
}

// LastCycle is the most recent completed reconciler pass.
type LastCycle struct {
	At             time.Time `json:"at,omitempty"`
	WatchesUpdated int       `json:"watchesUpdated"`
	DurationMs     int64     `json:"durationMs"`
}

// Snapshot is the read-only projection served by /api/v1/snapshot. It is a
// transport-owned shape built from domain values, never a live store object.
type Snapshot struct {
	LiveWatches       []domain.Watch              `json:"liveWatches"`
	BlockerIncidents  []domain.BlockerIncident    `json:"blockerIncidents"`
	BoundOrchestrator *domain.OrchestratorBinding `json:"boundOrchestrator,omitempty"`
	Escalations       []string                    `json:"escalations"`
	PointStats        PointStats                  `json:"pointStats"`
	Metrics           json.RawMessage             `json:"metrics"`
	LastCycle         LastCycle                   `json:"lastCycle"`
	LoadErrors        []string                    `json:"loadErrors"`
}

// StateProvider supplies the projection. The transport only calls Snapshot.
type StateProvider interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Decision is one human command. ObligationID and Note are optional; Actor is
// recorded in the journal by the sink, not interpreted by the transport.
type Decision struct {
	Command      string `json:"command"`
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID,omitempty"`
	Note         string `json:"note,omitempty"`
	Actor        string `json:"actor,omitempty"`
}

// DecisionResult is returned for every accepted command. Applied is false on
// an idempotent repeat so a client can tell a fresh effect from a cached one.
type DecisionResult struct {
	OK         bool   `json:"ok"`
	Command    string `json:"command"`
	WatchID    string `json:"watchID"`
	Applied    bool   `json:"applied"`
	DecisionID string `json:"decisionID"`
	Status     string `json:"status,omitempty"`
}

// DecisionSink applies one command. Idempotency is owned by Server, so the
// sink sees a given decision at most once per process lifetime.
type DecisionSink interface {
	Apply(ctx context.Context, d Decision) (DecisionResult, error)
}

// Config is the set of ports and facts the composition root hands to Start.
type Config struct {
	Addr          string
	StateDir      string
	Version       string
	ProjectKey    string
	IsPollerOwner bool
	StartedAt     time.Time
	// Token is optional. An empty value generates a random bearer token.
	Token    string
	Provider StateProvider
	Sink     DecisionSink
	Events   *EventBus
}

// connectionFile is written next to the state store with mode 0600. The extra
// pid and version fields let a reader tell a live endpoint from a stale one.
type connectionFile struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

// Event is one SSE frame. ID is assigned by the bus and doubles as the
// Last-Event-ID a client sends on reconnect.
type Event struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	WatchID string          `json:"watchID,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventBus keeps a bounded ring of recent events and fans live events out to
// SSE subscribers. Slow subscribers drop live frames but can replay them from
// the ring with Last-Event-ID.
type EventBus struct {
	mu       sync.Mutex
	next     uint64
	ring     []Event
	capacity int
	subs     map[chan Event]struct{}
	closed   bool
}

// NewEventBus builds a bus holding at most capacity events. A non-positive
// capacity selects the default, so no caller can build an unbounded buffer.
func NewEventBus(capacity int) *EventBus {
	if capacity <= 0 {
		capacity = defaultEventCapacity
	}
	return &EventBus{capacity: capacity, subs: make(map[chan Event]struct{})}
}

// Publish assigns an id and a timestamp, then stores the event and offers it
// to every live subscriber without blocking on a slow one.
func (b *EventBus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.next++
	e.ID = b.next
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b.ring = append(b.ring, e)
	if len(b.ring) > b.capacity {
		copy(b.ring, b.ring[len(b.ring)-b.capacity:])
		b.ring = b.ring[:b.capacity]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a live event channel and the function that removes it.
// The returned function is safe to call more than once.
func (b *EventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	if b.closed {
		close(ch)
		b.mu.Unlock()
		return ch, func() {}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[ch]; ok {
				delete(b.subs, ch)
				close(ch)
			}
			b.mu.Unlock()
		})
	}
}

// Since returns buffered events with an id greater than lastID, in order.
func (b *EventBus) Since(lastID uint64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, 0, len(b.ring))
	for _, e := range b.ring {
		if e.ID > lastID {
			out = append(out, e)
		}
	}
	return out
}

// Last returns the most recent n buffered events, in order.
func (b *EventBus) Last(n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.ring) {
		n = len(b.ring)
	}
	if n <= 0 {
		return nil
	}
	out := make([]Event, n)
	copy(out, b.ring[len(b.ring)-n:])
	return out
}

func (b *EventBus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
}

// Server owns the listener, the bearer token and the connection file. Close
// shuts the listener down and removes the connection file only when it still
// carries this process' pid.
type Server struct {
	cfg        Config
	token      string
	host       string
	port       int
	pid        int
	listener   net.Listener
	httpServer *http.Server
	events     *EventBus

	decisions map[string]DecisionResult
	decMu     sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// Start opens a loopback listener, publishes the endpoint in connection.json
// and begins serving. The caller owns the returned server and must Close it.
func Start(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return nil, errors.New("httpapi: empty state dir")
	}
	if cfg.Provider == nil {
		return nil, errors.New("httpapi: nil state provider")
	}
	if cfg.Sink == nil {
		return nil, errors.New("httpapi: nil decision sink")
	}

	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		var err error
		token, err = randomToken()
		if err != nil {
			return nil, err
		}
	}

	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = loopbackAddr
	}
	listenAddr, err := loopbackListenAddr(addr)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("httpapi: listen %s: %w", listenAddr, err)
	}
	tcp, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("httpapi: unexpected listener address %T", listener.Addr())
	}
	host := tcp.IP.String()
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}

	events := cfg.Events
	if events == nil {
		events = NewEventBus(0)
	}
	s := newServer(cfg, token, host, tcp.Port, events)
	s.listener = listener
	if err := s.writeConnection(); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s.httpServer = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			observe.ActorError("httpapi-serve", "", err)
		}
	}()
	return s, nil
}

func newServer(cfg Config, token, host string, port int, events *EventBus) *Server {
	if events == nil {
		events = NewEventBus(0)
	}
	return &Server{
		cfg:       cfg,
		token:     token,
		host:      host,
		port:      port,
		pid:       os.Getpid(),
		events:    events,
		decisions: make(map[string]DecisionResult),
	}
}

// loopbackListenAddr keeps the port from addr but always binds 127.0.0.1.
func loopbackListenAddr(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("httpapi: bad address %q: %w", addr, err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("httpapi: bad port in %q: %w", addr, err)
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("httpapi: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// writeConnection writes the endpoint atomically so a reader never observes a
// half-written file. CreateTemp already uses 0600 on Unix.
func (s *Server) writeConnection() error {
	payload, err := json.Marshal(connectionFile{
		Host: s.host, Port: s.port, Token: s.token, PID: s.pid, Version: s.cfg.Version,
	})
	if err != nil {
		return fmt.Errorf("httpapi: encode connection: %w", err)
	}
	tmp, err := os.CreateTemp(s.cfg.StateDir, ".connection-*.tmp")
	if err != nil {
		return fmt.Errorf("httpapi: temp connection: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("httpapi: write connection: %w", err)
	}
	if err := tmp.Chmod(connectionPerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("httpapi: chmod connection: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("httpapi: close connection: %w", err)
	}
	if err := os.Rename(name, filepath.Join(s.cfg.StateDir, connectionFileName)); err != nil {
		return fmt.Errorf("httpapi: install connection: %w", err)
	}
	return nil
}

// Close stops serving and removes the connection file only when it still
// points at this process. A file written by a newer owner is left untouched.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		if s.httpServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := s.httpServer.Shutdown(ctx); err != nil {
				s.closeErr = err
				_ = s.httpServer.Close()
			}
		} else if s.listener != nil {
			_ = s.listener.Close()
		}
		s.events.close()
		s.removeConnectionIfMine()
	})
	return s.closeErr
}

func (s *Server) removeConnectionIfMine() {
	path := filepath.Join(s.cfg.StateDir, connectionFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var current connectionFile
	if json.Unmarshal(raw, &current) != nil || current.PID != s.pid {
		return
	}
	_ = os.Remove(path)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	mux.Handle("POST /api/v1/human/decide", s.sameOriginJSON(http.HandlerFunc(s.handleDecide)))
	return s.bearer(mux)
}

func (s *Server) bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + s.token
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "неверный bearer-токен")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOriginJSON guards a state-changing JSON POST against cross-site
// requests. A browser must send an Origin matching the loopback listener;
// command-line callers that send no Origin are allowed.
func (s *Server) sameOriginJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "INVALID_INPUT", "нужен Content-Type: application/json")
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !s.sameOrigin(origin) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "Origin не совпадает с адресом сервера")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sameOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if port != strconv.Itoa(s.port) {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Code: code, Message: message})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	started := s.cfg.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	uptime := int64(time.Since(started).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	body := struct {
		OK            bool     `json:"ok"`
		Version       string   `json:"version"`
		PID           int      `json:"pid"`
		ProjectKey    string   `json:"projectKey"`
		IsPollerOwner bool     `json:"isPollerOwner"`
		UptimeSec     int64    `json:"uptimeSec"`
		LoadErrors    []string `json:"loadErrors"`
	}{
		OK: true, Version: s.cfg.Version, PID: s.pid,
		ProjectKey: s.cfg.ProjectKey, IsPollerOwner: s.cfg.IsPollerOwner,
		UptimeSec: uptime, LoadErrors: []string{},
	}
	snap, err := s.cfg.Provider.Snapshot(r.Context())
	if err != nil {
		body.OK = false
		body.LoadErrors = []string{err.Error()}
	} else {
		body.LoadErrors = append(body.LoadErrors, snap.LoadErrors...)
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := s.cfg.Provider.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SNAPSHOT_FAILED", err.Error())
		return
	}
	if snap.LiveWatches == nil {
		snap.LiveWatches = []domain.Watch{}
	}
	if snap.BlockerIncidents == nil {
		snap.BlockerIncidents = []domain.BlockerIncident{}
	}
	if snap.Escalations == nil {
		snap.Escalations = []string{}
	}
	if snap.LoadErrors == nil {
		snap.LoadErrors = []string{}
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "writer не поддерживает поток")
		return
	}
	ch, unsubscribe := s.events.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	last := uint64(0)
	for _, e := range s.replay(r) {
		writeSSE(w, flusher, e)
		last = e.ID
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.ID <= last {
				continue
			}
			writeSSE(w, flusher, e)
			last = e.ID
		case <-ticker.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) replay(r *http.Request) []Event {
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return s.events.Since(id)
		}
	}
	raw := strings.TrimSpace(r.URL.Query().Get("replay"))
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return nil
	}
	if n > maxReplay {
		n = maxReplay
	}
	return s.events.Last(n)
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, e Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	name := sanitizeEventName(e.Type)
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, name, data)
	flusher.Flush()
}

func sanitizeEventName(name string) string {
	if name == "" {
		return "message"
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
		if !ok {
			if i == 0 {
				return "message"
			}
			return name[:i]
		}
	}
	return name
}

var validCommands = map[string]bool{
	"approve": true, "reject": true, "context": true, "rework": true,
	"pause": true, "resume": true, "unblock": true, "cancel": true,
	"force_retry_merge": true,
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	var d Decision
	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(body).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "неверное тело запроса")
		return
	}
	d.Command = strings.ToLower(strings.TrimSpace(d.Command))
	d.WatchID = strings.TrimSpace(d.WatchID)
	d.ObligationID = strings.TrimSpace(d.ObligationID)
	if !validCommands[d.Command] {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "неизвестная команда "+d.Command)
		return
	}
	if d.WatchID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "пустое поле watchID")
		return
	}

	key := strings.Join([]string{d.Command, d.WatchID, d.ObligationID, d.Note}, "\x00")
	s.decMu.Lock()
	if previous, ok := s.decisions[key]; ok {
		previous.Applied = false
		s.decMu.Unlock()
		writeJSON(w, http.StatusOK, previous)
		return
	}
	result, err := s.cfg.Sink.Apply(r.Context(), d)
	if err != nil {
		s.decMu.Unlock()
		writeError(w, http.StatusInternalServerError, "DECISION_FAILED", err.Error())
		return
	}
	result.OK = true
	result.Applied = true
	if result.DecisionID == "" {
		result.DecisionID = key
	}
	s.decisions[key] = result
	s.decMu.Unlock()
	writeJSON(w, http.StatusOK, result)
}
