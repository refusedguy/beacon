package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// bearerClient builds an HTTP client for loopback calls with the given
// timeout. Callers that probe a slower endpoint pass their own timeout.
func bearerClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// bearerAuth sets a Bearer Authorization header on req.
func bearerAuth(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

// subscriberBuffer is the number of PR changes queued for one monitor. A
// monitor that falls further behind loses the extra changes and sees the run
// state on a later refresh. The drop keeps one slow connection from blocking
// the other monitors or the broadcaster.
const subscriberBuffer = 16

// streamID is the one id sequence for every loopback notification. An id is
// assigned when a change is observed, before it enters any queue, so the id
// line and the payload id of a frame are the same value. A monitor that sees
// a gap can tell from the id alone that it missed notifications.
type streamID struct {
	next atomic.Uint64
}

func newStreamID() *streamID { return &streamID{} }

// assign returns the next id. The zero value is usable, so a broadcaster built
// without an explicit source still emits ordered ids.
func (s *streamID) assign() int64 {
	if s == nil {
		return 0
	}
	return int64(s.next.Add(1))
}

// runsCache holds the serialized runs response for one store revision. A
// revision advances when the harness or a pull request changes. Keying the
// bytes by revision means a repeated or out-of-order invalidation cannot serve
// a snapshot from the wrong generation, and every runs GET in one revision
// reads the same bytes.
//
// The mutex is held while the snapshot is built. That serializes concurrent
// GETs and guarantees a single store read per revision.
type runsCache struct {
	mu       sync.Mutex
	body     []byte
	revision uint64 // revision the body was built for
	version  uint64 // revision advanced by invalidation
}

// invalidate advances the store revision. The next runs GET rebuilds the
// snapshot from the store.
func (c *runsCache) invalidate() {
	c.mu.Lock()
	c.version++
	c.mu.Unlock()
}

// revision now reports the current store revision.
func (c *runsCache) currentRevision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// snapshot returns the cached JSON for the current revision, building it once
// when the cached bytes belong to an older revision. The bytes match writeJSON
// exactly, including the trailing newline from json.Encoder.
func (c *runsCache) snapshot(ctx context.Context, status apiStatus) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body != nil && c.revision == c.version {
		return c.body, nil
	}
	runs, err := status.Active(ctx)
	if err != nil {
		return nil, err
	}
	// Marshal writes the same bytes as Encoder.Encode. Appending the newline
	// here avoids an Encoder and a bytes.Buffer on every revision.
	b, err := json.Marshal(core.NewWireRuns(runs))
	if err != nil {
		return nil, err
	}
	c.body = append(b, '\n')
	c.revision = c.version
	return c.body, nil
}

// The two notification names that tell a monitor to re-read runs. A gap says
// a reconnect could not replay; a resync says a bounded queue dropped a frame.
// Neither carries run state.
const (
	streamEventResync = "resync"
	streamEventGap    = "gap"
)

// wireResync is the payload of both notifications. Reason says which gap the
// monitor is recovering from.
type wireResync struct {
	Reason string `json:"reason"`
}

// appendSSEFrame appends an event and data pair to dst and returns it.
func appendSSEFrame(dst []byte, event string, body []byte) []byte {
	dst = append(dst, "event: "...)
	dst = append(dst, event...)
	dst = append(dst, "\ndata: "...)
	dst = append(dst, body...)
	return append(dst, "\n\n"...)
}

// notificationFrame builds one complete id-bearing frame with the given name.
func notificationFrame(ids *streamID, event, reason string) []byte {
	id := ids.assign()
	body, _ := json.Marshal(wireResync{Reason: reason})
	frame := make([]byte, 0, 24+len("event: ")+len(event)+len(body)+24)
	frame = appendSSEID(frame, id)
	return appendSSEFrame(frame, event, body)
}

// resyncFrame replaces a dropped frame so a monitor cannot stay stale without
// seeing a notification.
func resyncFrame(ids *streamID, reason string) []byte {
	return notificationFrame(ids, streamEventResync, reason)
}

