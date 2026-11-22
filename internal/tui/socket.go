// Package tui control socket. Agents attach to a running TUI over a local Unix
// socket. The socket is a boundary: it parses requests, calls a Handler, and
// pushes events. It never opens the store and never writes SQLite.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

const (
	frameResponse = "response"
	frameEvent    = "event"

	// outQueue bounds events waiting for one agent. A slow agent loses events
	// instead of blocking the TUI; it re-reads the projection on the next turn.
	outQueue = 16

	// maxDedup bounds the idempotency cache. Oldest keys are evicted first.
	maxDedup = 256

	// socketMode lets only the owning user reach the socket.
	socketMode = 0600
)

var (
	// ErrSocketClosed marks a control connection that stopped before the
	// response arrived.
	ErrSocketClosed = errors.New("tui: control socket closed")
)

// Command is one verb an agent may send through the control socket.
type Command string

// Lifecycle commands drive the Beacon watch and obligation lifecycle. The
// composition root handles them, not the human-decide route.
const (
	CmdAck     Command = "ack"
	CmdCancel  Command = "cancel"
	CmdClaim   Command = "claim"
	CmdRelease Command = "release"
)

// Decision commands mirror the verbs the human-decide route accepts in
// internal/infra/httpapi/server.go. They let an agent send the same decision
// an operator sends with a hotkey. Only the wire vocabulary lives here. The
// key mapping and the receipt display stay in the interactive app, so the
// agent channel never depends on operator hotkeys. CmdCancel above is the
// cancel verb of this set as well.
const (
	CmdApprove Command = "approve"
	CmdReject  Command = "reject"
	CmdContext Command = "context"
	CmdRework  Command = "rework"
	CmdPause   Command = "pause"
	CmdResume  Command = "resume"
	CmdUnblock Command = "unblock"
)

// DecisionCommands lists the eight human-decide verbs in the order the
// server declares them. Callers can use it for help text and validation
// without restating the strings.
var DecisionCommands = []Command{
	CmdApprove, CmdReject, CmdContext, CmdRework,
	CmdPause, CmdResume, CmdUnblock, CmdCancel,
}

func validCommand(c Command) bool {
	switch c {
	case CmdAck, CmdCancel, CmdClaim, CmdRelease,
		CmdApprove, CmdReject, CmdContext, CmdRework,
		CmdPause, CmdResume, CmdUnblock:
		return true
	}
	return false
}

// SocketRequest is one line an agent writes to the socket. ID correlates the
// response. WatchID and Revision make a command idempotent; the server returns
// a cached answer for a repeated key instead of calling the handler twice.
type SocketRequest struct {
	ID         string  `json:"id"`
	Command    Command `json:"command"`
	WatchID    string  `json:"watchID"`
	Revision   uint64  `json:"revision,omitempty"`
	Obligation string  `json:"obligationID,omitempty"`
	Note       string  `json:"note,omitempty"`
	Actor      string  `json:"actor,omitempty"`
}

