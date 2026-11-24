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

func TestGitHubObserveMapsCheckTimingsWithoutExtraCalls(t *testing.T) {
	checkCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			_, _ = w.Write([]byte(`{"number":42,"state":"open","head":{"sha":"abc"}}`))
		case strings.Contains(p, "/check-runs"):
			checkCalls++
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"name":"ci","status":"completed","conclusion":"success","started_at":"2026-11-24T08:00:00Z","completed_at":"2026-11-24T08:00:28Z"}]}`))
		case strings.HasSuffix(p, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(p, "/compare/"):
			_, _ = w.Write([]byte(`{"behind_by":0,"base_commit":{"sha":"base"}}`))
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
	if checkCalls != 1 {
		t.Fatalf("check-runs calls = %d, want 1", checkCalls)
	}
	if len(obs.Checks) != 1 {
		t.Fatalf("checks = %+v", obs.Checks)
	}
	if obs.Checks[0].StartedAt.IsZero() || obs.Checks[0].CompletedAt.IsZero() {
		t.Fatalf("timings not mapped: %+v", obs.Checks[0])
	}
	if got := obs.Checks[0].DurationSeconds(); got != 28 {
		t.Fatalf("duration = %d, want 28", got)
	}
}