// gapFrame answers a reconnect that asked to resume. The daemon does not keep
// a replay buffer, so it tells the monitor to read runs instead of claiming
// that the Last-Event-ID was honoured.
func gapFrame(ids *streamID) []byte {
	return notificationFrame(ids, streamEventGap, "reconnect")
}

// broadcast queues one frame for every subscriber. When a channel is full the
// oldest queued frame is replaced with a resync marker, so the monitor learns
// that it missed a notification instead of silently staying stale.
func broadcast(subs map[chan []byte]struct{}, frame []byte, newResync func() []byte) {
	for ch := range subs {
		select {
		case ch <- frame:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- newResync():
			default:
			}
		}
	}
}

// changeSubscribers broadcasts complete PR change frames to one channel per
// SSE connection. Each connection owns its channel, so every monitor sees
// every change. It also owns the runs snapshot, because it sees each PR change
// once at the source and can advance the revision before the monitors react.
type changeSubscribers struct {
	mu       sync.Mutex
	subs     map[chan []byte]struct{}
	snapshot *runsCache
	ids      *streamID
}

func newChangeSubscribers() *changeSubscribers {
	return &changeSubscribers{
		subs:     make(map[chan []byte]struct{}),
		snapshot: &runsCache{},
		ids:      newStreamID(),
	}
}

// subscribe adds a connection and returns its channel plus an unsubscribe
// function. The caller must call unsubscribe when the connection closes.
func (s *changeSubscribers) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// run copies each PR change to every subscriber. The id is assigned here, at
// observation time, and travels in both the frame's id line and its payload.
// A full subscriber channel receives a resync marker in place of an older
// frame so a slow monitor cannot stall the broadcast or stay stale.
func (s *changeSubscribers) run(ctx context.Context, src <-chan int) {
	for {
		select {
		case <-ctx.Done():
			return
		case pr := <-src:
			id := s.ids.assign()
			s.snapshot.invalidate()
			body := strconv.AppendInt(nil, int64(pr), 10)
			frame := make([]byte, 0, 24+len("event: pr_changed\ndata: {\"pr\":")+len(body)+6)
			frame = appendSSEID(frame, id)
			frame = append(frame, "event: pr_changed\ndata: {\"pr\":"...)
			frame = append(frame, body...)
			frame = append(frame, "}\n\n"...)
			s.mu.Lock()
			broadcast(s.subs, frame, func() []byte { return resyncFrame(s.ids, "gap") })
			s.mu.Unlock()
		}
	}
}

// The fixed envelope around a harness event's JSON payload. The prefix and
// suffix are appended directly into the frame buffer instead of going through
// fmt, which boxes the payload into an interface and grows a nil buffer.
const (
	harnessFramePrefix = "event: harness\ndata: "
	harnessFrameSuffix = "\n\n"
)

// appendHarnessFrame appends one complete SSE frame onto dst and returns it. The
// caller supplies a buffer with capacity for the whole frame so the result is
// allocated once. The returned slice is immutable and is shared with every
// subscriber, so the caller must not append to it after broadcasting.
func appendHarnessFrame(dst, body []byte) []byte {
	dst = append(dst, harnessFramePrefix...)
	dst = append(dst, body...)
	return append(dst, harnessFrameSuffix...)
}

// eventFrames encodes each harness event once and broadcasts the finished SSE
// frame to one channel per monitor. Encoding at the source keeps one
// json.Marshal per event no matter how many monitors are connected. The id is
// assigned here, with the timestamp, before the payload is encoded. The
// finished frame carries that id in its id line, so every subscriber receives
// the same bytes and no monitor invents a competing sequence.
type eventFrames struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	src     <-chan core.HarnessEvent
	marshal func(any) ([]byte, error)
	ids     *streamID
}

// newEventFrames builds a broadcaster that reads harness events from src.
// marshal encodes each event payload once, before any monitor sees the frame.
func newEventFrames(src <-chan core.HarnessEvent, marshal func(any) ([]byte, error)) *eventFrames {
	return &eventFrames{
		subs:    make(map[chan []byte]struct{}),
		src:     src,
		marshal: marshal,
		ids:     newStreamID(),
	}
}

