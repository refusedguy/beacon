package scm

import (
	"context"
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
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if obs.WorkflowSkipped {
		t.Fatal("WorkflowSkipped = true, want false when code changed")
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
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("changed-files calls = %d, want 2", calls)
	}
	if obs.WorkflowSkipped {
		t.Fatal("WorkflowSkipped = true, want false for a code file on the second page")
	}
}
