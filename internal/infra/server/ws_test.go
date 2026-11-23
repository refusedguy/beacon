package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockEventReader is an in-memory EventReader. It returns published envelopes
// with seq greater than the cursor, in ascending order, and never holds a
// writer lock.
type mockEventReader struct {
	mu     sync.Mutex
	events []Envelope
}

func (m *mockEventReader) ReadAfter(_ context.Context, cursor uint64, limit int) ([]Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Envelope, 0)
	for _, e := range m.events {
		if e.Seq > cursor {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *mockEventReader) publish(seqs ...uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, seq := range seqs {
		m.events = append(m.events, Envelope{
			Seq:           seq,
			Type:          "tick",
			SchemaVersion: SchemaVersion,
			Payload:       json.RawMessage(`{}`),
		})
	}
}

// observed is the union of the connected/control envelope and the wire
// envelope carried by an event frame. Both expose "type" and "seq".
type observed struct {
	Type          string `json:"type"`
	Seq           uint64 `json:"seq"`
	Message       string `json:"message"`
	SchemaVersion int    `json:"schema_version"`
}

type wsSession struct {
	client net.Conn
	reader *bufio.Reader
	done   <-chan struct{}
}

func startSession(t *testing.T, ws *WebSocket, cursor uint64) *wsSession {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	rw := bufio.NewReadWriter(bufio.NewReader(serverEnd), bufio.NewWriter(serverEnd))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ws.serve(newWSConn(serverEnd, rw), cursor, context.Background())
	}()
	return &wsSession{client: clientEnd, reader: bufio.NewReader(clientEnd), done: done}
}

// hijackRecorder is an http.ResponseWriter that can hijack into an in-memory
// pipe, so handshake tests never open a socket.
type hijackRecorder struct {
	header http.Header
	conn   net.Conn
	rw     *bufio.ReadWriter
}

func (h *hijackRecorder) Header() http.Header {
	if h.header == nil {
		h.header = make(http.Header)
	}
	return h.header
}
func (h *hijackRecorder) Write(p []byte) (int, error) { return h.rw.Write(p) }
func (h *hijackRecorder) WriteHeader(int)             {}
func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, h.rw, nil
}

func setWSHeaders(r *http.Request) {
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
}

func startHTTPSession(t *testing.T, ws *WebSocket, req *http.Request) *wsSession {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	rw := bufio.NewReadWriter(bufio.NewReader(serverEnd), bufio.NewWriter(serverEnd))
	h := &hijackRecorder{conn: serverEnd, rw: rw}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ws.ServeHTTP(h, req)
	}()

	reader := bufio.NewReader(clientEnd)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &wsSession{client: clientEnd, reader: reader, done: done}
}

func (s *wsSession) next(t *testing.T) observed {
	t.Helper()
	opcode, payload, masked, err := readFrame(s.reader)
	if err != nil {
		t.Fatal(err)
	}
	if opcode != wsOpcodeText {
		t.Fatalf("opcode = %x, want text frame", opcode)
	}
	if masked {
		t.Fatal("server sent a masked frame")
	}
	var o observed
	if err := json.Unmarshal(payload, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func (s *wsSession) closeAndWait(t *testing.T) {
	t.Helper()
	mask := [4]byte{1, 2, 3, 4}
	if err := writeFrame(s.client, wsOpcodeClose, nil, &mask); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("serve did not stop after client close")
	}
	_ = s.client.Close()
}

func TestWebSocketHandshake(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	rw := bufio.NewReadWriter(bufio.NewReader(serverEnd), bufio.NewWriter(serverEnd))
	h := &hijackRecorder{conn: serverEnd, rw: rw}
	ws := NewWebSocket(&mockEventReader{}, Config{Enabled: true, PollInterval: time.Millisecond})

	req := httptest.NewRequest("GET", "/api/v1/ws", nil)
	setWSHeaders(req)
	req.Header.Set("Connection", "keep-alive, Upgrade")

	done := make(chan struct{})
	go func() {
		defer close(done)
		ws.ServeHTTP(h, req)
	}()

	reader := bufio.NewReader(clientEnd)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(status, "HTTP/1.1 101") {
		t.Fatalf("status = %q, want 101 Switching Protocols", status)
	}
	accept := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(value)
		}
	}
	if want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; accept != want {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", accept, want)
	}

	opcode, payload, masked, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if opcode != wsOpcodeText || masked {
		t.Fatalf("connected frame opcode = %x, masked = %v", opcode, masked)
	}
	var hello observed
	if err := json.Unmarshal(payload, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Type != "connected" || hello.Seq != 0 {
		t.Fatalf("hello = %+v, want connected at seq 0", hello)
	}

	s := &wsSession{client: clientEnd, reader: reader, done: done}
	s.closeAndWait(t)
}

