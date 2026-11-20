package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRebasePullRequestReturnsNewHead drives BatchProvider.Rebase, the port the
// composition root binds to cibridge.WithAutoRebase. The mock server answers the
// update-branch PUT with 202 and a follow-up GET with a new head SHA, which is
// how GitHub behaves for a scheduled branch update.
func TestRebasePullRequestReturnsNewHead(t *testing.T) {
	const putPath = "/repos/octo/demo/pulls/42/update-branch"
	const getPath = "/repos/octo/demo/pulls/42"

	var methods, paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case putPath:
			if r.Method != http.MethodPut {
				t.Errorf("update method = %s, want PUT", r.Method)
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"message":"Update started"}`)
		case getPath:
			if r.Method != http.MethodGet {
				t.Errorf("reread method = %s, want GET", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"number":42,"head":{"sha":"newheadsha"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	counter := newRESTTestCounter()
	provider := NewBatchProvider(nil, newRESTTestClient(t, srv, counter, "test-token"), counter)

	sha, err := provider.Rebase(context.Background(), "octo/demo", 42)
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if sha != "newheadsha" {
		t.Fatalf("new head SHA = %q, want %q", sha, "newheadsha")
	}

	if len(methods) != 2 || methods[0] != http.MethodPut || methods[1] != http.MethodGet {
		t.Fatalf("methods = %q, want [PUT GET]", methods)
	}
	if len(paths) != 2 || paths[0] != putPath || paths[1] != getPath {
		t.Fatalf("paths = %q, want [%q %q]", paths, putPath, getPath)
	}
	if got := counter.Used(); got != 2 {
		t.Fatalf("points used = %d, want 2", got)
	}
}

// TestRebasePullRequestPutFailure does not reread the PR when the update-branch
// call fails with a real error. The error reaches the caller so the Bridge can
// fall back to its legacy rebase wake.
func TestRebasePullRequestPutFailure(t *testing.T) {
	const putPath = "/repos/octo/demo/pulls/42/update-branch"

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != putPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	counter := newRESTTestCounter()
	provider := NewBatchProvider(nil, newRESTTestClient(t, srv, counter, "test-token"), counter)

	sha, err := provider.Rebase(context.Background(), "octo/demo", 42)
	if err == nil {
		t.Fatalf("Rebase succeeded with SHA %q, want error", sha)
	}
	if sha != "" {
		t.Fatalf("SHA on error = %q, want empty", sha)
	}
	if len(paths) != 1 || paths[0] != putPath {
		t.Fatalf("paths = %q, want only [%q]", paths, putPath)
	}
}

// TestRebasePullRequestMissingHead reports a successful update whose follow-up
// read has no head SHA. Publishing an empty SHA to the Bridge would skip the
// retry merge and leave the Watch on the rebase verdict.
func TestRebasePullRequestMissingHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":42,"head":null}`)
	}))
	defer srv.Close()

	counter := newRESTTestCounter()
	provider := NewBatchProvider(nil, newRESTTestClient(t, srv, counter, "test-token"), counter)

	sha, err := provider.Rebase(context.Background(), "octo/demo", 42)
	if err == nil {
		t.Fatalf("Rebase succeeded with SHA %q, want error", sha)
	}
	if sha != "" {
		t.Fatalf("SHA on error = %q, want empty", sha)
	}
}

// TestRebaseInvalidRepo never reaches the network for a malformed repo, so a
// bad Watch cannot spend a point or rebase an unintended branch.
func TestRebaseInvalidRepo(t *testing.T) {
	counter := newRESTTestCounter()
	provider := NewBatchProvider(nil, nil, counter)

	sha, err := provider.Rebase(context.Background(), "demo", 42)
	if err == nil {
		t.Fatalf("Rebase succeeded with SHA %q, want error", sha)
	}
	if sha != "" {
		t.Fatalf("SHA on error = %q, want empty", sha)
	}
	if got := counter.Used(); got != 0 {
		t.Fatalf("points used = %d, want 0", got)
	}
}