// subscribe returns a channel of complete SSE frames and an unsubscribe
// function. The caller must call unsubscribe when the connection closes.
func (f *eventFrames) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, subscriberBuffer)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}
}

// run reads each harness event, builds one frame with its id, and hands the
// same bytes to every subscriber. A full subscriber channel has an older frame
// replaced with a resync marker, so a slow monitor cannot stall the broadcast
// or stay stale without a notification.
func (f *eventFrames) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-f.src:
			if !ok {
				return
			}
			id := f.ids.assign()
			b, err := f.marshal(core.NewWireEvent(ev, id, time.Now().UTC()))
			if err != nil {
				continue
			}
			size := 24 + len(harnessFramePrefix) + len(b) + len(harnessFrameSuffix)
			frame := appendSSEID(make([]byte, 0, size), id)
			frame = appendHarnessFrame(frame, b)
			f.mu.Lock()
			broadcast(f.subs, frame, func() []byte { return resyncFrame(f.ids, "gap") })
			f.mu.Unlock()
		}
	}
}

// pingBroadcaster owns the one keepalive ticker for every SSE connection. Each
// tick assigns one id and builds a single id-bearing frame, then hands the same
// bytes to all subscribers. The ping reads runs in the monitor, so a connection
// that fell behind converges on the next tick.
type pingBroadcaster struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
	ids  *streamID
}

func newPingBroadcaster() *pingBroadcaster {
	return &pingBroadcaster{
		subs: make(map[chan []byte]struct{}),
		ids:  newStreamID(),
	}
}

// subscribe returns a channel of complete ping frames and an unsubscribe
// function. The caller must unsubscribe when the connection closes.
func (p *pingBroadcaster) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	p.mu.Lock()
	p.subs[ch] = struct{}{}
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.subs, ch)
		p.mu.Unlock()
	}
}

// pingFrame builds one complete keepalive frame. The id line carries the id
// assigned at tick time. An RFC3339 UTC timestamp contains only digits, '-',
// 'T', ':', '.' and 'Z', so its payload needs no JSON escaping.
func pingFrame(id int64, now time.Time) []byte {
	ts := now.UTC().Format(time.RFC3339)
	frame := make([]byte, 0, len(ts)+48)
	frame = appendSSEID(frame, id)
	frame = append(frame, "event: ping\ndata: {\"time\":\""...)
	frame = append(frame, ts...)
	frame = append(frame, "\"}\n\n"...)
	return frame
}

// run broadcasts one ping per tick. A full subscriber channel receives a
// resync marker in place of its older keepalive.
func (p *pingBroadcaster) run(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			id := p.ids.assign()
			frame := pingFrame(id, now)
			p.mu.Lock()
			broadcast(p.subs, frame, func() []byte { return resyncFrame(p.ids, "gap") })
			p.mu.Unlock()
		}
	}
}

// apiStatus and apiControl are the whole service surface the loopback API
// needs. Keeping them as interfaces prevents handlers from reaching the
// dispatcher, SCM, harness or task services behind the wiring root.
type apiStatus interface {
	Active(ctx context.Context) ([]*core.Run, error)
}

type apiControl interface {
	WatchPR(ctx context.Context, id core.RunID, pr int) error
	Approve(ctx context.Context, id core.RunID, sha string) error
	Unblock(ctx context.Context, id core.RunID) error
	Cancel(ctx context.Context, id core.RunID) error
	Retry(ctx context.Context, id core.RunID) error
	Nudge(ctx context.Context, id core.RunID, message string) error
}

// apiServices is the slice of the wiring root exposed to the API server.
type apiServices struct {
	status  apiStatus
	control apiControl
}

type apiServer struct {
	services apiServices
	// daemonCtx ends when the daemon stops. The SSE handler watches it in
	// addition to the request context, which srv.Shutdown does not cancel.
	daemonCtx context.Context
	// token holds the token bytes, converted once at construction so the auth
	// check does not allocate a byte slice on every request.
	token   []byte
	ids     *streamID
	frames  *eventFrames
	changes *changeSubscribers
	pings   *pingBroadcaster
}

