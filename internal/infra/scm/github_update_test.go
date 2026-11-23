package scm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

type graphqlRequest struct {
	Query     string            `json:"query"`
	Variables map[string]string `json:"variables"`
}

func TestGitHubUpdateBranchGraphQL(t *testing.T) {
	const nodeID = "PR_kwDOABC123"
	const oldSHA = "old"
	const newSHA = "new"

	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/42"):
			_, _ = w.Write([]byte(`{"number":42,"node_id":"PR_kwDOABC123","head":{"ref":"beacon/E2E-01","sha":"old"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/graphql"):
			posts++
			var req graphqlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode graphql request: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if !strings.Contains(req.Query, "updatePullRequestBranch") {
				t.Errorf("query %q does not name updatePullRequestBranch", req.Query)
			}
			if !strings.Contains(req.Query, "expectedHeadOid") {
				t.Errorf("query %q does not pass expectedHeadOid", req.Query)
			}
			if got := req.Variables["id"]; got != nodeID {
				t.Errorf("variable id = %q, want %q", got, nodeID)
			}
			if got := req.Variables["sha"]; got != oldSHA {
				t.Errorf("variable sha = %q, want %q", got, oldSHA)
			}
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

	sha, err := g.UpdateBranch(context.Background(), 42, oldSHA)
	if err != nil {
		t.Fatal(err)
	}
	if sha != newSHA {
		t.Fatalf("sha = %q, want %q", sha, newSHA)
	}
	if posts != 1 {
		t.Fatalf("graphql posts = %d, want 1", posts)
	}
}

func TestGitHubUpdateBranchGraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/42"):
			_, _ = w.Write([]byte(`{"number":42,"node_id":"PR_kwDOABC123","head":{"sha":"old"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/graphql"):
			_, _ = w.Write([]byte(`{"errors":[{"message":"pull request is not mergeable"}]}`))
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

	_, err = g.UpdateBranch(context.Background(), 42, "old")
	if err == nil {
		t.Fatal("want error for graphql errors")
	}
	if !strings.Contains(err.Error(), "not mergeable") {
		t.Fatalf("error = %q, want graphql message", err)
	}
}
