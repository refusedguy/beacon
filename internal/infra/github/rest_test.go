package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
)

func newRESTTestCounter() *Counter {
	return &Counter{now: time.Now}
}

func newRESTTestClient(t *testing.T, srv *httptest.Server, counter *Counter, token string) *RestClient {
	t.Helper()
	base := srv.URL + "/"
	client, err := gh.NewClient(
		gh.WithAuthToken(token),
		gh.WithURLs(&base, nil),
	)
	if err != nil {
		t.Fatalf("gh.NewClient: %v", err)
	}
	return newRestClient(client, counter)
}

func TestConditionalGetStoresETagAndHandles304(t *testing.T) {
	const etag = `"pr-42-v1"`
	const path = "repos/octo/demo/pulls/42"

	var ifNoneMatch []string
	var methods []string
	var auth []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		methods = append(methods, r.Method)
		auth = append(auth, r.Header.Get("Authorization"))
		ifNoneMatch = append(ifNoneMatch, r.Header.Get("If-None-Match"))
		if r.URL.Path != "/"+path {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":42,"mergeable":null,"mergeable_state":"unknown"}`)
	}))
	defer srv.Close()

	counter := newRESTTestCounter()
	rc := newRESTTestClient(t, srv, counter, "test-token")

	var first gh.PullRequest
	notModified, err := rc.GetConditional(context.Background(), path, &first)
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	if notModified {
		t.Fatal("first GET reported 304")
	}
	if first.GetNumber() != 42 {
		t.Fatalf("decoded PR number = %d, want 42", first.GetNumber())
	}
	if got := counter.Used(); got != 1 {
		t.Fatalf("after 200 Used = %d, want 1", got)
	}

	var second gh.PullRequest
	notModified, err = rc.GetConditional(context.Background(), path, &second)
	if err != nil {
		t.Fatalf("conditional GET: %v", err)
	}
	if !notModified {
		t.Fatal("second GET did not report 304")
	}
	if second.GetNumber() != 0 {
		t.Fatalf("304 wrote into out: number = %d", second.GetNumber())
	}
	if got := counter.Used(); got != 1 {
		t.Fatalf("304 charged a point: Used = %d, want 1", got)
	}

	if calls != 2 {
		t.Fatalf("GET calls = %d, want 2", calls)
	}
	if len(ifNoneMatch) != 2 || ifNoneMatch[0] != "" || ifNoneMatch[1] != etag {
		t.Fatalf("If-None-Match = %q, want [\"\", %q]", ifNoneMatch, etag)
	}
	for i, method := range methods {
		if method != http.MethodGet {
			t.Errorf("call %d method = %s, want GET", i, method)
		}
		if auth[i] == "" {
			t.Errorf("call %d had no Authorization header", i)
		}
	}
}

func TestPingMergeableCallsPullEndpoint(t *testing.T) {
	const wantPath = "/repos/octo/demo/pulls/7"
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != wantPath {
			t.Errorf("ping path = %q, want %q", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"pr-7-unknown"`)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":7,"mergeable":null,"mergeable_state":"unknown"}`)
	}))
	defer srv.Close()

	counter := newRESTTestCounter()
	rc := newRESTTestClient(t, srv, counter, "test-token")

	state, notModified, err := rc.PingMergeable(context.Background(), "octo", "demo", 7)
	if err != nil {
		t.Fatalf("PingMergeable: %v", err)
	}
	if notModified {
		t.Fatal("first ping reported 304")
	}
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("ping paths = %q, want [%q]", paths, wantPath)
	}
	if !state.Unknown() {
		t.Fatalf("state = %+v, want UNKNOWN", state)
	}
	if got := counter.Used(); got != 1 {
		t.Fatalf("ping cost: Used = %d, want 1", got)
	}
}

func TestPingMergeableReadsComputedState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":8,"mergeable":true,"mergeable_state":"clean"}`)
	}))
	defer srv.Close()

	rc := newRESTTestClient(t, srv, newRESTTestCounter(), "test-token")
	state, _, err := rc.PingMergeable(context.Background(), "octo", "demo", 8)
	if err != nil {
		t.Fatalf("PingMergeable: %v", err)
	}
	if state.Mergeable == nil || !*state.Mergeable {
		t.Fatalf("Mergeable = %v, want true", state.Mergeable)
	}
	if state.MergeableState != "clean" {
		t.Fatalf("MergeableState = %q, want clean", state.MergeableState)
	}
	if state.Unknown() {
		t.Fatal("computed state still reported Unknown")
	}
}

