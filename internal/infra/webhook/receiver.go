// Package webhook turns signed GitHub deliveries into hints for the CI bridge.
// A webhook never reads CI state and never publishes a verdict: it only asks
// the bridge to re-read the authoritative state through its own port.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// The receiver binds loopback only. The address override may change the port,
// never the interface: a bad setting must not expose the listener publicly
// (ADR 0184, ADR 0219).
const (
	LoopbackHost       = "127.0.0.1"
	DefaultWebhookAddr = "127.0.0.1:8788"
	WebhookAddrEnv     = "BEACON_WEBHOOK_ADDR"
	SecretFileName     = "webhook.secret"
	EventPing          = "ping"
)

// Delivery is the journal-shaped view of one validated delivery. It carries
// identifiers and the signed bytes, never a status or a verdict.
type Delivery struct {
	ID      string
	Event   string
	Action  string
	Payload []byte
	At      time.Time
}

// DeliveryRecorder is the minimal journal port the receiver needs. T3.375 adds
// a durable store-backed implementation; until it lands the adapter in
// cmd/beacon supplies a process-local journal. A repeated delivery ID must
// report claimed=false without a second record.
type DeliveryRecorder interface {
	Record(ctx context.Context, d Delivery) (claimed bool, err error)
}

// DeliveryKey names one delivery in an idempotency journal. The store adapter
// from T3.375 uses the same namespace, so a redelivery keeps the same key
// across process restarts.
func DeliveryKey(deliveryID string) string {
	return domain.IdempotencyKey(githubDeliveryNamespace, deliveryID)
}

var _ http.Handler = (*Receiver)(nil)

// Receiver validates a signed delivery, acknowledges it at once and records it
// in the background. It never calls GitHub and never changes a Watch verdict.
type Receiver struct {
	secret   []byte
	recorder DeliveryRecorder
	clock    domain.Clock
}

// NewReceiver builds a loopback webhook handler. An empty secret or a nil
// recorder is refused: a handler that cannot verify or cannot record must not
// start.
func NewReceiver(secret []byte, recorder DeliveryRecorder, clock domain.Clock) (*Receiver, error) {
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	if recorder == nil {
		return nil, errors.New("webhook: delivery recorder is required")
	}
	if clock == nil {
		clock = wallClock{}
	}
	return &Receiver{
		secret:   append([]byte(nil), secret...),
		recorder: recorder,
		clock:    clock,
	}, nil
}

// ServeHTTP verifies the raw body before answering. Parsing and journaling run
// after the 2xx so a slow recorder never holds the GitHub connection open.
func (h *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "webhook: POST required")
		return
	}
	// SHA-256 is mandatory. go-github falls back to the legacy SHA-1 header,
	// so require X-Hub-Signature-256 explicitly (HMAC-SHA256 only).
	if strings.TrimSpace(r.Header.Get(gh.SHA256SignatureHeader)) == "" {
		writeJSON(w, http.StatusUnauthorized, "invalid_signature", "webhook: X-Hub-Signature-256 required")
		return
	}

	// ValidatePayload reads the wire bytes, enforces the 25 MB cap and checks
	// the digest in constant time. GitHub sends application/json; defaulting a
	// missing Content-Type to that does not change the signed bytes.
	verifyReq := r.Clone(r.Context())
	if verifyReq.Header.Get("Content-Type") == "" {
		verifyReq.Header.Set("Content-Type", "application/json")
	}
	payload, err := gh.ValidatePayload(verifyReq, h.secret)
	if err != nil {
		writeJSON(w, validationStatus(err), "invalid_payload", err.Error())
		return
	}

	event := gh.WebHookType(r)
	// ping only proves the route works, and a delivery with no event type has
	// nothing to record. Both are acknowledged without background work.
	if event == "" || event == EventPing {
		writeJSON(w, http.StatusAccepted, "ignored", "no delivery to record")
		return
	}
	deliveryID := strings.TrimSpace(gh.DeliveryID(r))
	if deliveryID == "" {
		writeJSON(w, http.StatusBadRequest, "missing_delivery_id", "webhook: X-GitHub-Delivery required")
		return
	}

	d := Delivery{
		ID:      deliveryID,
		Event:   event,
		Payload: append([]byte(nil), payload...),
		At:      h.clock.Now(),
	}
	// ServeHTTP's request context is cancelled on return, so the detached
	// goroutine must not inherit it.
	go h.record(d)
	writeJSON(w, http.StatusAccepted, "accepted", "")
}

func (h *Receiver) record(d Delivery) {
	ctx := context.Background()
	parsed, err := gh.ParseWebHook(d.Event, d.Payload)
	if err != nil {
		// Unknown event or unparsable payload: acknowledge, do not record.
		// The effect is a no-op, so a redelivery stays idempotent.
		observe.Event(observe.LevelInfo, "webhook-unknown-event", observe.Fields{
			"delivery": d.ID,
			"event":    d.Event,
		})
		return
	}
	d.Action = eventAction(parsed, d.Payload)
	claimed, err := h.recorder.Record(ctx, d)
	if err != nil {
		observe.ActorError("webhook-record", d.ID, err)
		return
	}
	if !claimed {
		observe.Event(observe.LevelInfo, "webhook-duplicate", observe.Fields{
			"delivery": d.ID,
			"event":    d.Event,
		})
	}
}

type actionGetter interface{ GetAction() string }

// eventAction reads the action from the typed go-github event. Events without
// an action (push, for example) fall back to the raw envelope field.
func eventAction(parsed any, payload []byte) string {
	if acted, ok := parsed.(actionGetter); ok {
		return acted.GetAction()
	}
	var envelope struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return envelope.Action
}

// validationStatus maps a go-github validation error to an HTTP status. A
// signature failure is 401, an oversized body is 413, the rest are bad input.
func validationStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "exceeds maximum allowed size"):
		return http.StatusRequestEntityTooLarge
	case strings.Contains(msg, "signature"):
		return http.StatusUnauthorized
	default:
		return http.StatusBadRequest
	}
}

// Config assembles the loopback receiver. SecretPath must point to an
// owner-only state file; Recorder is the journal port.
type Config struct {
	Addr       string
	SecretPath string
	Recorder   DeliveryRecorder
	Clock      domain.Clock
}

// Server owns the loopback listener and its HTTP server. Close stops serving.
type Server struct {
	listener   net.Listener
	httpServer *http.Server
	addr       string
	closeOnce  sync.Once
	closeErr   error
}

// Start loads the 0600 secret, binds 127.0.0.1 and serves signed deliveries.
// A missing, permissive or empty secret is an error: the receiver never runs
// unauthenticated.
func Start(cfg Config) (*Server, error) {
	secret, err := LoadSecret(cfg.SecretPath)
	if err != nil {
		return nil, err
	}
	receiver, err := NewReceiver(secret, cfg.Recorder, cfg.Clock)
	if err != nil {
		return nil, err
	}
	listenAddr, err := loopbackWebhookAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	s := &Server{listener: listener, addr: listener.Addr().String()}
	s.httpServer = &http.Server{
		Handler:           receiver,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute, // room for a 25 MB delivery
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			observe.ActorError("webhook-serve", "", err)
		}
	}()
	return s, nil
}

// Addr reports the bound 127.0.0.1:port endpoint.
func (s *Server) Addr() string { return s.addr }

// Close stops the listener and waits for in-flight requests.
func (s *Server) Close() error {
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

// loopbackWebhookAddr keeps the port from addr but always binds 127.0.0.1.
func loopbackWebhookAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = DefaultWebhookAddr
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", err
	}
	return net.JoinHostPort(LoopbackHost, port), nil
}
