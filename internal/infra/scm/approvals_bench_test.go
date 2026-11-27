package scm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// BenchmarkGitLabApprovals isolates the extra /approvals GET that Observe pays
// when the MR detail carries an empty approved_by. The MR detail GET is shared
// by both subcases, so the only difference between them is that extra call.
// The scm-observe row stays the end-to-end number; this row prices the redundant
// round alone.
//
// Work is the approvals returned plus every request the fixture served. Errs
// counts approval failures and an approvals count that differs from the fixed
// fixture, so a stale or broken path cannot pass as a fast pass. The named
// limiter is the MR detail GET plus the optional /approvals GET inside the
// SetLimit(4) signal round.
//
// CI command:
//
//	go test -run '^$' -bench '^BenchmarkGitLabApprovals$' -benchtime=100x -count=5 -benchmem ./internal/infra/scm/
const (
	nameGitLabApprovals    = "gitlab-approvals"
	limiterGitLabApprovals = "gitlab-approvals-limiter-detail-plus-optional-approvals-get-setlimit4"

	limiterGitLabApprovalsText = "MR detail GET + optional /approvals GET under SetLimit(4)"

	glApprovalsMR    = 42
	glApprovalsCount = 2
)

var (
	glApprovalsDetailEmpty = []byte(`{"iid":42,"state":"opened","sha":"head","approved_by":[]}`)
	glApprovalsDetailFull  = []byte(`{"iid":42,"state":"opened","sha":"head","approved_by":[{"id":1,"user":{"username":"alice"}},{"id":2,"user":{"username":"bob"}}]}`)
	glApprovalsBody        = []byte(`{"approved_by":[{"id":1,"user":{"username":"alice"}},{"id":2,"user":{"username":"bob"}}]}`)
)

// glApprovalsFixture serves one MR detail and the signals Observe fetches for
// it. The full flag switches the detail's approved_by between empty and a fixed
// two-approver list. Counters let each pass prove which calls it made.
type glApprovalsFixture struct {
	server   *httptest.Server
	gateway  *GitLab
	full     atomic.Bool
	detail   atomic.Int64
	approval atomic.Int64
	total    atomic.Int64
}

func newGitLabApprovalsFixture(tb testing.TB) *glApprovalsFixture {
	f := &glApprovalsFixture{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	tb.Cleanup(f.server.Close)
	f.gateway = NewGitLab(f.server.URL+"/api/v4/", "tok", "o", "r")
	return f
}

func (f *glApprovalsFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.total.Add(1)
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/merge_requests/42") && r.Method == http.MethodGet:
		f.detail.Add(1)
		if f.full.Load() {
			_, _ = w.Write(glApprovalsDetailFull)
		} else {
			_, _ = w.Write(glApprovalsDetailEmpty)
		}
	case strings.HasSuffix(p, "/merge_requests/42/approvals"):
		f.approval.Add(1)
		_, _ = w.Write(glApprovalsBody)
	case strings.HasSuffix(p, "/threads"):
		_, _ = w.Write([]byte(`[]`))
	case strings.HasSuffix(p, "/pipelines"):
		_, _ = w.Write([]byte(`[{"id":11,"sha":"head","status":"success"}]`))
	case strings.HasSuffix(p, "/pipelines/11/jobs"):
		_, _ = w.Write([]byte(`[]`))
	case strings.HasSuffix(p, "/repository/compare"):
		_, _ = w.Write([]byte(`{"behind_by":0,"base_commit":{"id":"base"}}`))
	default:
		http.NotFound(w, r)
	}
}

// glApprovalsPass is one Observe through the fixture. detail and approval are
// per-pass deltas, so they stay exact across a benchmark's repeated calls.
type glApprovalsPass struct {
	work     int64
	errs     int64
	detail   int64
	approval int64
}

func (f *glApprovalsFixture) observe(ctx context.Context) glApprovalsPass {
	detailBefore := f.detail.Load()
	approvalBefore := f.approval.Load()
	totalBefore := f.total.Load()

	o, err := f.gateway.Observe(ctx, glApprovalsMR)

	detail := f.detail.Load() - detailBefore
	approval := f.approval.Load() - approvalBefore
	total := f.total.Load() - totalBefore

	var errs int64
	if err != nil {
		errs++
	}
	if strings.Contains(o.IncompleteReason, "approvals:") {
		errs++
	}
	if o.Approvals != glApprovalsCount {
		errs++
	}
	return glApprovalsPass{
		work:     int64(o.Approvals) + total,
		errs:     errs,
		detail:   detail,
		approval: approval,
	}
}

// TestGitLabApprovalsFixtureCounts pins the two subcases the benchmark relies
// on. An empty approved_by must trigger exactly one detail GET and one
// /approvals GET; a populated detail must make only the detail GET. Both must
// return the same approvals count with no errors.
func TestGitLabApprovalsFixtureCounts(t *testing.T) {
	f := newGitLabApprovalsFixture(t)
	ctx := context.Background()

	f.full.Store(false)
	empty := f.observe(ctx)
	if empty.detail != 1 || empty.approval != 1 {
		t.Fatalf("empty approved_by: detail=%d approvals=%d, want 1 and 1", empty.detail, empty.approval)
	}
	if empty.errs != 0 || empty.work <= 0 {
		t.Fatalf("empty approved_by: errs=%d work=%d, want no errors and positive work", empty.errs, empty.work)
	}

	f.full.Store(true)
	present := f.observe(ctx)
	if present.detail != 1 || present.approval != 0 {
		t.Fatalf("present approved_by: detail=%d approvals=%d, want 1 and 0", present.detail, present.approval)
	}
	if present.errs != 0 || present.work <= 0 {
		t.Fatalf("present approved_by: errs=%d work=%d, want no errors and positive work", present.errs, present.work)
	}
}

func BenchmarkGitLabApprovals(b *testing.B) {
	f := newGitLabApprovalsFixture(b)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		full bool
	}{
		{name: "empty", full: false},
		{name: "present", full: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			f.full.Store(tc.full)
			var work, errs int64
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pass := f.observe(ctx)
				work += pass.work
				errs += pass.errs
			}
			b.StopTimer()

			fmt.Printf("%s/%s: work=%d errs=%d limiter=%s\n",
				nameGitLabApprovals, tc.name, work, errs, limiterGitLabApprovalsText)
			b.ReportMetric(float64(work), nameGitLabApprovals+"-work")
			b.ReportMetric(float64(errs), nameGitLabApprovals+"-errs")
			b.ReportMetric(1, limiterGitLabApprovals)
		})
	}
}
