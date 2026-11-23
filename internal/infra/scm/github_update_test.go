package scm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

// A branch ref with a slash must be escaped in the update-branch URL. GitHub
// treats an unescaped slash as a path separator and answers 404.
func TestGitHubUpdateBranchEscapesSlashedBranch(t *testing.T) {
	const branch = "beacon/E2E-01"
	putPath := ""
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/42"):
			gets++
			sha := "old"
			if gets > 1 {
				sha = "new"
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"number":42,"head":{"ref":%q,"sha":%q}}`, branch, sha)))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/branches/"):
			putPath = r.URL.EscapedPath()
			w.WriteHeader(http.StatusNoContent)
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

	sha, err := g.UpdateBranch(context.Background(), 42, "old")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "new" {
		t.Fatalf("sha = %q, want new", sha)
	}
	if !strings.Contains(putPath, "beacon%2FE2E-01/update") {
		t.Fatalf("PUT path = %q, want escaped branch", putPath)
	}
}
