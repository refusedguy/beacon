package scm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/pkg/core"
)

func ghObserveGitHub(t *testing.T, handler http.Handler) *GitHub {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatalf("github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+\"/\", srv.URL+\"/\")): %v", err)
	}
	return NewGitHub(client, "o", "r", "main")
}

// ghObserveHandler serves the fixed Observe endpoints and delegates the
// changed-files response to files.
func ghObserveHandler(t *testing.T, files http.HandlerFunc) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			files(w, r)
		case strings.Contains(p, "/compare/"):
			_, _ = w.Write([]byte(`{"behind_by":0,"base_commit":{"sha":"base"}}`))
		default:
			t.Errorf("unexpected path %s", p)
			http.NotFound(w, r)
		}
	})
}

func TestIgnoredPathsOnly(t *testing.T) {
	file := func(name, previous string) *github.CommitFile {
		return &github.CommitFile{Filename: github.String(name), PreviousFilename: github.String(previous)}
	}
	cases := []struct {
		name  string
		files []*github.CommitFile
		want  bool
	}{
		{"empty", nil, false},
		{"docs file", []*github.CommitFile{file("docs/tasks/T-1.md", "")}, true},
		{"nested markdown", []*github.CommitFile{file("notes/deep/README.MD", "")}, true},
		{"root markdown", []*github.CommitFile{file("README.md", "")}, true},
		{"code file", []*github.CommitFile{file("internal/app/a.go", "")}, false},
		{"mixed", []*github.CommitFile{file("docs/a.md", ""), file("main.go", "")}, false},
		{"rename from code", []*github.CommitFile{file("docs/a.md", "src/a.go")}, false},
		{"rename within docs", []*github.CommitFile{file("docs/b.md", "docs/a.md")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ignoredPathsOnly(tc.files); got != tc.want {
				t.Fatalf("ignoredPathsOnly() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestGitHubObserveMarksDocsOnlySkip(t *testing.T) {
	calls := 0
	g := ghObserveGitHub(t, ghObserveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`[{"filename":"docs/tasks/T-1.md"},{"filename":"README.MD"}]`))
	}))
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("g.Observe(context.Background(), 42): %v", err)
	}
	if !obs.WorkflowSkipped {
		t.Fatal("WorkflowSkipped = false, want true for a docs-only diff with no checks")
	}
	if calls != 1 {
		t.Fatalf("changed-files calls = %d, want 1", calls)
	}
}

func TestGitHubObserveDoesNotMarkCodeChangeAsSkipped(t *testing.T) {
	g := ghObserveGitHub(t, ghObserveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"filename":"docs/a.md"},{"filename":"internal/app/a.go"}]`))
	}))
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("g.Observe(context.Background(), 42): %v", err)
	}
	if obs.WorkflowSkipped {
		t.Fatal("WorkflowSkipped = true, want false when code changed")
	}
}

func TestGitHubObserveDoesNotMarkBlockedCleanPRAsConflict(t *testing.T) {
	base := ghObserveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"filename":"internal/app/a.go"}]`))
	})
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/pulls/42") {
			_, _ = w.Write([]byte(`{"number":42,"state":"open","mergeable":false,"mergeable_state":"clean","head":{"sha":"abc"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/check-runs") {
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"name":"ci","status":"completed","conclusion":"failure","output":{"title":"check failed","text":"details"}}]}`))
			return
		}
		base.ServeHTTP(w, r)
	}))
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("g.Observe(context.Background(), 42): %v", err)
	}
	if obs.Conflict {
		t.Fatal("Conflict = true for a clean PR blocked by checks")
	}
	if obs.FailureExcerpt == "" {
		t.Fatal("FailureExcerpt is empty, want the failed check output for the policy rule")
	}
}

func TestGitHubObserveChecksEveryChangedFilePage(t *testing.T) {
	calls := 0
	g := ghObserveGitHub(t, ghObserveHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `</repos/o/r/pulls/42/files?page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"filename":"docs/a.md"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"filename":"internal/app/a.go"}]`))
	}))
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("g.Observe(context.Background(), 42): %v", err)
	}
	if calls != 2 {
		t.Fatalf("changed-files calls = %d, want 2", calls)
	}
	if obs.WorkflowSkipped {
		t.Fatal("WorkflowSkipped = true, want false for a code file on the second page")
	}
}