func TestWebSocketDisabledByDefault(t *testing.T) {
	ws := NewWebSocket(&mockEventReader{}, Config{})
	rec := httptest.NewRecorder()
	ws.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/ws", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (SSE stays the default transport)", rec.Code)
	}
}

func TestReplayFromCursor(t *testing.T) {
	r := &mockEventReader{}
	r.publish(1, 2, 3, 4, 5)
	ws := NewWebSocket(r, Config{Enabled: true, PollInterval: time.Millisecond})

	s := startSession(t, ws, 2)
	defer s.client.Close()
	if o := s.next(t); o.Type != "connected" || o.Seq != 2 {
		t.Fatalf("hello = %+v, want connected at seq 2", o)
	}
	for i, want := range []uint64{3, 4, 5} {
		if o := s.next(t); o.Seq != want {
			t.Fatalf("event %d seq = %d, want %d", i, o.Seq, want)
		}
	}
	s.closeAndWait(t)
}

func TestReconnectWithLastEventID(t *testing.T) {
	r := &mockEventReader{}
	r.publish(1, 2, 3)
	ws := NewWebSocket(r, Config{Enabled: true, PollInterval: time.Millisecond})

	first := httptest.NewRequest("GET", "/api/v1/ws", nil)
	setWSHeaders(first)
	s1 := startHTTPSession(t, ws, first)
	if o := s1.next(t); o.Type != "connected" || o.Seq != 0 {
		t.Fatalf("first hello = %+v, want connected at seq 0", o)
	}
	for i, want := range []uint64{1, 2, 3} {
		if o := s1.next(t); o.Seq != want {
			t.Fatalf("first stream event %d seq = %d, want %d", i, o.Seq, want)
		}
	}
	s1.closeAndWait(t)

	// Events appended while the client is away must appear only on the next
	// connection, and nothing the client already saw may repeat.
	r.publish(4, 5, 6)

	second := httptest.NewRequest("GET", "/api/v1/ws", nil)
	setWSHeaders(second)
	second.Header.Set("Last-Event-ID", "3")
	s2 := startHTTPSession(t, ws, second)
	if o := s2.next(t); o.Type != "connected" || o.Seq != 3 {
		t.Fatalf("reconnect hello = %+v, want connected at seq 3", o)
	}
	for i, want := range []uint64{4, 5, 6} {
		if o := s2.next(t); o.Seq != want {
			t.Fatalf("replayed event %d seq = %d, want %d", i, o.Seq, want)
		}
	}
	s2.closeAndWait(t)
}

// recordingParentNotifier captures notices delivered through the parent-chat
// port. A channel, when set, lets a test wait for a delivery from the reader
// goroutine without sleeping.
type recordingParentNotifier struct {
	mu      sync.Mutex
	notices []ParentNotice
	err     error
	ch      chan ParentNotice
}

func (n *recordingParentNotifier) Notify(_ context.Context, notice ParentNotice) error {
	n.mu.Lock()
	n.notices = append(n.notices, notice)
	n.mu.Unlock()
	if n.ch != nil {
		n.ch <- notice
	}
	return n.err
}

func (n *recordingParentNotifier) delivered() []ParentNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ParentNotice(nil), n.notices...)
}

