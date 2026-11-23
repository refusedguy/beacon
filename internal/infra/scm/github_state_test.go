package scm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestGitHubObserveMapsManualMergeState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			_, _ = w.Write([]byte(`{"number":42,"state":"closed","merged":true,"mergeable_state":"dirty","head":{"sha":"abc"}}`))
		case strings.Contains(p, "/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case strings.HasSuffix(p, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(p, "/compare/"):
			_, _ = w.Write([]byte(`{"behind_by":0}`))
		default:
			t.Errorf("unexpected path %s", p)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGitHub(client, "o", "r", "main")
	obs, err := g.Observe(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PRState != "merged" {
		t.Fatalf("state = %q", obs.PRState)
	}
	if !obs.Conflict {
		t.Fatal("mergeable=false was not mapped to conflict")
	}
}