// idSource returns the one id sequence shared by the three broadcasters. A
// handler built with only one broadcaster still emits ordered resume markers.
func (a *apiServer) idSource() *streamID {
	if a.ids != nil {
		return a.ids
	}
	if a.frames != nil {
		return a.frames.ids
	}
	if a.changes != nil {
		return a.changes.ids
	}
	if a.pings != nil {
		return a.pings.ids
	}
	return nil
}

// stringBytes returns a read-only byte view of s without allocating. Callers
// must not modify the returned slice.
func stringBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// runHTTP serves handler at addr until ctx ends and reports its exit on errCh.
// The loopback API and the webhook share this lifecycle, so both use the same
// read-header timeout and both shut down gracefully when ctx is cancelled.
func runHTTP(ctx context.Context, errCh chan<- error, addr string, handler http.Handler) {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Shutdown is data-loss handling: it gives in-flight requests a last
		// chance to finish. Keep the call here and log its error.
		logDiscard("http server shutdown", srv.Shutdown(shutdown))
	}()
	go func() { errCh <- srv.ListenAndServe() }()
}

// startAPIServer serves the loopback API until ctx ends and reports its exit
// on errCh. It owns the handler wiring, while runHTTP owns the server lifecycle.
func startAPIServer(ctx context.Context, services apiServices, token string, events <-chan core.HarnessEvent, changes *changeSubscribers, addr string, errCh chan<- error) {
	// One view feeds the frame broadcaster and one ends the snapshot
	// generation. A single consumer owns the invalidation view, so one harness
	// event invalidates once no matter how many monitors are connected.
	views := fanout(ctx, events, 2)
	go invalidateOnHarness(ctx, views[1], changes.snapshot)
	ids := newStreamID()
	changes.ids = ids
	frames := newEventFrames(views[0], json.Marshal)
	frames.ids = ids
	go frames.run(ctx)
	pings := newPingBroadcaster()
	pings.ids = ids
	go pings.run(ctx)
	api := &apiServer{services: services, daemonCtx: ctx, token: []byte(token), ids: ids, frames: frames, changes: changes, pings: pings}
	runHTTP(ctx, errCh, addr, api.handler())
}

// invalidateOnHarness ends the current snapshot generation whenever the
// harness reports an event. The caller runs one copy of this loop so each
// event ends exactly one generation.
func invalidateOnHarness(ctx context.Context, events <-chan core.HarnessEvent, cache *runsCache) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			cache.invalidate()
		}
	}
}

func (a *apiServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	mux.Handle("/api/v1/runs", a.auth(http.HandlerFunc(a.runs)))
	mux.Handle("/api/v1/events", a.auth(http.HandlerFunc(a.events)))
	mux.Handle("/api/v1/control", a.auth(http.HandlerFunc(a.control)))
	return mux
}

func (a *apiServer) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if len(header) < 8 || header[:7] != "Bearer " ||
			subtle.ConstantTimeCompare(stringBytes(header[7:]), a.token) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func (a *apiServer) runs(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// A disconnecting monitor must not discard the shared snapshot, so the
	// store read runs outside the request's cancellation.
	body, err := a.changes.snapshot.snapshot(context.WithoutCancel(r.Context()), a.services.status)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(body)
}

