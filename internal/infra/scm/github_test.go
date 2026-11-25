package scm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
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