func TestGitHubObserveChecksEveryCheckRunPage(t *testing.T) {
	base := ghObserveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("changed files fetched even though checks were returned")
	})
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/check-runs") {
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `</repos/o/r/commits/abc/check-runs?page=2>; rel="next"`)
				_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"name":"first","status":"completed","conclusion":"success"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"name":"second","status":"completed","conclusion":"failure","output":{"title":"failed on page two","text":""}}]}`))
			return
		}
		base.ServeHTTP(w, r)
	}))
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("g.Observe(context.Background(), 42): %v", err)
	}
	if len(obs.Checks) != 2 {
		t.Fatalf("checks = %+v, want two entries", obs.Checks)
	}
	if obs.FailureExcerpt != "failed on page two" {
		t.Fatalf("FailureExcerpt = %q, want the failure from page two", obs.FailureExcerpt)
	}
}

func TestGitHubObserveCountsApprovalsOnEveryReviewsPage(t *testing.T) {
	base := ghObserveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `</repos/o/r/pulls/42/reviews?page=2>; rel="next"`)
				_, _ = w.Write([]byte(`[{"user":{"login":"alice"},"state":"CHANGES_REQUESTED","submitted_at":"2024-01-01T00:00:00Z"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"user":{"login":"bob"},"state":"APPROVED","submitted_at":"2024-01-02T00:00:00Z"}]`))
			return
		}
		base.ServeHTTP(w, r)
	}))
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("g.Observe(context.Background(), 42): %v", err)
	}
	if obs.Approvals != 1 {
		t.Fatalf("Approvals = %d, want 1 from page two", obs.Approvals)
	}
}

func TestChecksStopsAtPageCap(t *testing.T) {
	calls := 0
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `</repos/o/r/commits/abc/check-runs?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
	}))
	if _, err := g.checks(context.Background(), "abc"); err != nil {
		t.Fatalf("g.checks(context.Background(), \"abc\"): %v", err)
	}
	if calls != maxGitHubPages {
		t.Fatalf("check-runs calls = %d, want %d", calls, maxGitHubPages)
	}
}

func TestGuardedReturnsTypedResults(t *testing.T) {
	br := newSCMBreaker("guarded-test")

	value, err := guarded(br, func() (string, error) { return "ok", nil })
	if err != nil || value != "ok" {
		t.Fatalf("guarded() = %q, %v; want ok, nil", value, err)
	}

	var nilPR *github.PullRequest
	pr, err := guarded(br, func() (*github.PullRequest, error) { return nilPR, nil })
	if err != nil || pr != nilPR {
		t.Fatalf("guarded() = %v, %v; want typed nil, nil", pr, err)
	}

	sentinel := errors.New("network down")
	count, err := guarded(br, func() (int, error) { return 0, sentinel })
	if count != 0 || !errors.Is(err, sentinel) {
		t.Fatalf("guarded() = %d, %v; want 0, sentinel", count, err)
	}
}

// roundGate counts the sequential rounds an Observe makes. The first expected
// calls wait on a gate before answering, which proves they ran together and
// keeps them distinct from calls in a later round.
type roundGate struct {
	mu       sync.Mutex
	arrived  int
	inFlight int
	peak     int
	gate     chan struct{}
	once     sync.Once
	expected int
	waves    map[int]bool
}

func newRoundGate(expected int) *roundGate {
	return &roundGate{gate: make(chan struct{}), expected: expected, waves: map[int]bool{}}
}

func (g *roundGate) first() {
	g.mu.Lock()
	g.arrived++
	n := g.arrived
	g.inFlight++
	if g.inFlight > g.peak {
		g.peak = g.inFlight
	}
	g.waves[1] = true
	g.mu.Unlock()
	if n == g.expected {
		g.once.Do(func() { close(g.gate) })
	}
	select {
	case <-g.gate:
	case <-time.After(3 * time.Second):
	}
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
}

