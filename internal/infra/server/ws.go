// Package server projects the durable event journal into the server transport
// as SSE frames. This file adds an optional WebSocket transport over the same
// EventReader port (BS.40). It implements the small slice of RFC 6455 a server
// needs, so the package gains no external dependency and never imports store
// or app packages. SSE stays the default; WS is off until Config.Enabled.
package server

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsOpcodeText  = 0x1
	wsOpcodeClose = 0x8
	wsOpcodePing  = 0x9
	wsOpcodePong  = 0xA

	// wsGUID is the magic suffix from RFC 6455 section 1.3.
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	defaultWSPoll   = 500 * time.Millisecond
	wsWriteWait     = 5 * time.Second
	maxWSFrameBytes = 1 << 20
)

// Config controls the WebSocket transport. Enabled is false by default so the
// SSE endpoint in events.go remains the only active stream (BS.40 acceptance).
// PollInterval sets how often the stream checks the journal for new events.
type Config struct {
	Enabled      bool
	PollInterval time.Duration
}

// WebSocket is an HTTP handler that upgrades a request to a WebSocket and
// streams events from an EventReader. Replay starts at the cursor resolved
// from Last-Event-ID or ?replay, and a client can resume in place by sending a
// {"type":"resume","seq":N} text frame.
type WebSocket struct {
	cfg        Config
	projection *Projection
	poll       time.Duration
}

// NewWebSocket builds a WebSocket transport over reader. A nil reader is
// tolerated at construction and reported per request by the projection.
func NewWebSocket(reader EventReader, cfg Config) *WebSocket {
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = defaultWSPoll
	}
	return &WebSocket{cfg: cfg, projection: NewProjection(reader), poll: poll}
}

// ServeHTTP upgrades a valid handshake and serves the event stream. When the
// transport is disabled it answers 404 and never hijacks the connection.
func (ws *WebSocket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !ws.cfg.Enabled {
		http.Error(w, "websocket transport disabled", http.StatusNotFound)
		return
	}
	if !validWSHandshake(r) {
		http.Error(w, "not a websocket handshake", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	netConn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	if err := writeHandshake(rw, r.Header.Get("Sec-WebSocket-Key")); err != nil {
		_ = netConn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = netConn.Close()
		return
	}
	conn := newWSConn(netConn, rw)
	cursor, err := CursorFromRequest(r)
	if err != nil {
		_ = conn.WriteFrame(wsOpcodeText, errorMessage(err))
		_ = conn.Close()
		return
	}
	ws.serve(conn, cursor, r.Context())
}

// serve owns the connection from handshake to close. It replays pages from
// cursor without gaps or duplicates, then polls for new events until the
// client closes or the parent context is cancelled.
func (ws *WebSocket) serve(conn frameConn, cursor uint64, parent context.Context) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	if err := conn.WriteFrame(wsOpcodeText, connectedMessage(cursor)); err != nil {
		return
	}

	resumes := make(chan uint64, 8)
	go ws.readClient(conn, cancel, resumes)

	ticker := time.NewTicker(ws.poll)
	defer ticker.Stop()

	for {
		frames, err := ws.projection.Frames(ctx, cursor)
		if err != nil {
			_ = conn.WriteFrame(wsOpcodeText, errorMessage(err))
			return
		}
		for _, f := range frames {
			if f.ID <= cursor {
				continue
			}
			if err := conn.WriteFrame(wsOpcodeText, f.Data); err != nil {
				return
			}
			cursor = f.ID
		}
		// A full page likely has a successor; read it before waiting.
		if len(frames) >= PageSize {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case next := <-resumes:
			cursor = next
		case <-ticker.C:
		}
	}
}

// readClient consumes client frames. A close frame or read error cancels the
// stream; a ping is answered with a pong; a resume text frame moves the cursor.
func (ws *WebSocket) readClient(conn frameConn, cancel context.CancelFunc, resumes chan<- uint64) {
	for {
		opcode, payload, err := conn.ReadFrame()
		if err != nil {
			cancel()
			return
		}
		switch opcode {
		case wsOpcodeClose:
			cancel()
			return
		case wsOpcodePing:
			_ = conn.WriteFrame(wsOpcodePong, payload)
		case wsOpcodeText:
			var m clientMessage
			if json.Unmarshal(payload, &m) == nil && m.Type == "resume" {
				select {
				case resumes <- m.Seq:
				default:
				}
			}
		}
	}
}

// frameConn is the slice of a WebSocket connection the stream needs. It keeps
// the serve loop testable over an in-memory pipe.
type frameConn interface {
	WriteFrame(opcode byte, payload []byte) error
	ReadFrame() (opcode byte, payload []byte, err error)
	Close() error
}

// wsConn adapts a hijacked HTTP connection to frameConn. Writes are serialized
// because the reader goroutine may emit a pong while the stream emits events.
type wsConn struct {
	netConn net.Conn
	rw      *bufio.ReadWriter
	writeMu sync.Mutex
}

func newWSConn(c net.Conn, rw *bufio.ReadWriter) *wsConn {
	return &wsConn{netConn: c, rw: rw}
}

func (c *wsConn) WriteFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.netConn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
		return err
	}
	if err := writeFrame(c.rw, opcode, payload, nil); err != nil {
		return err
	}
	return c.rw.Flush()
}

