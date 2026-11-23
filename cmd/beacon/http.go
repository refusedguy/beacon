package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type apiServer struct {
	wiring      *wiring
	token       string
	eventStream <-chan core.HarnessEvent
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
		Action string `json:"action"`
		RunID  string `json:"run_id"`
		SHA    string `json:"sha"`
		PR     int    `json:"pr"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	id := core.RunID(in.RunID)
	var err error
	switch in.Action {
	case "watch":
		err = a.wiring.control.WatchPR(r.Context(), id, in.PR)
	case "approve":
		err = a.wiring.control.Approve(r.Context(), id, in.SHA)
	case "unblock":
		err = a.wiring.control.Unblock(r.Context(), id)
	case "cancel":
		err = a.wiring.control.Cancel(r.Context(), id)
	case "retry":
		err = a.wiring.control.Retry(r.Context(), id)
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
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
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
		case pr := <-a.wiring.changes:
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
