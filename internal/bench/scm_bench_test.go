package bench

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/internal/infra/scm"
)

// scmObserves bounds the smoke work. Ten observes drive the full PR, check,
// approval, compare and changed-file round trip set without a slow fixture.
const scmObserves = 10

// stubRoundTripper serves fixed bodies in process so no benchmark pass pays
// socket setup or scheduler jitter. Both HTTP-backed harnesses share it.
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

// scmObserveBench holds one GitHub gateway on an in-process transport. The
// counters are read before and after every observe so each row reports the
// requests it served, not a cumulative total.
type scmObserveBench struct {
	gateway    *scm.GitHub
	ctx        context.Context
	requests   *atomic.Int64
	unexpected *atomic.Int64
}

// newSCMObserveHarness mirrors the client setup in
// internal/infra/scm/github_test.go:19. The transport serves the same five
// endpoints as ghObserveHandler, with a code file so no observe is skipped.
func newSCMObserveHarness(b *testing.B) *scmObserveBench {
	b.Helper()
	var requests, unexpected atomic.Int64
	transport := stubRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			return stubJSON(req, http.StatusOK, `{"number":42,"state":"open","head":{"sha":"abc"}}`), nil
		case strings.Contains(p, "/check-runs"):
			return stubJSON(req, http.StatusOK, `{"total_count":0,"check_runs":[]}`), nil
		case strings.HasSuffix(p, "/reviews"):
			return stubJSON(req, http.StatusOK, `[]`), nil
		case strings.HasSuffix(p, "/pulls/42/files"):
			return stubJSON(req, http.StatusOK, `[{"filename":"internal/app/a.go"}]`), nil
		case strings.Contains(p, "/compare/"):
			return stubJSON(req, http.StatusOK, `{"behind_by":0,"base_commit":{"sha":"base"}}`), nil
		default:
			unexpected.Add(1)
			return stubJSON(req, http.StatusNotFound, `{"message":"not found"}`), nil
		}
	})
	client, err := github.NewClient(
		github.WithHTTPClient(&http.Client{Transport: transport}),
		github.WithEnterpriseURLs("https://bench.invalid/", "https://bench.invalid/"),
	)
	if err != nil {
		b.Fatal(err)
	}
	return &scmObserveBench{
		gateway:    scm.NewGitHub(client, "o", "r", "main"),
		ctx:        context.Background(),
		requests:   &requests,
		unexpected: &unexpected,
	}
}

// runSCMObserve completes ten observes. Work is completed observes plus the
// loopback requests they served; errors are Observe failures and requests to
// endpoints the fixture does not model.
func runSCMObserve(h *scmObserveBench) (int64, int64) {
	var work, errs int64
	beforeRequests := h.requests.Load()
	beforeUnexpected := h.unexpected.Load()
	for i := 0; i < scmObserves; i++ {
		if _, err := h.gateway.Observe(h.ctx, 42); err != nil {
			errs++
		} else {
			work++
		}
	}
	work += h.requests.Load() - beforeRequests
	errs += h.unexpected.Load() - beforeUnexpected
	return work, errs
}