func TestPingMergeableForceIgnoresCachedETag(t *testing.T) {
	const etag = `"pr-11-v1"`
	var ifNoneMatch []string
	uncounted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ifNoneMatch = append(ifNoneMatch, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		uncounted++
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		if uncounted == 1 {
			fmt.Fprint(w, `{"number":11,"mergeable":null,"mergeable_state":"unknown"}`)
			return
		}
		fmt.Fprint(w, `{"number":11,"mergeable":true,"mergeable_state":"clean"}`)
	}))
	defer srv.Close()

	rc := newRESTTestClient(t, srv, newRESTTestCounter(), "test-token")
	seeded, notModified, err := rc.PingMergeable(context.Background(), "octo", "demo", 11)
	if err != nil || notModified || !seeded.Unknown() {
		t.Fatalf("seed ping = (%+v,%v,%v), want UNKNOWN/200", seeded, notModified, err)
	}

	// A conditional ping now returns 304 with no usable value.
	_, notModified, err = rc.PingMergeable(context.Background(), "octo", "demo", 11)
	if err != nil || !notModified {
		t.Fatalf("conditional ping = (%v,%v), want 304", notModified, err)
	}

	forced, err := rc.PingMergeableForce(context.Background(), "octo", "demo", 11)
	if err != nil {
		t.Fatalf("PingMergeableForce: %v", err)
	}
	if forced.Mergeable == nil || !*forced.Mergeable {
		t.Fatalf("forced Mergeable = %v, want true", forced.Mergeable)
	}
	if len(ifNoneMatch) != 3 {
		t.Fatalf("GET calls = %d, want 3", len(ifNoneMatch))
	}
	if ifNoneMatch[0] != "" || ifNoneMatch[1] != etag || ifNoneMatch[2] != "" {
		t.Fatalf("If-None-Match = %q, want [\"\", %q, \"\"]", ifNoneMatch, etag)
	}
}

func TestResolveTokenChain(t *testing.T) {
	t.Setenv("BEACON_GITHUB_TOKEN", "primary-token")
	t.Setenv("GITHUB_TOKEN", "secondary-token")

	token, err := ResolveToken(context.Background())
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if token != "primary-token" {
		t.Fatalf("token = %q, want primary-token", token)
	}

	t.Setenv("BEACON_GITHUB_TOKEN", "   ")
	token, err = ResolveToken(context.Background())
	if err != nil {
		t.Fatalf("ResolveToken with empty primary: %v", err)
	}
	if token != "secondary-token" {
		t.Fatalf("token = %q, want secondary-token", token)
	}
}

func TestResolveTokenGhFallbackAndErrorSanitization(t *testing.T) {
	const ghToken = "gh-issued-token"
	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	t.Setenv("PATH", dir)
	t.Setenv("BEACON_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	writeGh := func(script string) {
		t.Helper()
		if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake gh: %v", err)
		}
	}

	writeGh("#!/bin/sh\nprintf '%s\\n' \"" + ghToken + "\"\n")
	token, err := ResolveToken(context.Background())
	if err != nil {
		t.Fatalf("ResolveToken via gh: %v", err)
	}
	if token != ghToken {
		t.Fatalf("token = %q, want %q", token, ghToken)
	}

	// A failing gh must not put its output into the returned error.
	writeGh("#!/bin/sh\nprintf '%s\\n' \"" + ghToken + "\" >&2\nexit 1\n")
	_, err = ResolveToken(context.Background())
	if err == nil {
		t.Fatal("ResolveToken succeeded when gh failed")
	}
	if strings.Contains(err.Error(), ghToken) {
		t.Fatalf("token leaked in error: %v", err)
	}
}

func TestTokenOnlyInAuthorizationHeader(t *testing.T) {
	const token = "sentinel-read-token"
	t.Setenv("BEACON_GITHUB_TOKEN", token)

	resolved, err := ResolveToken(context.Background())
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if resolved != token {
		t.Fatalf("resolved token = %q, want %q", resolved, token)
	}

	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		for _, place := range []string{r.URL.String(), string(body)} {
			if strings.Contains(place, token) {
				t.Errorf("token leaked into request at %q", place)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"server exploded"}`)
	}))
	defer srv.Close()

	counter := newRESTTestCounter()
	rc := newRESTTestClient(t, srv, counter, resolved)
	_, _, err = rc.PingMergeable(context.Background(), "octo", "demo", 9)
	if err == nil {
		t.Fatal("expected an error from the 500 response")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked in error: %v", err)
	}
	if len(auth) != 1 || auth[0] != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", auth, "Bearer "+token)
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	for key, value := range rc.etags {
		if strings.Contains(key, token) || strings.Contains(value, token) {
			t.Fatalf("token leaked into ETag cache: %q -> %q", key, value)
		}
	}
}