func (g *roundGate) later() {
	g.mu.Lock()
	g.waves[2] = true
	g.mu.Unlock()
	select {
	case <-g.gate:
	case <-time.After(3 * time.Second):
	}
}

func (g *roundGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.waves)
}

func (g *roundGate) peakConcurrency() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

func TestGitHubObserveFetchesIndependentSignalsInTwoRounds(t *testing.T) {
	// checks, reviews and the base comparison have no data dependency; changed
	// files wait for the check result.
	gate := newRoundGate(3)
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		first := strings.Contains(p, "/check-runs") || strings.HasSuffix(p, "/reviews") || strings.Contains(p, "/compare/")
		if first {
			gate.first()
		} else if strings.HasSuffix(p, "/files") {
			gate.later()
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			_, _ = w.Write([]byte(`{"number":42,"state":"open","head":{"sha":"abc"}}`))
		case strings.Contains(p, "/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case strings.HasSuffix(p, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(p, "/files"):
			_, _ = w.Write([]byte(`[{"filename":"docs/a.md"}]`))
		case strings.Contains(p, "/compare/"):
			_, _ = w.Write([]byte(`{"behind_by":0,"base_commit":{"sha":"base"}}`))
		default:
			t.Errorf("unexpected path %s", p)
			http.NotFound(w, r)
		}
	}))
	o, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got := gate.count(); got != 2 {
		t.Fatalf("rounds = %d, want 2 (checks and reviews and compare, then files)", got)
	}
	if got := gate.peakConcurrency(); got != 3 {
		t.Fatalf("peak concurrent first-round calls = %d, want 3", got)
	}
	if o.PRNumber != 42 || o.HeadSHA != "abc" || o.PRState != "open" || o.Approvals != 0 ||
		!o.WorkflowSkipped || o.Behind || o.BaseSHA != "base" || o.Incomplete || o.IncompleteReason != "" {
		t.Fatalf("observation changed: %+v", o)
	}
}

func TestGitHubChecksPaginatesEveryPage(t *testing.T) {
	const total = 250
	pages := 0
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/check-runs") {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		pages++
		page := r.URL.Query().Get("page")
		start := 0
		switch page {
		case "":
			start = 0
			w.Header().Set("Link", `</repos/o/r/commits/abc/check-runs?page=2>; rel="next"`)
		case "2":
			start = 100
			w.Header().Set("Link", `</repos/o/r/commits/abc/check-runs?page=3>; rel="next"`)
		case "3":
			start = 200
		default:
			t.Errorf("unexpected page %q", page)
			return
		}
		runs := make([]map[string]any, 0, 100)
		for i := start; i < start+100 && i < total; i++ {
			runs = append(runs, map[string]any{
				"name":       "check-" + strconv.Itoa(i),
				"status":     "completed",
				"conclusion": "success",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"total_count": total, "check_runs": runs}); err != nil {
			t.Error(err)
		}
	}))
	checks, err := g.checks(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != total {
		t.Fatalf("checks = %d, want %d", len(checks), total)
	}
	if pages != 3 {
		t.Fatalf("check-run pages = %d, want 3", pages)
	}
}

func TestConcurrentObservesShareOnePullRequestGet(t *testing.T) {
	const observers = 8
	var prGets atomic.Int64
	arrived := make(chan struct{}, observers)
	release := make(chan struct{})
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			prGets.Add(1)
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-release
			_, _ = w.Write([]byte(`{"number":42,"state":"open","head":{"sha":"abc"}}`))
		case strings.Contains(p, "/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case strings.HasSuffix(p, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(p, "/files"):
			_, _ = w.Write([]byte(`[{"filename":"docs/a.md"}]`))
		case strings.Contains(p, "/compare/"):
			_, _ = w.Write([]byte(`{"behind_by":0,"base_commit":{"sha":"base"}}`))
		default:
			t.Errorf("unexpected path %s", p)
			http.NotFound(w, r)
		}
	}))

	var wg sync.WaitGroup
	for i := 0; i < observers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Observe(context.Background(), 42); err != nil {
				t.Error(err)
			}
		}()
	}
	<-arrived
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := prGets.Load(); got != 1 {
		t.Fatalf("pull request GETs = %d, want 1 for concurrent observes", got)
	}
}

