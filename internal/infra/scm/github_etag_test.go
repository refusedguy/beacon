package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestGetPRUsesETagCache(t *testing.T) {
	requests := 0
	conditional := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional++
			rw.WriteHeader(http.StatusNotModified)
			return
		}
		rw.Header().Set("ETag", `"v1"`)
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"number":42,"head":{"sha":"abc"}}`))
	}))
	defer srv.Close()
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGitHub(client, "o", "r", "main")
	for i := 0; i < 3; i++ {
		pr, err := g.getPR(context.Background(), 42)
		if err != nil {
			t.Fatal(err)
		}
		if pr.GetHead().GetSHA() != "abc" {
			t.Fatalf("cached PR lost head SHA: %+v", pr)
		}
	}
	if requests != 3 || conditional != 2 {
		t.Fatalf("requests=%d conditional=%d", requests, conditional)
	}
}

// TestUpdateBranchInvalidatesPRCache reproduces the stale headRefOid from the
// GraphQL mutation. An update followed by Observe must report the real head,
// and a second update must succeed with that refreshed expectedHeadOid.
func TestUpdateBranchInvalidatesPRCache(t *testing.T) {
	state := struct {
		head string
		etag string
	}{head: "old", etag: `"v1"`}

	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/graphql"):
			posts++
			var req graphqlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode graphql request: %v", err)
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			if got := req.Variables["sha"]; got != state.head {
				t.Errorf("expectedHeadOid = %q, want current head %q", got, state.head)
			}
			// The mutation reports the head before the update; only the
			// following GET reveals the real post-update head.
			pre := state.head
			state.head = "new"
			state.etag = `"v2"`
			_, _ = fmt.Fprintf(rw, `{"data":{"updatePullRequestBranch":{"pullRequest":{"headRefOid":%q}}}}`, pre)
		case strings.HasSuffix(p, "/pulls/42"):
			if r.Header.Get("If-None-Match") == state.etag {
				rw.WriteHeader(http.StatusNotModified)
				return
			}
			rw.Header().Set("ETag", state.etag)
			_, _ = fmt.Fprintf(rw, `{"number":42,"node_id":"PR_1","state":"open","head":{"sha":%q}}`, state.head)
		case strings.Contains(p, "/check-runs"):
			_, _ = rw.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case strings.HasSuffix(p, "/reviews"):
			_, _ = rw.Write([]byte(`[]`))
		case strings.HasSuffix(p, "/pulls/42/files"):
			_, _ = rw.Write([]byte(`[]`))
		case strings.Contains(p, "/compare/"):
			_, _ = rw.Write([]byte(`{"behind_by":0,"base_commit":{"sha":"base"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGitHub(client, "o", "r", "main")
	ctx := context.Background()

	// Prime the cache with the worker head.
	obs, err := g.Observe(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if obs.HeadSHA != "old" {
		t.Fatalf("head before update = %q, want old", obs.HeadSHA)
	}

	sha, err := g.UpdateBranch(ctx, 42, "old")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "new" {
		t.Fatalf("UpdateBranch sha = %q, want post-update head new", sha)
	}

	// The next Observe must not get a 304 with the stale validator.
	obs, err = g.Observe(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if obs.HeadSHA != "new" {
		t.Fatalf("head after update = %q, want new", obs.HeadSHA)
	}

	// A second update with the refreshed expectedHeadOid must succeed.
	if _, err := g.UpdateBranch(ctx, 42, obs.HeadSHA); err != nil {
		t.Fatalf("second UpdateBranch: %v", err)
	}
	if posts != 2 {
		t.Fatalf("graphql posts = %d, want 2", posts)
	}
}