func (a *apiServer) control(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Action  string `json:"action"`
		RunID   string `json:"run_id"`
		SHA     string `json:"sha"`
		PR      int    `json:"pr"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	id := core.RunID(in.RunID)
	action := core.ParseControlAction(in.Action)
	if !action.Valid() {
		http.Error(rw, "unknown action", http.StatusBadRequest)
		return
	}
	err := app.ApplyControl(r.Context(), a.services.control, action, app.ControlArgs{
		RunID: id, SHA: in.SHA, PR: in.PR, Message: in.Message,
	})
	if err != nil {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(rw).Encode(core.NewWireControlRefusal(action, id, err.Error()))
		return
	}
	writeJSON(rw, core.NewWireControlResult(action, id))
}

func (a *apiServer) events(rw http.ResponseWriter, r *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Shutdown does not cancel active requests, so watch the daemon context as
	// well. A nil context leaves the channel nil and blocks that case, which is
	// the behaviour of handlers built directly in tests.
	var daemonDone <-chan struct{}
	if a.daemonCtx != nil {
		daemonDone = a.daemonCtx.Done()
	}
	frames, unsubscribeFrames := a.frames.subscribe()
	defer unsubscribeFrames()
	changes, unsubscribeChanges := a.changes.subscribe()
	defer unsubscribeChanges()
	// The server owns one keepalive ticker. A handler built without a
	// broadcaster (as in the wire-shape tests) simply receives no pings.
	var pings <-chan []byte
	if a.pings != nil {
		var unsubscribePings func()
		pings, unsubscribePings = a.pings.subscribe()
		defer unsubscribePings()
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	// Send the headers now so the monitor's request returns before the first
	// event. Otherwise the connection appears to hang until the first ping.
	rw.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Ids are assigned once at observation time and travel inside each frame.
	// The server does not replay frames for Last-Event-ID, so it does not claim
	// to resume. A reconnect instead gets one gap marker, which tells the
	// monitor to read runs and converge on the next poll.
	if r.Header.Get("Last-Event-ID") != "" {
		_, _ = rw.Write(gapFrame(a.idSource()))
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-daemonDone:
			return
		case frame := <-frames:
			_, _ = rw.Write(frame)
			flusher.Flush()
		case frame := <-changes:
			_, _ = rw.Write(frame)
			flusher.Flush()
		case frame := <-pings:
			_, _ = rw.Write(frame)
			flusher.Flush()
		}
	}
}

// wirePRChanged and wirePing are the data payloads of the two non-harness
// events on the loopback stream. Naming them keeps each emitted key set in one
// place. The monitor's wire contract fixtures pin the same key sets.
type wirePRChanged struct {
	PR int `json:"pr"`
}

type wirePing struct {
	Time string `json:"time"`
}

// appendSSEID appends one SSE id line to dst. Reusing the caller's buffer
// keeps the per-frame write allocation free.
func appendSSEID(dst []byte, id int64) []byte {
	dst = append(dst, "id: "...)
	dst = strconv.AppendInt(dst, id, 10)
	return append(dst, '\n')
}

// sseFrameBuffer holds one reusable frame buffer with a JSON encoder bound to
// it. writeSSE marshals the payload straight into the frame, so neither the
// encoded body nor the assembled frame allocates on a warm pool. The encoder
// is created once per pooled object, not once per write.
type sseFrameBuffer struct {
	buf []byte
	enc *json.Encoder
}

func newSSEFrameBuffer() *sseFrameBuffer {
	f := &sseFrameBuffer{}
	f.enc = json.NewEncoder(f)
	return f
}

// Write appends encoded JSON to the frame buffer. The event and data prefixes
// are already in place when json.Encoder calls it.
func (f *sseFrameBuffer) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	return len(p), nil
}

var sseFrameBufferPool = sync.Pool{New: func() any { return newSSEFrameBuffer() }}

// writeSSE writes one complete event/data frame. json.Encoder terminates the
// JSON value with a newline; dropping it leaves the single blank line that
// ends an SSE frame.
func writeSSE(rw http.ResponseWriter, event string, payload any) {
	f := sseFrameBufferPool.Get().(*sseFrameBuffer)
	defer sseFrameBufferPool.Put(f)
	f.buf = f.buf[:0]
	f.buf = append(f.buf, "event: "...)
	f.buf = append(f.buf, event...)
	f.buf = append(f.buf, "\ndata: "...)
	if err := f.enc.Encode(payload); err != nil {
		return
	}
	if n := len(f.buf); n > 0 && f.buf[n-1] == '\n' {
		f.buf = f.buf[:n-1]
	}
	f.buf = append(f.buf, "\n\n"...)
	_, _ = rw.Write(f.buf)
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}
