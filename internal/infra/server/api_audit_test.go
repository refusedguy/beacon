package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubAuditReader mirrors the store's ListAudit: it applies the actor and
// ingress filter it is given and returns entries oldest first. The error and
// call counters let a test prove the projection reads the port once.
type stubAuditReader struct {
	entries    []AuditEntry
	err        error
	calls      int
	lastFilter AuditFilter
}

func (s *stubAuditReader) ListAudit(_ context.Context, f AuditFilter) ([]AuditEntry, error) {
	s.calls++
	s.lastFilter = f
	if s.err != nil {
		return nil, s.err
	}
	out := make([]AuditEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if f.Actor != "" && e.Actor != f.Actor {
			continue
		}
		if f.Ingress != "" && e.Ingress != f.Ingress {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

var auditBase = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func sampleAuditEntries() []AuditEntry {
	return []AuditEntry{
		{Actor: "ses_exec", Ingress: AuditIngressMCP, CommandID: "c1", Action: "approve", At: auditBase},
		{Actor: "ses_orch", Ingress: AuditIngressWebhook, CommandID: "c2", Action: "merge", At: auditBase.Add(time.Minute)},
		{Actor: "ses_exec", Ingress: AuditIngressTUI, CommandID: "c3", Action: "wake", At: auditBase.Add(2 * time.Minute)},
		{Actor: "ses_orch", Ingress: AuditIngressMCP, CommandID: "c4", Action: "close", At: auditBase.Add(3 * time.Minute)},
	}
}

func auditCommandIDs(entries []AuditEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.CommandID)
	}
	return ids
}

func TestAuditPageFilters(t *testing.T) {
	cases := []struct {
		name   string
		query  AuditQuery
		want   []string
		filter AuditFilter
	}{
		{name: "all", query: AuditQuery{Limit: 10}, want: []string{"c1", "c2", "c3", "c4"}, filter: AuditFilter{}},
		{name: "actor", query: AuditQuery{Actor: "ses_exec", Limit: 10}, want: []string{"c1", "c3"},
			filter: AuditFilter{Actor: "ses_exec"}},
		{name: "ingress", query: AuditQuery{Ingress: AuditIngressMCP, Limit: 10}, want: []string{"c1", "c4"},
			filter: AuditFilter{Ingress: AuditIngressMCP}},
		{name: "actor and ingress", query: AuditQuery{Actor: "ses_orch", Ingress: AuditIngressMCP, Limit: 10},
			want: []string{"c4"}, filter: AuditFilter{Actor: "ses_orch", Ingress: AuditIngressMCP}},
		{name: "from", query: AuditQuery{From: auditBase.Add(2 * time.Minute), Limit: 10}, want: []string{"c3", "c4"}},
		{name: "to", query: AuditQuery{To: auditBase.Add(time.Minute), Limit: 10}, want: []string{"c1", "c2"}},
		{name: "time range", query: AuditQuery{From: auditBase.Add(time.Minute), To: auditBase.Add(2 * time.Minute), Limit: 10},
			want: []string{"c2", "c3"}},
		{name: "no match", query: AuditQuery{Actor: "nobody", Limit: 10}, want: []string{}, filter: AuditFilter{Actor: "nobody"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &stubAuditReader{entries: sampleAuditEntries()}
			page, err := NewAuditProjection(reader).Page(context.Background(), tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := auditCommandIDs(page.Entries); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("command ids = %v, want %v", got, tc.want)
			}
			if page.Total != len(tc.want) {
				t.Fatalf("total = %d, want %d", page.Total, len(tc.want))
			}
			if reader.calls != 1 {
				t.Fatalf("reader calls = %d, want 1", reader.calls)
			}
			if reader.lastFilter != tc.filter {
				t.Fatalf("port filter = %+v, want %+v", reader.lastFilter, tc.filter)
			}
		})
	}
}

func TestAuditPagination(t *testing.T) {
	entries := make([]AuditEntry, 5)
	for i := range entries {
		entries[i] = AuditEntry{CommandID: "c" + strconv.Itoa(i+1), At: auditBase.Add(time.Duration(i) * time.Minute)}
	}
	projection := NewAuditProjection(&stubAuditReader{entries: entries})
	q := AuditQuery{Limit: 2}
	seen := make([]string, 0, len(entries))
	pages := 0
	for {
		page, err := projection.Page(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Total != len(entries) {
			t.Fatalf("total = %d on page %d, want %d", page.Total, pages, len(entries))
		}
		seen = append(seen, auditCommandIDs(page.Entries)...)
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
		if pages > len(entries) {
			t.Fatal("pagination did not terminate")
		}
	}
	want := []string{"c1", "c2", "c3", "c4", "c5"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("paged ids = %v, want %v", seen, want)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 for five entries at limit 2", pages)
	}
}

func TestAuditPageEmpty(t *testing.T) {
	page, err := NewAuditProjection(&stubAuditReader{}).Page(context.Background(), AuditQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Entries == nil || len(page.Entries) != 0 {
		t.Fatalf("empty entries = %+v, want an empty non-nil slice", page.Entries)
	}
	if page.Total != 0 || page.NextCursor != "" {
		t.Fatalf("empty page total/cursor = %d/%q, want 0/empty", page.Total, page.NextCursor)
	}
}

func TestAuditPageRedactsTokens(t *testing.T) {
	token := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	pat := "github_pat_abcdefghijklmnopqrstuvwxyz_0123456789"
	auth := "Authorization: Bearer secretvalue1234567890"
	urlCred := "visit https://user:password@example.com/x for details"
	entries := []AuditEntry{{
		Actor:     "ses_" + pat,
		Ingress:   AuditIngressMCP,
		CommandID: auth,
		Action:    "merge " + token + " " + urlCred,
		At:        auditBase,
	}}

	page, err := NewAuditProjection(&stubAuditReader{entries: entries}).Page(context.Background(), AuditQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, secret := range []string{token, pat, "secretvalue1234567890", "user:password"} {
		if strings.Contains(body, secret) {
			t.Fatalf("audit export leaked %q: %s", secret, body)
		}
	}
	got := page.Entries[0]
	if !strings.Contains(got.Actor, auditRedacted) || !strings.Contains(got.CommandID, auditRedacted) ||
		!strings.Contains(got.Action, auditRedacted) {
		t.Fatalf("redacted entry = %+v, want a marker in every secret-bearing field", got)
	}
	if got.Actor == entries[0].Actor || got.CommandID == entries[0].CommandID || got.Action == entries[0].Action {
		t.Fatal("redaction did not change the source entry")
	}
	if !strings.Contains(got.Action, "example.com/x") {
		t.Fatalf("redaction dropped the non-secret URL part: %q", got.Action)
	}
}

func TestAuditReaderError(t *testing.T) {
	sentinel := errors.New("store down")
	projection := NewAuditProjection(&stubAuditReader{err: sentinel})
	if _, err := projection.Page(context.Background(), AuditQuery{}); !errors.Is(err, sentinel) {
		t.Fatalf("Page error = %v, want wrapped %v", err, sentinel)
	}

	rr := httptest.NewRecorder()
	projection.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, AuditPath, nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var body auditErrorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "AUDIT_FAILED" || body.OK {
		t.Fatalf("error body = %+v, want AUDIT_FAILED", body)
	}
}

func TestAuditHandlerFiltersAndEmptyState(t *testing.T) {
	path := AuditPath + "?actor=ses_exec&ingress=mcp&from=" +
		urlTime(auditBase) + "&to=" + urlTime(auditBase.Add(3*time.Minute))
	rr := httptest.NewRecorder()
	NewAuditProjection(&stubAuditReader{entries: sampleAuditEntries()}).Handler().
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var page AuditPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if got := auditCommandIDs(page.Entries); !reflect.DeepEqual(got, []string{"c1"}) {
		t.Fatalf("filtered ids = %v, want c1", got)
	}
	if page.Total != 1 {
		t.Fatalf("total = %d, want 1", page.Total)
	}

	empty := httptest.NewRecorder()
	NewAuditProjection(&stubAuditReader{}).Handler().
		ServeHTTP(empty, httptest.NewRequest(http.MethodGet, AuditPath, nil))
	var emptyPage AuditPage
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyPage); err != nil {
		t.Fatal(err)
	}
	if emptyPage.Entries == nil || len(emptyPage.Entries) != 0 {
		t.Fatalf("empty response entries = %+v, want []", emptyPage.Entries)
	}
}

func TestAuditHandlerReadOnly(t *testing.T) {
	rr := httptest.NewRecorder()
	NewAuditProjection(&stubAuditReader{}).Handler().
		ServeHTTP(rr, httptest.NewRequest(http.MethodPost, AuditPath, nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestAuditHandlerRejectsInvalidQuery(t *testing.T) {
	for _, path := range []string{
		AuditPath + "?cursor=not-base64!",
		AuditPath + "?cursor=LTEx", // base64 of "-11"
		AuditPath + "?limit=0",
		AuditPath + "?limit=abc",
		AuditPath + "?ingress=unknown",
		AuditPath + "?from=yesterday",
		AuditPath + "?to=tomorrow",
		AuditPath + "?from=" + urlTime(auditBase.Add(2*time.Minute)) + "&to=" + urlTime(auditBase),
	} {
		rr := httptest.NewRecorder()
		NewAuditProjection(&stubAuditReader{entries: sampleAuditEntries()}).Handler().
			ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, rr.Code)
		}
	}
}

func TestAuditNilReaderIsAnError(t *testing.T) {
	var projection *AuditProjection
	if _, err := projection.Page(context.Background(), AuditQuery{}); err == nil {
		t.Fatal("nil projection Page returned no error")
	}
	if _, err := NewAuditProjection(nil).Page(context.Background(), AuditQuery{}); err == nil {
		t.Fatal("nil reader Page returned no error")
	}
}

func TestAuditPageClampsLimit(t *testing.T) {
	reader := &stubAuditReader{entries: sampleAuditEntries()}
	page, err := NewAuditProjection(reader).Page(context.Background(), AuditQuery{Limit: MaxAuditPageSize + 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != len(sampleAuditEntries()) {
		t.Fatalf("entries = %d, want all entries", len(page.Entries))
	}
}

// urlTime renders an instant for a query string without adding another
// dependency to the test.
func urlTime(t time.Time) string {
	return t.Format(time.RFC3339)
}
