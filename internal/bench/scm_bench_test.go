package bench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/internal/infra/scm"
)

// scmObserves bounds the smoke work. Ten observes drive the full PR, check,
// approval, compare and changed-file round trip set without a slow fixture.
const scmObserves = 10

// scmObserveBench holds one GitHub gateway pointed at a loopback server. The
// counters are read before and after every observe so each row reports the
// requests it served, not a cumulative total.
type scmObserveBench struct {
	gateway    *scm.GitHub
	ctx        context.Context
	requests   *atomic.Int64
	unexpected *atomic.Int64
}

// newSCMObserveHarness mirrors the client setup in
// internal/infra/scm/github_test.go:19. The handler serves the same five
// endpoints as ghObserveHandler, with a code file so no observe is skipped.
func newSCMObserveHarness(b *testing.B) *scmObserveBench {
	b.Helper()
	var requests, unexpected atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			_, _ = w.Write([]byte(`{"number":42,"state":"open","head":{"sha":"abc"}}`))
		case strings.Contains(p, "/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case strings.HasSuffix(p, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(p, "/pulls/42/files"):
			_, _ = w.Write([]byte(`[{"filename":"internal/app/a.go"}]`))
		case strings.Contains(p, "/compare/"):
			_, _ = w.Write([]byte(`{"behind_by":0,"base_commit":{"sha":"base"}}`))
		default:
			unexpected.Add(1)
			http.NotFound(w, r)
		}
	}))
	b.Cleanup(srv.Close)
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
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