func TestUpdateBranchUsesConditionalGet(t *testing.T) {
	const oldSHA, newSHA = "old", "new"
	var gets, conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/42"):
			gets++
			if r.Header.Get("If-None-Match") == `"v1"` {
				conditional++
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`{"number":42,"node_id":"PR_kwDOABC123","head":{"sha":"old"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/graphql"):
			_, _ = w.Write([]byte(`{"data":{"updatePullRequestBranch":{"pullRequest":{"headRefOid":"new"}}}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGitHub(client, "o", "r", "main")
	for i := 0; i < 2; i++ {
		sha, err := g.UpdateBranch(context.Background(), 42, oldSHA)
		if err != nil {
			t.Fatal(err)
		}
		if sha != newSHA {
			t.Fatalf("sha = %q, want %q", sha, newSHA)
		}
	}
	if gets != 2 || conditional != 1 {
		t.Fatalf("pull request GETs = %d, conditional = %d; want 2 and 1", gets, conditional)
	}
}

func TestGitHubChecksConditionalReadFollowsUpdates(t *testing.T) {
	const v1, v2 = `"checks-v1"`, `"checks-v2"`
	revision := 0
	var calls, conditional int
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/check-runs") {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls++
		etag, conclusion := v1, "success"
		if revision == 1 {
			etag, conclusion = v2, "failure"
		}
		if got := r.Header.Get("If-None-Match"); got == etag {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"name":"ci","status":"completed","conclusion":%q}]}`, conclusion)
	}))

	first, err := g.checks(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].State != core.CheckSuccess {
		t.Fatalf("first checks = %+v, want one success", first)
	}
	second, err := g.checks(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].State != core.CheckSuccess {
		t.Fatalf("cached checks = %+v, want one success", second)
	}
	if calls != 2 || conditional != 1 {
		t.Fatalf("check-runs calls = %d, conditional = %d; want 2 and 1", calls, conditional)
	}

	revision = 1
	updated, err := g.checks(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].State != core.CheckFailure {
		t.Fatalf("updated checks = %+v, want one failure", updated)
	}
	if calls != 3 {
		t.Fatalf("check-runs calls = %d, want 3 after a new check run", calls)
	}
}

func TestGitHubApprovalsConditionalReadFollowsUpdates(t *testing.T) {
	const v1, v2 = `"reviews-v1"`, `"reviews-v2"`
	revision := 0
	var calls, conditional int
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/reviews") {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls++
		etag := v1
		body := `[]`
		if revision == 1 {
			etag = v2
			body = `[{"id":1,"user":{"login":"alice"},"state":"APPROVED","submitted_at":"2024-01-01T00:00:00Z"}]`
		}
		if got := r.Header.Get("If-None-Match"); got == etag {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(body))
	}))

	first, err := g.approvals(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if first != 0 {
		t.Fatalf("approvals = %d, want 0", first)
	}
	second, err := g.approvals(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Fatalf("cached approvals = %d, want 0", second)
	}
	if calls != 2 || conditional != 1 {
		t.Fatalf("reviews calls = %d, conditional = %d; want 2 and 1", calls, conditional)
	}

	revision = 1
	updated, err := g.approvals(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 1 {
		t.Fatalf("approvals = %d, want 1 after a new review", updated)
	}
	if calls != 3 {
		t.Fatalf("reviews calls = %d, want 3 after a new review", calls)
	}
}

func TestGitHubObserveMemoizesCompare(t *testing.T) {
	var compares int
	base := ghObserveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"filename":"docs/a.md"}]`))
	})
	g := ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/compare/") {
			compares++
		}
		base.ServeHTTP(w, r)
	}))

	for i := 0; i < 2; i++ {
		if _, err := g.Observe(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if compares != 1 {
		t.Fatalf("compare calls = %d, want 1 for two ticks with the same SHAs", compares)
	}
}