func newLineageTree(t *testing.T) (*ChildThreads, *recordingParentNotifier) {
	t.Helper()
	threads := NewChildThreads()
	if err := threads.Root("root", "Root chat"); err != nil {
		t.Fatal(err)
	}
	if err := threads.Spawn("root", "child", "Builder"); err != nil {
		t.Fatal(err)
	}
	return threads, &recordingParentNotifier{}
}

func TestChildThreadsRoutesQuestionToParent(t *testing.T) {
	threads, notifier := newLineageTree(t)

	notice, err := threads.Ask(context.Background(), notifier.Notify, "child", "  which branch?  ")
	if err != nil {
		t.Fatal(err)
	}
	want := ParentNotice{
		ParentID: "root", ChildID: "child", ChildName: "Builder", Question: "which branch?",
	}
	if notice != want {
		t.Fatalf("notice = %+v, want %+v", notice, want)
	}
	if got := notifier.delivered(); len(got) != 1 || got[0] != want {
		t.Fatalf("delivered = %+v, want [%+v]", got, want)
	}

	name, parent, ok := threads.Get("child")
	if !ok || name != "Builder" || parent != "root" {
		t.Fatalf("Get(child) = %q, %q, %v", name, parent, ok)
	}
	if threads.Len() != 2 {
		t.Fatalf("Len = %d, want 2", threads.Len())
	}
}

func TestChildThreadsQuestionWithoutNotifier(t *testing.T) {
	threads, _ := newLineageTree(t)
	notice, err := threads.Ask(context.Background(), nil, "child", "second question")
	if err != nil {
		t.Fatal(err)
	}
	if notice.Question != "second question" || notice.ChildName != "Builder" {
		t.Fatalf("built notice = %+v", notice)
	}
}

func TestChildThreadsRejectsBadQuestions(t *testing.T) {
	threads, notifier := newLineageTree(t)
	cases := []struct {
		name     string
		childID  string
		question string
		want     error
	}{
		{name: "unknown child", childID: "missing", question: "why", want: ErrUnknownThread},
		{name: "empty question", childID: "child", question: "   ", want: ErrEmptyQuestion},
		{name: "root question", childID: "root", question: "why", want: ErrNoParent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := threads.Ask(context.Background(), notifier.Notify, tc.childID, tc.question); !errors.Is(err, tc.want) {
				t.Fatalf("Ask error = %v, want %v", err, tc.want)
			}
		})
	}
	if got := notifier.delivered(); len(got) != 0 {
		t.Fatalf("failed Ask delivered notices: %+v", got)
	}
}

func TestChildThreadsDeliveryError(t *testing.T) {
	threads, notifier := newLineageTree(t)
	boom := errors.New("delivery failed")
	notifier.err = boom
	notice, err := threads.Ask(context.Background(), notifier.Notify, "child", "retry me")
	if !errors.Is(err, boom) {
		t.Fatalf("Ask error = %v, want boom", err)
	}
	if notice.Question != "retry me" {
		t.Fatalf("notice = %+v, want question retry me", notice)
	}
}

func TestChildThreadsReferenceErrors(t *testing.T) {
	threads := NewChildThreads()
	if err := threads.Root(" ", "Blank"); !errors.Is(err, ErrInvalidThread) {
		t.Fatalf("Root blank error = %v, want ErrInvalidThread", err)
	}
	if err := threads.Spawn("root", "child", "Builder"); !errors.Is(err, ErrUnknownParent) {
		t.Fatalf("Spawn under missing parent error = %v, want ErrUnknownParent", err)
	}
	if err := threads.Root("root", "Root chat"); err != nil {
		t.Fatal(err)
	}
	if err := threads.Spawn("root", "child", "Builder"); err != nil {
		t.Fatal(err)
	}
	if err := threads.Spawn("root", "child", "Again"); !errors.Is(err, ErrDuplicateThread) {
		t.Fatalf("duplicate Spawn error = %v, want ErrDuplicateThread", err)
	}
	if err := threads.Spawn("root", "blank", "  "); !errors.Is(err, ErrInvalidThread) {
		t.Fatalf("blank name error = %v, want ErrInvalidThread", err)
	}
}

