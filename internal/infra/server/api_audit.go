// Package server projects the privileged-command audit log into a read-only
// HTTP endpoint. It reads through a narrow AuditReader port, so the projection
// never opens SQLite and never imports the store that owns the audit table
// (BS.50, ADR 0188). Time bounds, pagination and redaction belong to this
// layer; the port supplies only the actor and ingress filters the store knows.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// AuditPath is the read-only route for the audit projection.
	AuditPath = "/api/v1/audit"

	// DefaultAuditPageSize is the page size when the request omits limit.
	DefaultAuditPageSize = 50
	// MaxAuditPageSize bounds a page so one call cannot grow without limit.
	MaxAuditPageSize = 500
)

var (
	// ErrInvalidAuditCursor marks a cursor that is not an opaque page offset.
	ErrInvalidAuditCursor = errors.New("server: invalid audit cursor")
	// ErrInvalidAuditLimit marks a limit that is not a positive integer.
	ErrInvalidAuditLimit = errors.New("server: invalid audit limit")
	// ErrInvalidAuditIngress marks an ingress outside the three known values.
	ErrInvalidAuditIngress = errors.New("server: invalid audit ingress")
	// ErrInvalidAuditTime marks a time bound that is not an RFC3339 instant.
	ErrInvalidAuditTime = errors.New("server: invalid audit time")
)

// AuditIngress is the transport a privileged command arrived on. The three
// values mirror the store's ingresses without importing that package.
type AuditIngress string

const (
	AuditIngressMCP     AuditIngress = "mcp"
	AuditIngressTUI     AuditIngress = "tui"
	AuditIngressWebhook AuditIngress = "webhook"
)

func validAuditIngress(in AuditIngress) bool {
	switch in {
	case AuditIngressMCP, AuditIngressTUI, AuditIngressWebhook:
		return true
	default:
		return false
	}
}

// AuditEntry is one privileged command projected for HTTP. The wire shape
// matches the store row so an operator sees who acted, on which transport and
// when, without a second lookup.
type AuditEntry struct {
	Actor     string       `json:"actor"`
	Ingress   AuditIngress `json:"ingress"`
	CommandID string       `json:"commandID"`
	Action    string       `json:"action"`
	At        time.Time    `json:"at"`
}

// AuditFilter narrows the port read. Empty fields mean any; the two set fields
// combine with AND. Time bounds and pagination stay in this package.
type AuditFilter struct {
	Actor   string
	Ingress AuditIngress
}

// AuditReader is the read-only port the audit projection needs. ListAudit
// returns entries matching f, oldest first. The composition root implements
// it over the store's ListAudit. With no write method on the port, a handler
// holding only this dependency cannot change Beacon state.
type AuditReader interface {
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error)
}

// AuditReaderFunc adapts a read function to AuditReader so the composition
// root can pass a closure over the store without adding a type here.
type AuditReaderFunc func(ctx context.Context, f AuditFilter) ([]AuditEntry, error)

func (f AuditReaderFunc) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	return f(ctx, filter)
}

// AuditQuery is the parsed request for one page. From and To are inclusive
// UTC bounds; a zero value leaves that side open.
type AuditQuery struct {
	Actor   string
	Ingress AuditIngress
	From    time.Time
	To      time.Time
	Limit   int
	Cursor  string
}

// AuditQueryFromRequest reads the query parameters of an audit request.
// Malformed values are errors so a client never silently receives a different
// filter or page size than it asked for.
func AuditQueryFromRequest(r *http.Request) (AuditQuery, error) {
	q := AuditQuery{Limit: DefaultAuditPageSize}
	if r == nil || r.URL == nil {
		return q, nil
	}
	values := r.URL.Query()
	q.Actor = strings.TrimSpace(values.Get("actor"))
	q.Ingress = AuditIngress(strings.ToLower(strings.TrimSpace(values.Get("ingress"))))
	if q.Ingress != "" && !validAuditIngress(q.Ingress) {
		return q, fmt.Errorf("%w: %q", ErrInvalidAuditIngress, q.Ingress)
	}
	if raw := strings.TrimSpace(values.Get("from")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, fmt.Errorf("%w: from %q: %v", ErrInvalidAuditTime, raw, err)
		}
		q.From = t.UTC()
	}
	if raw := strings.TrimSpace(values.Get("to")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, fmt.Errorf("%w: to %q: %v", ErrInvalidAuditTime, raw, err)
		}
		q.To = t.UTC()
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return q, fmt.Errorf("%w: from is after to", ErrInvalidAuditTime)
	}
	q.Cursor = strings.TrimSpace(values.Get("cursor"))
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return q, fmt.Errorf("%w: %q", ErrInvalidAuditLimit, raw)
		}
		q.Limit = n
	}
	return q, nil
}

