package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
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

// runsCache holds the serialized runs response for one change generation. A
// generation ends when the harness or a pull request changes. Every runs GET
// in a generation reads the same bytes, so eight monitors reacting to one
// notification cause one status.Active call instead of eight.
//
// The mutex is held while the snapshot is built. That serializes concurrent
// GETs and guarantees a single store read per generation.
type runsCache struct {
	mu   sync.Mutex
	body []byte
}

// invalidate ends the current generation. The next runs GET rebuilds the
// snapshot from the store.
func (c *runsCache) invalidate() {
	c.mu.Lock()
	c.body = nil
	c.mu.Unlock()
}

// snapshot returns the cached JSON for the current generation, building it
// once when the cache is empty. The bytes match writeJSON exactly, including
// the trailing newline from json.Encoder.
func (c *runsCache) snapshot(ctx context.Context, status apiStatus) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body != nil {
		return c.body, nil
	}
	runs, err := status.Active(ctx)
	if err != nil {
		return nil, err
	}
	// Marshal writes the same bytes as Encoder.Encode. Appending the newline
	// here avoids an Encoder and a bytes.Buffer on every generation.
	b, err := json.Marshal(core.NewWireRuns(runs))
	if err != nil {
		return nil, err
	}
	c.body = append(b, '\n')
	return c.body, nil
}

// changeSubscribers broadcasts PR change numbers to one channel per SSE
// connection. Each connection owns its channel, so every monitor sees every
// change. It also owns the runs snapshot, because it sees each PR change once
// at the source and can end the generation before the monitors react.
type changeSubscribers struct {
	mu       sync.Mutex
	subs     map[chan int]struct{}
	snapshot *runsCache
}

func newChangeSubscribers() *changeSubscribers {
	return &changeSubscribers{
		subs:     make(map[chan int]struct{}),
		snapshot: &runsCache{},
	}
}

// subscribe adds a connection and returns its channel plus an unsubscribe
// function. The caller must call unsubscribe when the connection closes.
func (s *changeSubscribers) subscribe() (<-chan int, func()) {
	ch := make(chan int, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// run copies each PR change to every subscriber. A full subscriber channel is
// skipped so a slow monitor cannot stall the broadcast.
func (s *changeSubscribers) run(ctx context.Context, src <-chan int) {
	for {
		select {
		case <-ctx.Done():
			return
		case pr := <-src:
			s.snapshot.invalidate()
			s.mu.Lock()
			for ch := range s.subs {
				select {
				case ch <- pr:
				default:
				}
			}
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
// json.Marshal per event no matter how many monitors are connected. Every
// subscriber receives the same bytes.
type eventFrames struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	src     <-chan core.HarnessEvent
	marshal func(any) ([]byte, error)
}

// newEventFrames builds a broadcaster that reads harness events from src.
// marshal encodes each event payload once, before any monitor sees the frame.
func newEventFrames(src <-chan core.HarnessEvent, marshal func(any) ([]byte, error)) *eventFrames {
	return &eventFrames{
		subs:    make(map[chan []byte]struct{}),
		src:     src,
		marshal: marshal,
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

// run reads each harness event, builds one frame, and hands the same bytes to
// every subscriber. A full subscriber channel is skipped so a slow monitor
// cannot stall the broadcast or the other monitors.
func (f *eventFrames) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-f.src:
			if !ok {
				return
			}
			b, err := f.marshal(core.NewWireEvent(ev))
			if err != nil {
				continue
			}
			size := len(harnessFramePrefix) + len(b) + len(harnessFrameSuffix)
			frame := appendHarnessFrame(make([]byte, 0, size), b)
			f.mu.Lock()
			for ch := range f.subs {
				select {
				case ch <- frame:
				default:
				}
			}
			f.mu.Unlock()
		}
	}
}

// pingBroadcaster owns the one keepalive ticker for every SSE connection. Each
// tick builds a single payload and hands the same frame bytes to all subscribers,
// so neither the timer nor the JSON work grows with the monitor count.
type pingBroadcaster struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newPingBroadcaster() *pingBroadcaster {
	return &pingBroadcaster{
		subs: make(map[chan []byte]struct{}),
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

// pingFrame builds one keepalive frame per tick. Each monitor writes these same
// bytes after its own SSE id line.
func pingFrame(now time.Time) []byte {
	// An RFC3339 UTC timestamp contains only digits, '-', 'T', ':', '.' and
	// 'Z', so it needs no JSON escaping. Building the payload directly skips
	// the json.Marshal allocation. The bytes match wirePing exactly.
	ts := now.UTC().Format(time.RFC3339)
	frame := make([]byte, 0, len(ts)+32)
	frame = append(frame, "event: ping\ndata: {\"time\":\""...)
	frame = append(frame, ts...)
	frame = append(frame, "\"}\n\n"...)
	return frame
}

// run broadcasts one ping per tick. A full subscriber channel is skipped so a
// slow monitor cannot stall the other connections.
func (p *pingBroadcaster) run(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			frame := pingFrame(now)
			p.mu.Lock()
			for ch := range p.subs {
				select {
				case ch <- frame:
				default:
				}
			}
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
	frames  *eventFrames
	changes *changeSubscribers
	pings   *pingBroadcaster
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
	frames := newEventFrames(views[0], json.Marshal)
	go frames.run(ctx)
	pings := newPingBroadcaster()
	go pings.run(ctx)
	api := &apiServer{services: services, daemonCtx: ctx, token: []byte(token), frames: frames, changes: changes, pings: pings}
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

	// Every frame carries a monotonic id so a monitor that reconnects can send
	// Last-Event-ID and ask to resume. Seeding from that header keeps the id
	// sequence increasing across reconnects. The event and data lines keep
	// their documented shape; the id line is an SSE field before them.
	nextID := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			nextID = n
		}
	}
	// The id buffer is reused across frames. A 24-byte array holds "id: "
	// plus the 19 digits of the largest int64 plus the newline, so no frame
	// write allocates.
	var idBuf [24]byte
	writeID := func() {
		nextID++
		_, _ = rw.Write(appendSSEID(idBuf[:0], nextID))
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-daemonDone:
			return
		case frame := <-frames:
			writeID()
			_, _ = rw.Write(frame)
			flusher.Flush()
		case pr := <-changes:
			writeID()
			writeSSE(rw, "pr_changed", wirePRChanged{PR: pr})
			flusher.Flush()
		case frame := <-pings:
			writeID()
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

func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := make([]byte, 0, len(event)+len(b)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, event...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, b...)
	frame = append(frame, "\n\n"...)
	_, _ = rw.Write(frame)
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}
