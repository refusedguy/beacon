// Package server projects Beacon's read models into HTTP transports. This file
// serves the read-only /api/v1/watches endpoint through a narrow WatchReader
// port. The projection never opens SQLite and never imports a sibling infra
// package: the composition root supplies the reader (BS.34, ADR 0194).
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// WatchesPath is the read-only route for the watches projection.
	WatchesPath = "/api/v1/watches"

	// DefaultWatchPageSize is the page size when the request omits limit.
	DefaultWatchPageSize = 50
	// MaxWatchPageSize bounds a page so one call cannot grow without limit.
	MaxWatchPageSize = 500
)

var (
	// ErrInvalidWatchCursor marks a cursor that is not an opaque page offset.
	ErrInvalidWatchCursor = errors.New("server: invalid watches cursor")
	// ErrInvalidWatchLimit marks a limit that is not a positive integer.
	ErrInvalidWatchLimit = errors.New("server: invalid watches limit")
)

// WatchReader is the read-only port the watches projection needs. Watches
// returns the current watches in the control loop. The composition root
// implements it over the store's live-watch read, the same set
// beacon_list_running projects. With no write method on the port, a handler
// holding only this dependency cannot mutate Beacon state.
type WatchReader interface {
	Watches(ctx context.Context) ([]domain.Watch, error)
}

// WatchReaderFunc adapts a read function to WatchReader. The wiring layer can
// pass a closure over the store without adding a type to this package.
type WatchReaderFunc func(ctx context.Context) ([]domain.Watch, error)

func (f WatchReaderFunc) Watches(ctx context.Context) ([]domain.Watch, error) { return f(ctx) }

// WatchCounts mirrors the counters of beacon_list_running: the counts cover
// every filtered watch, not just the current page, so a client paging through
// the list sees stable totals.
type WatchCounts struct {
	Watches     int            `json:"watches"`
	ByVerdict   map[string]int `json:"byVerdict"`
	Subscribers int            `json:"subscribers"`
}

// WatchesPage is the response of GET /api/v1/watches. Watches holds one page;
// NextCursor is empty on the last page. Each watch keeps its verdict, revision
// and subscribers, the same values beacon_list_running publishes, so TUI
// clients can read the projection without opening the store.
type WatchesPage struct {
	Watches    []domain.Watch `json:"watches"`
	Counts     WatchCounts    `json:"counts"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// WatchQuery is the parsed request for one page. Verdict and Status filter on
// the watch verdict and lifecycle status. Limit bounds the page and Cursor is
// the opaque offset returned as NextCursor by an earlier call.
type WatchQuery struct {
	Verdict string
	Status  string
	Limit   int
	Cursor  string
}

// WatchQueryFromRequest reads the query parameters of a watches request.
// Empty parameters select the default page; a malformed limit is an error so a
// client never silently gets a different page size than it asked for.
func WatchQueryFromRequest(r *http.Request) (WatchQuery, error) {
	q := WatchQuery{Limit: DefaultWatchPageSize}
	if r == nil || r.URL == nil {
		return q, nil
	}
	values := r.URL.Query()
	q.Verdict = strings.TrimSpace(values.Get("verdict"))
	q.Status = strings.TrimSpace(values.Get("status"))
	q.Cursor = strings.TrimSpace(values.Get("cursor"))
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return q, fmt.Errorf("%w: %q", ErrInvalidWatchLimit, raw)
		}
		q.Limit = n
	}
	return q, nil
}

// WatchProjection turns a reader's watches into filtered pages. It keeps only
// the reader and a default page size, so a page is a pure read of the port.
type WatchProjection struct {
	reader   WatchReader
	pageSize int
}

// NewWatchProjection builds a projection that reads pages of
// DefaultWatchPageSize watches.
func NewWatchProjection(reader WatchReader) *WatchProjection {
	return &WatchProjection{reader: reader, pageSize: DefaultWatchPageSize}
}

// Handler returns the read-only HTTP handler for WatchesPath. A non-GET method
// is refused with 405 so the route can never be used to change state.
func (p *WatchProjection) Handler() http.Handler {
	return http.HandlerFunc(p.handle)
}

// Page reads the port once, filters the watches and returns one page. Counts
// are computed over all filtered watches before paging. A nil reader is
// reported as an error rather than a panic so bad wiring cannot take the
// server down.
func (p *WatchProjection) Page(ctx context.Context, q WatchQuery) (WatchesPage, error) {
	if p == nil || p.reader == nil {
		return WatchesPage{}, errors.New("server: nil watch reader")
	}
	offset, err := decodeWatchCursor(q.Cursor)
	if err != nil {
		return WatchesPage{}, err
	}
	watches, err := p.reader.Watches(ctx)
	if err != nil {
		return WatchesPage{}, fmt.Errorf("server: read watches: %w", err)
	}

	matched := make([]domain.Watch, 0, len(watches))
	counts := WatchCounts{ByVerdict: make(map[string]int)}
	for _, w := range watches {
		if q.Verdict != "" && string(w.Verdict) != q.Verdict {
			continue
		}
		if q.Status != "" && string(w.Status) != q.Status {
			continue
		}
		matched = append(matched, w)
		counts.Watches++
		counts.ByVerdict[string(w.Verdict)]++
		counts.Subscribers += len(w.Subscribers)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = p.pageLimit()
	}
	if limit > MaxWatchPageSize {
		limit = MaxWatchPageSize
	}
	start := offset
	if start > len(matched) {
		start = len(matched)
	}
	end := start + limit
	if end > len(matched) || end < start {
		end = len(matched)
	}
	page := append(make([]domain.Watch, 0, end-start), matched[start:end]...)
	next := ""
	if end < len(matched) {
		next = encodeWatchCursor(end)
	}
	return WatchesPage{Watches: page, Counts: counts, NextCursor: next}, nil
}

func (p *WatchProjection) pageLimit() int {
	if p == nil || p.pageSize <= 0 {
		return DefaultWatchPageSize
	}
	return p.pageSize
}

func (p *WatchProjection) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWatchesError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET is supported")
		return
	}
	q, err := WatchQueryFromRequest(r)
	if err != nil {
		writeWatchesError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	page, err := p.Page(r.Context(), q)
	if err != nil {
		if errors.Is(err, ErrInvalidWatchCursor) || errors.Is(err, ErrInvalidWatchLimit) {
			writeWatchesError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
			return
		}
		writeWatchesError(w, http.StatusInternalServerError, "WATCHES_FAILED", err.Error())
		return
	}
	writeWatchesJSON(w, http.StatusOK, page)
}

func encodeWatchCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeWatchCursor(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidWatchCursor, err)
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidWatchCursor, s)
	}
	return offset, nil
}

func writeWatchesJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type watchesErrorBody struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeWatchesError(w http.ResponseWriter, status int, code, message string) {
	writeWatchesJSON(w, status, watchesErrorBody{Code: code, Message: message})
}