func (c *wsConn) ReadFrame() (byte, []byte, error) {
	opcode, payload, masked, err := readFrame(c.rw)
	if err != nil {
		return 0, nil, err
	}
	if !masked {
		return 0, nil, errors.New("server: unmasked websocket client frame")
	}
	return opcode, payload, nil
}

func (c *wsConn) Close() error { return c.netConn.Close() }

// clientMessage is the small control envelope a client sends on a text frame.
type clientMessage struct {
	Type string `json:"type"`
	Seq  uint64 `json:"seq"`
}

func connectedMessage(cursor uint64) []byte {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Seq  uint64 `json:"seq"`
	}{"connected", cursor})
	return b
}

func errorMessage(err error) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{"error", err.Error()})
	return b
}

func validWSHandshake(r *http.Request) bool {
	return r != nil &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		hasToken(r.Header.Get("Connection"), "Upgrade") &&
		r.Header.Get("Sec-WebSocket-Version") == "13" &&
		r.Header.Get("Sec-WebSocket-Key") != ""
}

func hasToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeHandshake(w io.Writer, key string) error {
	_, err := fmt.Fprintf(w,
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n",
		wsAcceptKey(key))
	return err
}

// writeFrame emits one RFC 6455 frame. A non-nil mask marks a client frame and
// XORs the payload in place; server frames pass nil and stay unmasked.
func writeFrame(w io.Writer, opcode byte, payload []byte, mask *[4]byte) error {
	var head [14]byte
	head[0] = 0x80 | opcode // FIN + opcode
	n := len(payload)
	off := 2
	switch {
	case n < 126:
		head[1] = byte(n)
	case n <= 0xFFFF:
		head[1] = 126
		binary.BigEndian.PutUint16(head[2:4], uint16(n))
		off = 4
	default:
		head[1] = 127
		binary.BigEndian.PutUint64(head[2:10], uint64(n))
		off = 10
	}
	if mask != nil {
		head[1] |= 0x80
		copy(head[off:off+4], mask[:])
		off += 4
	}
	if _, err := w.Write(head[:off]); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if mask == nil {
		_, err := w.Write(payload)
		return err
	}
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.Write(masked)
	return err
}

// readFrame parses one frame and reports whether the sender masked it. The
// caller decides whether masking was required (clients must mask, servers may
// not). Fragmented client messages are not reassembled; control frames are
// required to be final and short.
func readFrame(r io.Reader) (opcode byte, payload []byte, masked bool, err error) {
	var head [2]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return 0, nil, false, err
	}
	opcode = head[0] & 0x0F
	fin := head[0]&0x80 != 0
	masked = head[1]&0x80 != 0
	n := int64(head[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, false, err
		}
		n = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, false, err
		}
		n = int64(binary.BigEndian.Uint64(ext[:]))
		if n < 0 || n > maxWSFrameBytes {
			return 0, nil, false, errors.New("server: websocket frame too large")
		}
	}
	if opcode >= 0x8 && (!fin || n > 125) {
		return 0, nil, false, errors.New("server: invalid websocket control frame")
	}
	var key [4]byte
	if masked {
		if _, err = io.ReadFull(r, key[:]); err != nil {
			return 0, nil, false, err
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, false, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= key[i%4]
		}
	}
	return opcode, payload, masked, nil
}