func TestChildThreadsEvictsOldestFirst(t *testing.T) {
	threads := NewChildThreads()
	for i := 0; i < maxChildThreads+1; i++ {
		if err := threads.Root("t-"+strconv.Itoa(i), "name"); err != nil {
			t.Fatalf("Root(%d): %v", i, err)
		}
	}

	if got := threads.Len(); got != maxChildThreads {
		t.Fatalf("Len = %d, want %d", got, maxChildThreads)
	}
	if _, _, ok := threads.Get("t-0"); ok {
		t.Fatal("oldest thread survived eviction")
	}
	if _, _, ok := threads.Get("t-" + strconv.Itoa(maxChildThreads)); !ok {
		t.Fatal("newest thread missing")
	}
}

func TestChildThreadsSpawnEvictsOldestFirst(t *testing.T) {
	threads := NewChildThreads()
	for i := 0; i < maxChildThreads; i++ {
		if err := threads.Root("t-"+strconv.Itoa(i), "name"); err != nil {
			t.Fatalf("Root(%d): %v", i, err)
		}
	}
	if err := threads.Spawn("t-1", "child", "child name"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if got := threads.Len(); got != maxChildThreads {
		t.Fatalf("Len = %d, want %d", got, maxChildThreads)
	}
	if _, _, ok := threads.Get("t-0"); ok {
		t.Fatal("oldest thread survived spawn eviction")
	}
	if name, parent, ok := threads.Get("child"); !ok || parent != "t-1" || name != "child name" {
		t.Fatalf("Get(child) = %q, %q, %v", name, parent, ok)
	}
}

func TestWebSocketQuestionFrameNotifiesParent(t *testing.T) {
	threads, notifier := newLineageTree(t)
	notifier.ch = make(chan ParentNotice, 1)
	ws := NewWebSocketWithLineage(&mockEventReader{}, Config{Enabled: true, PollInterval: time.Millisecond}, threads, notifier.Notify)

	s := startSession(t, ws, 0)
	defer s.client.Close()
	if o := s.next(t); o.Type != "connected" {
		t.Fatalf("hello = %+v, want connected", o)
	}

	mask := [4]byte{1, 2, 3, 4}
	frame := []byte(`{"type":"question","childID":"child","question":"which branch?"}`)
	if err := writeFrame(s.client, wsOpcodeText, frame, &mask); err != nil {
		t.Fatal(err)
	}

	select {
	case notice := <-notifier.ch:
		want := ParentNotice{ParentID: "root", ChildID: "child", ChildName: "Builder", Question: "which branch?"}
		if notice != want {
			t.Fatalf("notice = %+v, want %+v", notice, want)
		}
	case <-time.After(time.Second):
		t.Fatal("question frame did not reach the parent chat")
	}
	s.closeAndWait(t)
}

func TestEmptyStream(t *testing.T) {
	ws := NewWebSocket(&mockEventReader{}, Config{Enabled: true, PollInterval: time.Millisecond})
	s := startSession(t, ws, 0)
	if o := s.next(t); o.Type != "connected" || o.Seq != 0 {
		t.Fatalf("hello = %+v, want connected at seq 0", o)
	}

	// An idle journal produces no frames. A blocked read proves that: if an
	// event arrived it would unblock, so the select would report it. Closing
	// the client end releases the reader and stops the stream.
	frames := make(chan observed, 1)
	errs := make(chan error, 1)
	go func() {
		opcode, payload, _, err := readFrame(s.reader)
		if err != nil {
			errs <- err
			return
		}
		if opcode != wsOpcodeText {
			errs <- fmt.Errorf("opcode = %x, want text frame", opcode)
			return
		}
		var o observed
		if err := json.Unmarshal(payload, &o); err != nil {
			errs <- err
			return
		}
		frames <- o
	}()

	select {
	case o := <-frames:
		t.Fatalf("unexpected frame on an empty stream: %+v", o)
	case err := <-errs:
		t.Fatalf("unexpected read error on an empty stream: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	_ = s.client.Close()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("serve did not stop after client close")
	}
}