// AuditPage is the response of GET /api/v1/audit. Entries holds one page;
// Total covers every entry matching the query before paging. NextCursor is
// empty on the last page.
type AuditPage struct {
	Entries    []AuditEntry `json:"entries"`
	Total      int          `json:"total"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// AuditProjection turns a reader's entries into filtered, redacted pages.
type AuditProjection struct {
	reader   AuditReader
	pageSize int
}

// NewAuditProjection builds a projection that reads pages of
// DefaultAuditPageSize entries.
func NewAuditProjection(reader AuditReader) *AuditProjection {
	return &AuditProjection{reader: reader, pageSize: DefaultAuditPageSize}
}

// Handler returns the read-only HTTP handler for AuditPath. A non-GET method
// is refused with 405 so the route can never change state.
func (p *AuditProjection) Handler() http.Handler {
	return http.HandlerFunc(p.handle)
}

// Page reads the port once, applies the time bounds, pages the result and
// redacts each returned entry. A nil reader is reported as an error rather
// than a panic so bad wiring cannot take the server down.
func (p *AuditProjection) Page(ctx context.Context, q AuditQuery) (AuditPage, error) {
	if p == nil || p.reader == nil {
		return AuditPage{}, errors.New("server: nil audit reader")
	}
	offset, err := decodeAuditCursor(q.Cursor)
	if err != nil {
		return AuditPage{}, err
	}
	entries, err := p.reader.ListAudit(ctx, AuditFilter{Actor: q.Actor, Ingress: q.Ingress})
	if err != nil {
		return AuditPage{}, fmt.Errorf("server: list audit: %w", err)
	}

	matched := make([]AuditEntry, 0, len(entries))
	for _, e := range entries {
		if !q.From.IsZero() && e.At.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && e.At.After(q.To) {
			continue
		}
		matched = append(matched, e)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = p.pageLimit()
	}
	if limit > MaxAuditPageSize {
		limit = MaxAuditPageSize
	}
	start := offset
	if start > len(matched) {
		start = len(matched)
	}
	end := start + limit
	if end > len(matched) || end < start {
		end = len(matched)
	}
	page := make([]AuditEntry, 0, end-start)
	for _, e := range matched[start:end] {
		page = append(page, redactAuditEntry(e))
	}
	next := ""
	if end < len(matched) {
		next = encodeAuditCursor(end)
	}
	return AuditPage{Entries: page, Total: len(matched), NextCursor: next}, nil
}

func (p *AuditProjection) pageLimit() int {
	if p == nil || p.pageSize <= 0 {
		return DefaultAuditPageSize
	}
	return p.pageSize
}

func (p *AuditProjection) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAuditError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET is supported")
		return
	}
	q, err := AuditQueryFromRequest(r)
	if err != nil {
		writeAuditError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	page, err := p.Page(r.Context(), q)
	if err != nil {
		if errors.Is(err, ErrInvalidAuditCursor) || errors.Is(err, ErrInvalidAuditLimit) ||
			errors.Is(err, ErrInvalidAuditIngress) || errors.Is(err, ErrInvalidAuditTime) {
			writeAuditError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
			return
		}
		writeAuditError(w, http.StatusInternalServerError, "AUDIT_FAILED", err.Error())
		return
	}
	writeAuditJSON(w, http.StatusOK, page)
}

func encodeAuditCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeAuditCursor(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidAuditCursor, err)
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAuditCursor, s)
	}
	return offset, nil
}

var (
	// These patterns mirror the store's redaction filter for GitHub tokens,
	// Telegram bot tokens, Authorization credentials and URL credentials.
	// The projection cannot import the store (both are infra), so it keeps
	// its own copy at the boundary where entries leave the process.
	auditTokenRE    = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)
	auditTelegramRE = regexp.MustCompile(`[0-9]+:[A-Za-z0-9_-]{35}`)
	auditAuthRE     = regexp.MustCompile(`(?i)(authorization["'[:space:]]*[:=]["'[:space:]]*)(?:(?:bearer|token|basic)[[:space:]]+)?[A-Za-z0-9._~+/=-]+`)
	auditCredRE     = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@[:space:]:]+:[^/@[:space:]]+@`)

	// auditRedacted is the marker that replaces a secret in the export.
	auditRedacted = "[REDACTED]"
)

// redactAuditString removes GitHub and Telegram tokens, Authorization
// credentials and URL credentials from s. It never mutates its input.
func redactAuditString(s string) string {
	if s == "" {
		return s
	}
	out := auditTokenRE.ReplaceAllString(s, auditRedacted)
	out = auditTelegramRE.ReplaceAllString(out, auditRedacted)
	out = auditAuthRE.ReplaceAllString(out, "${1}"+auditRedacted)
	return auditCredRE.ReplaceAllString(out, "${1}"+auditRedacted+"@")
}

func redactAuditEntry(e AuditEntry) AuditEntry {
	e.Actor = redactAuditString(e.Actor)
	e.CommandID = redactAuditString(e.CommandID)
	e.Action = redactAuditString(e.Action)
	e.Ingress = AuditIngress(redactAuditString(string(e.Ingress)))
	return e
}

func writeAuditJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type auditErrorBody struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeAuditError(w http.ResponseWriter, status int, code, message string) {
	writeAuditJSON(w, status, auditErrorBody{Code: code, Message: message})
}
