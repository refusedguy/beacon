package bench

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/internal/infra/scm"
)

// scmObserves bounds the smoke work. Ten observes alternate between two PRs so
// the pass pays both the populated-signal path and the changed-files fallback
// with the same count on every pass.
const scmObserves = 10

// stubRoundTripper serves fixed bodies in process so no benchmark pass pays
// socket setup or scheduler jitter. The harness spawn benchmark shares it.
type stubRoundTripper func(*http.Request) (*http.Response, error)

func (fn stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

// stubJSON builds a response with the same wire bodies the loopback servers
// used to return. The caller supplies the status so error paths stay covered.
func stubJSON(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		Status:     http.StatusText(status),
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// Fixed payloads keep the decoded bytes identical between base and head. PR 42
// carries populated checks over two pages, two review pages and a diverged
// comparison. PR 43 has no checks, so Observe takes the paginated files path.
const (
	pr42Payload    = `{"number":42,"state":"open","head":{"sha":"abc"},"base":{"sha":"base","ref":"main"},"mergeable_state":"clean"}`
	pr43Payload    = `{"number":43,"state":"open","head":{"sha":"def"},"base":{"sha":"base","ref":"main"},"mergeable_state":"clean"}`
	comparePayload = `{"behind_by":2,"ahead_by":1,"base_commit":{"sha":"base"}}`
)

func writeSCMBody(w http.ResponseWriter, body string) { _, _ = io.WriteString(w, body) }

func writeCheckRuns(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/abc/") {
		writeSCMBody(w, `{"total_count":0,"check_runs":[]}`)
		return
	}
	if r.URL.Query().Get("page") == "" {
		w.Header().Set("Link", `</repos/o/r/commits/abc/check-runs?page=2>; rel="next"`)
		writeSCMBody(w, `{"total_count":2,"check_runs":[{"name":"go test","status":"completed","conclusion":"success"}]}`)
		return
	}
	writeSCMBody(w, `{"total_count":2,"check_runs":[{"name":"gates","status":"completed","conclusion":"failure","output":{"title":"failed","text":"one advisory line"}}]}`)
}

func writeReviews(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/42/reviews") {
		writeSCMBody(w, `[{"user":{"login":"carol"},"state":"APPROVED","submitted_at":"2026-01-03T00:00:00Z"}]`)
		return
	}
	if r.URL.Query().Get("page") == "" {
		w.Header().Set("Link", `</repos/o/r/pulls/42/reviews?page=2>; rel="next"`)
		writeSCMBody(w, `[{"user":{"login":"alice"},"state":"CHANGES_REQUESTED","submitted_at":"2026-01-01T00:00:00Z"}]`)
		return
	}
	writeSCMBody(w, `[{"user":{"login":"bob"},"state":"APPROVED","submitted_at":"2026-01-02T00:00:00Z"}]`)
}

func writeChangedFiles(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("page") == "" {
		w.Header().Set("Link", `</repos/o/r/pulls/43/files?page=2>; rel="next"`)
		writeSCMBody(w, `[{"filename":"docs/a.md"}]`)
		return
	}
	writeSCMBody(w, `[{"filename":"internal/app/a.go"}]`)
}

// scmObserveHandler serves the Observe endpoints over loopback HTTP. Each
// request is counted so the harness can report the work a pass actually drove.
func scmObserveHandler(requests, unexpected *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			writeSCMBody(w, pr42Payload)
		case strings.HasSuffix(p, "/pulls/43"):
			writeSCMBody(w, pr43Payload)
		case strings.Contains(p, "/check-runs"):
			writeCheckRuns(w, r)
		case strings.HasSuffix(p, "/reviews"):
			writeReviews(w, r)
		case strings.HasSuffix(p, "/files"):
			writeChangedFiles(w, r)
		case strings.Contains(p, "/compare/"):
			writeSCMBody(w, comparePayload)
		default:
			unexpected.Add(1)
			http.NotFound(w, r)
		}
	})
}

// scmObserveBench holds one loopback server and a shared client. Each pass
// builds a fresh gateway so ETag and compare memos cannot turn a later pass
// into a cache hit. Counters are read before and after each pass so every row
// reports only the requests that pass served.
type scmObserveBench struct {
	client     *github.Client
	ctx        context.Context
	requests   *atomic.Int64
	unexpected *atomic.Int64
}

func newSCMObserveHarness(tb testing.TB) *scmObserveBench {
	tb.Helper()
	var requests, unexpected atomic.Int64
	srv := httptest.NewServer(scmObserveHandler(&requests, &unexpected))
	tb.Cleanup(srv.Close)
	client, err := github.NewClient(
		github.WithHTTPClient(srv.Client()),
		github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"),
	)
	if err != nil {
		tb.Fatal(err)
	}
	return &scmObserveBench{
		client:     client,
		ctx:        context.Background(),
		requests:   &requests,
		unexpected: &unexpected,
	}
}

func (h *scmObserveBench) gateway() *scm.GitHub {
	return scm.NewGitHub(h.client, "o", "r", "main")
}

// runSCMObserve completes ten observes through one fresh gateway. Work is
// completed observes plus the loopback requests they served; errors are
// Observe failures and requests to endpoints the fixture does not model.
func runSCMObserve(h *scmObserveBench) (int64, int64) {
	var work, errs int64
	beforeRequests := h.requests.Load()
	beforeUnexpected := h.unexpected.Load()
	gateway := h.gateway()
	for i := 0; i < scmObserves; i++ {
		pr := 42
		if i%2 == 1 {
			pr = 43
		}
		if _, err := gateway.Observe(h.ctx, pr); err != nil {
			errs++
		} else {
			work++
		}
	}
	work += h.requests.Load() - beforeRequests
	errs += h.unexpected.Load() - beforeUnexpected
	return work, errs
}

// TestSCMObserveExercisesPopulatedSignals proves the loopback fixture drives
// checks, reviews, comparison and the changed-files fallback for real.
func TestSCMObserveExercisesPopulatedSignals(t *testing.T) {
	h := newSCMObserveHarness(t)
	gateway := h.gateway()
	rich, err := gateway.Observe(h.ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(rich.Checks) != 2 || rich.Approvals != 1 || !rich.Behind ||
		rich.BaseSHA != "base" || rich.FailureExcerpt == "" {
		t.Fatalf("populated observation = %+v", rich)
	}
	fallback, err := gateway.Observe(h.ctx, 43)
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback.Checks) != 0 || fallback.Approvals != 1 || fallback.WorkflowSkipped {
		t.Fatalf("fallback observation = %+v", fallback)
	}
}

func TestSCMObserveCountsAreFixed(t *testing.T) {
	h := newSCMObserveHarness(t)
	w1, e1 := runSCMObserve(h)
	w2, e2 := runSCMObserve(h)
	if e1 != 0 || e2 != 0 {
		t.Fatalf("observe errs = %d and %d", e1, e2)
	}
	if w1 != w2 {
		t.Fatalf("observe work drifted: %d then %d", w1, w2)
	}
}