// SocketResponse is the server's answer to one SocketRequest. OK false means
// the command was refused; Error carries the reason. Applied reports whether
// the command changed state rather than matched a cached answer.
type SocketResponse struct {
	ID      string  `json:"id"`
	OK      bool    `json:"ok"`
	Applied bool    `json:"applied"`
	Command Command `json:"command,omitempty"`
	WatchID string  `json:"watchID,omitempty"`
	Status  string  `json:"status,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// SocketEvent is one notification the TUI pushes to every attached agent.
// Agents treat it as a cue to refresh, not as state to trust.
type SocketEvent struct {
	Type    string          `json:"type"`
	WatchID string          `json:"watchID,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Handler executes one control command. The composition root supplies it and
// forwards the command to the running server. A nil handler is rejected by
// Listen so the socket never silently drops commands.
type Handler interface {
	Handle(context.Context, SocketRequest) (SocketResponse, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, SocketRequest) (SocketResponse, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, req SocketRequest) (SocketResponse, error) {
	return f(ctx, req)
}

// wireFrame tags every server-to-agent frame so one decoder can separate
// responses from pushed events.
type wireFrame struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func encodeFrame(kind string, v any) (wireFrame, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return wireFrame{}, err
	}
	return wireFrame{Kind: kind, Data: data}, nil
}

// dedupKey returns the idempotency key for a request. Revision zero means the
// caller did not supply a version, so the request is not cached.
func dedupKey(req SocketRequest) (string, bool) {
	if req.Revision == 0 {
		return "", false
	}
	return fmt.Sprintf("%s|%s|%d", req.Command, req.WatchID, req.Revision), true
}

func validationError(req SocketRequest) string {
	if !validCommand(req.Command) {
		return fmt.Sprintf("unsupported command %q", req.Command)
	}
	if strings.TrimSpace(req.WatchID) == "" {
		return "watchID is required"
	}
	return ""
}

// socketConn is one attached agent. A single writer goroutine owns conn.Write,
// while the read loop and Publish enqueue frames through send and sendWait.
type socketConn struct {
	conn     net.Conn
	out      chan wireFrame
	done     chan struct{}
	doneOnce sync.Once
}

func newSocketConn(conn net.Conn) *socketConn {
	return &socketConn{conn: conn, out: make(chan wireFrame, outQueue), done: make(chan struct{})}
}

func (sc *socketConn) stop() {
	sc.doneOnce.Do(func() { close(sc.done) })
}

// send drops a frame when the agent's queue is full. Events are hints, so a
// slow agent is better served by losing a hint than by stalling the TUI.
func (sc *socketConn) send(f wireFrame) {
	select {
	case sc.out <- f:
	case <-sc.done:
	default:
	}
}

// sendWait queues a response and waits for the writer or for shutdown.
func (sc *socketConn) sendWait(ctx context.Context, f wireFrame) bool {
	select {
	case sc.out <- f:
		return true
	case <-ctx.Done():
		return false
	case <-sc.done:
		return false
	}
}

// Server accepts agent connections on one local Unix socket.
type Server struct {
	path    string
	ln      net.Listener
	handler Handler
	rootCtx context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	clients map[*socketConn]struct{}
	dedup   map[string]SocketResponse
	order   []string
	closed  bool

	once sync.Once
	wg   sync.WaitGroup
}

// Listen opens path as a Unix socket, restricts it to the owning user, and
// starts accepting agents. Call Close to stop the listener and detach agents.
func Listen(path string, handler Handler) (*Server, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("tui: socket path is required")
	}
	if handler == nil {
		return nil, errors.New("tui: socket handler is required")
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("tui: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, socketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("tui: restrict socket %s: %w", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		path:    path,
		ln:      ln,
		handler: handler,
		rootCtx: ctx,
		cancel:  cancel,
		clients: make(map[*socketConn]struct{}),
		dedup:   make(map[string]SocketResponse),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr returns the socket path.
func (s *Server) Addr() string { return s.path }

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		sc := newSocketConn(conn)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.clients[sc] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handleConn(sc)
	}
}

func (s *Server) handleConn(sc *socketConn) {
	defer s.wg.Done()
	defer func() {
		sc.stop()
		_ = sc.conn.Close()
		s.mu.Lock()
		delete(s.clients, sc)
		s.mu.Unlock()
	}()

	s.wg.Add(1)
	go s.writeLoop(sc)

	dec := json.NewDecoder(sc.conn)
	for {
		var req SocketRequest
		if err := dec.Decode(&req); err != nil {
			break
		}
		resp := s.dispatch(s.rootCtx, req)
		frame, err := encodeFrame(frameResponse, resp)
		if err != nil {
			break
		}
		if !sc.sendWait(s.rootCtx, frame) {
			break
		}
	}
}

func (s *Server) writeLoop(sc *socketConn) {
	defer s.wg.Done()
	enc := json.NewEncoder(sc.conn)
	for {
		select {
		case f := <-sc.out:
			if err := enc.Encode(f); err != nil {
				sc.stop()
				_ = sc.conn.Close()
				return
			}
		case <-sc.done:
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req SocketRequest) SocketResponse {
	refused := func(reason string) SocketResponse {
		return SocketResponse{ID: req.ID, OK: false, Command: req.Command, WatchID: req.WatchID, Error: reason}
	}
	if reason := validationError(req); reason != "" {
		return refused(reason)
	}
	if key, ok := dedupKey(req); ok {
		s.mu.Lock()
		cached, found := s.dedup[key]
		s.mu.Unlock()
		if found {
			return cached
		}
	}

	resp, err := s.handler.Handle(ctx, req)
	if err != nil {
		return refused(err.Error())
	}
	resp.ID = req.ID
	if resp.Command == "" {
		resp.Command = req.Command
	}
	if resp.WatchID == "" {
		resp.WatchID = req.WatchID
	}
	if key, ok := dedupKey(req); ok {
		s.remember(key, resp)
	}
	return resp
}

func (s *Server) remember(key string, resp SocketResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.dedup[key]; exists {
		return
	}
	s.dedup[key] = resp
	s.order = append(s.order, key)
	if len(s.order) > maxDedup {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.dedup, oldest)
	}
}

// Publish pushes one event to every attached agent. It never blocks on an
// agent; a full queue loses the event for that agent only.
func (s *Server) Publish(ev SocketEvent) {
	frame, err := encodeFrame(frameEvent, ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	targets := make([]*socketConn, 0, len(s.clients))
	for sc := range s.clients {
		targets = append(targets, sc)
	}
	s.mu.Unlock()
	for _, sc := range targets {
		sc.send(frame)
	}
}

// Close stops accepting, detaches every agent, unlinks the socket file, and
// waits for goroutines to finish. Repeated calls are safe.
func (s *Server) Close() error {
	var closeErr error
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		targets := make([]*socketConn, 0, len(s.clients))
		for sc := range s.clients {
			targets = append(targets, sc)
		}
		s.mu.Unlock()

		s.cancel()
		closeErr = s.ln.Close()
		for _, sc := range targets {
			sc.stop()
			_ = sc.conn.Close()
		}
		s.wg.Wait()
	})
	return closeErr
}

// ControlClient is an agent's end of the control socket. Send writes one
// request and waits for its response. Events carries pushed notifications.
type ControlClient struct {
	conn      net.Conn
	enc       *json.Encoder
	dec       *json.Decoder
	events    chan SocketEvent
	responses chan SocketResponse

	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once

	sendMu sync.Mutex
	errMu  sync.Mutex
	err    error
}

// Dial attaches to a running TUI socket. The context bounds the dial only.
func Dial(ctx context.Context, path string) (*ControlClient, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("tui: dial %s: %w", path, err)
	}
	c := &ControlClient{
		conn:      conn,
		enc:       json.NewEncoder(conn),
		dec:       json.NewDecoder(conn),
		events:    make(chan SocketEvent, outQueue),
		responses: make(chan SocketResponse, outQueue),
		done:      make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *ControlClient) stop() {
	c.doneOnce.Do(func() { close(c.done) })
}

func (c *ControlClient) setErr(err error) {
	c.errMu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.errMu.Unlock()
}

func (c *ControlClient) failure() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err != nil {
		return c.err
	}
	return ErrSocketClosed
}

