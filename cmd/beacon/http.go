package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// subscriberBuffer is the number of PR changes queued for one monitor. A
// monitor that falls further behind loses the extra changes and sees the run
// state on a later refresh. The drop keeps one slow connection from blocking
// the other monitors or the broadcaster.
const subscriberBuffer = 16

// changeSubscribers broadcasts PR change numbers to one channel per SSE
// connection. Each connection owns its channel, so every monitor sees every
// change.
type changeSubscribers struct {
	mu   sync.Mutex
	subs map[chan int]struct{}
}

func newChangeSubscribers() *changeSubscribers {
	return &changeSubscribers{subs: make(map[chan int]struct{})}
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

type apiServer struct {
	wiring      *wiring
	token       string
	eventStream <-chan core.HarnessEvent
	changes     *changeSubscribers
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
			subtle.ConstantTimeCompare([]byte(header[7:]), []byte(a.token)) != 1 {
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
	runs, err := a.wiring.status.Active(r.Context())
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, runs)
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
	var err error
	switch action {
	case core.ControlWatch:
		err = a.wiring.control.WatchPR(r.Context(), id, in.PR)
	case core.ControlApprove:
		err = a.wiring.control.Approve(r.Context(), id, in.SHA)
	case core.ControlUnblock:
		err = a.wiring.control.Unblock(r.Context(), id)
	case core.ControlCancel:
		err = a.wiring.control.Cancel(r.Context(), id)
	case core.ControlRetry:
		err = a.wiring.control.Retry(r.Context(), id)
	case core.ControlNudge:
		err = a.wiring.control.Nudge(r.Context(), id, in.Message)
	default:
		http.Error(rw, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(rw, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(rw, map[string]string{"status": "ok"})
}

func (a *apiServer) events(rw http.ResponseWriter, r *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	changes, unsubscribe := a.changes.subscribe()
	defer unsubscribe()
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	// Send the headers now so the monitor's request returns before the first
	// event. Otherwise the connection appears to hang until the first ping.
	rw.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	events := a.eventStream
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			writeSSE(rw, "harness", ev)
			flusher.Flush()
		case pr := <-changes:
			writeSSE(rw, "pr_changed", map[string]int{"pr": pr})
			flusher.Flush()
		case <-ticker.C:
			writeSSE(rw, "ping", map[string]string{"time": time.Now().UTC().Format(time.RFC3339)})
			flusher.Flush()
		}
	}
}

func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}