func (c *ControlClient) readLoop() {
	defer close(c.events)
	defer close(c.responses)
	for {
		var f wireFrame
		if err := c.dec.Decode(&f); err != nil {
			c.setErr(err)
			c.stop()
			return
		}
		switch f.Kind {
		case frameResponse:
			var resp SocketResponse
			if err := json.Unmarshal(f.Data, &resp); err != nil {
				continue
			}
			select {
			case c.responses <- resp:
			case <-c.done:
				return
			}
		case frameEvent:
			var ev SocketEvent
			if err := json.Unmarshal(f.Data, &ev); err != nil {
				continue
			}
			select {
			case c.events <- ev:
			case <-c.done:
				return
			default:
				// An agent that does not drain Events loses hints, never
				// responses it is actively waiting for.
			}
		}
	}
}

// Send writes one request and returns the matching response. A refused command
// arrives as a response with OK false; a transport failure arrives as an error.
// Calls are serialized so responses stay matched to their requests.
func (c *ControlClient) Send(ctx context.Context, req SocketRequest) (SocketResponse, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.done:
		return SocketResponse{}, c.failure()
	default:
	}
	if err := c.enc.Encode(req); err != nil {
		return SocketResponse{}, fmt.Errorf("tui: send request: %w", err)
	}
	select {
	case resp, ok := <-c.responses:
		if !ok {
			return SocketResponse{}, c.failure()
		}
		return resp, nil
	case <-ctx.Done():
		return SocketResponse{}, ctx.Err()
	case <-c.done:
		return SocketResponse{}, c.failure()
	}
}

// Events returns the channel of pushed notifications. It closes when the
// connection ends.
func (c *ControlClient) Events() <-chan SocketEvent { return c.events }

// Close ends the connection and releases the reader goroutine. Repeated calls
// are safe.
func (c *ControlClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.stop()
		err = c.conn.Close()
	})
	return err
}
