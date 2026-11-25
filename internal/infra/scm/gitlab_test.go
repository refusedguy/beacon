package scm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func glTestServer(t *testing.T, handler http.Handler) *GitLab {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewGitLab(srv.URL+"/api/v4/", "tok", "o", "r")
}

func glWriteJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestGitLabObserveUnresolvedThreads(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/merge_requests/7") && r.Method == http.MethodGet:
			if !strings.Contains(r.URL.EscapedPath(), "o%2Fr") {
				t.Errorf("project id not escaped: %s", r.URL.EscapedPath())
			}
			glWriteJSON(t, w, map[string]any{
				"iid": 7, "state": "opened", "sha": "head1", "has_conflicts": false,
				"blocking_discussions_resolved": false, "target_branch": "main", "labels": []string{"ready"},
				"diverged_commits_count": 2,
				"approved_by":            []map[string]any{{"user": map[string]string{"username": "fallback"}}},
			})
		case strings.HasSuffix(p, "/merge_requests/7/threads"):
			if r.Method != http.MethodGet {
				t.Errorf("threads method = %s", r.Method)
			}
			glWriteJSON(t, w, []map[string]any{{
				"notes": []map[string]any{
					{"body": "resolved note", "resolvable": true, "resolved": true},
					{"body": "fix token = abcd1234abcd1234abcd1234", "resolvable": true, "resolved": false},
				},
			}})
		case strings.HasSuffix(p, "/pipelines"):
			if got := r.URL.Query().Get("sha"); got != "head1" {
				t.Errorf("pipeline sha = %q", got)
			}
			if got := r.URL.Query().Get("order_by"); got != "id" {
				t.Errorf("order_by = %q", got)
			}
			glWriteJSON(t, w, []map[string]any{{"id": 11, "sha": "head1", "status": "success"}})
		case strings.HasSuffix(p, "/pipelines/11/jobs"):
			glWriteJSON(t, w, []map[string]any{{"id": 110, "name": "build", "status": "success"}})
		case strings.HasSuffix(p, "/approvals"):
			glWriteJSON(t, w, map[string]any{"approved_by": []map[string]any{
				{"user": map[string]string{"username": "alice"}},
				{"user": map[string]string{"username": "bob"}},
			}})
		case strings.HasSuffix(p, "/repository/branches/main"):
			glWriteJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"id": "base1"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			http.NotFound(w, r)
		}
	}))

	o, err := g.Observe(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if o.PRNumber != 7 || o.HeadSHA != "head1" || o.PRState != "opened" || o.Conflict {
		t.Fatalf("bad observation head: %+v", o)
	}
	if len(o.Labels) != 1 || o.Labels[0] != "ready" {
		t.Fatalf("bad labels: %v", o.Labels)
	}
	if !o.Behind || o.BaseSHA != "base1" {
		t.Fatalf("behind = %t base = %q, want true/base1", o.Behind, o.BaseSHA)
	}
	if o.Approvals != 2 {
		t.Fatalf("approvals = %d", o.Approvals)
	}
	if len(o.UnresolvedThreads) != 1 || !strings.Contains(o.UnresolvedThreads[0], "[REDACTED]") {
		t.Fatalf("unresolved threads = %v", o.UnresolvedThreads)
	}
	decision := core.Decide(o, nil)
	if decision.Verdict != core.VerdictNeedsAgentFix || !strings.Contains(decision.Reason, "unresolved discussion") {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestGitLabObserveFailedPipelineRedaction(t *testing.T) {
	const secret = "supertopsecrettoken1234567890"
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/merge_requests/8"):
			glWriteJSON(t, w, map[string]any{
				"iid": 8, "state": "opened", "sha": "sha2", "has_conflicts": false,
				"blocking_discussions_resolved": true,
				"approved_by":                   []map[string]any{{"user": map[string]string{"username": "alice"}}},
			})
		case strings.HasSuffix(p, "/threads"):
			glWriteJSON(t, w, []any{})
		case strings.HasSuffix(p, "/pipelines"):
			glWriteJSON(t, w, []map[string]any{{"id": 22, "sha": "sha2", "status": "failed"}})
		case strings.HasSuffix(p, "/pipelines/22/jobs"):
			glWriteJSON(t, w, []map[string]any{
				{"id": 220, "name": "unit", "status": "failed"},
				{"id": 221, "name": "lint", "status": "success"},
			})
		case strings.HasSuffix(p, "/jobs/220/trace"):
			if r.Method != http.MethodGet {
				t.Errorf("trace method = %s", r.Method)
			}
			_, _ = w.Write([]byte(strings.Repeat("x", 1400) + " token = " + secret))
		case strings.HasSuffix(p, "/approvals"):
			glWriteJSON(t, w, map[string]any{})
		case strings.HasSuffix(p, "/repository/branches/main"):
			glWriteJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"id": "base2"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			http.NotFound(w, r)
		}
	}))

	o, err := g.Observe(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if o.Approvals != 1 {
		t.Fatalf("approvals fallback = %d", o.Approvals)
	}
	if len(o.Checks) != 2 || o.Checks[0].Name != "unit" || o.Checks[0].State != core.CheckFailure {
		t.Fatalf("checks = %+v", o.Checks)
	}
	if o.Checks[1].State != core.CheckSuccess {
		t.Fatalf("second check = %+v", o.Checks[1])
	}
	if len([]rune(o.FailureExcerpt)) > 1200 {
		t.Fatalf("excerpt too long: %d", len([]rune(o.FailureExcerpt)))
	}
	if !strings.Contains(o.FailureExcerpt, "[REDACTED]") || strings.Contains(o.FailureExcerpt, secret) {
		t.Fatalf("excerpt not redacted: %q", o.FailureExcerpt)
	}
	if got := core.Decide(o, nil); got.Verdict != core.VerdictNeedsAgentFix {
		t.Fatalf("decision = %+v", got)
	}
}

func TestGitLabUpdateBranchRebase(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/merge_requests/9/rebase") {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["expected_head_sha"] != "old" {
			t.Errorf("body = %v", body)
		}
		glWriteJSON(t, w, map[string]any{"sha": "new"})
	}))

	sha, err := g.UpdateBranch(context.Background(), 9, "old")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "new" {
		t.Fatalf("sha = %q", sha)
	}
}

func TestGitLabMergeSquash(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/merge_requests/10/merge") {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["squash"] != true || body["sha"] != "expected" || body["commit_title"] != "title" {
			t.Errorf("body = %v", body)
		}
		glWriteJSON(t, w, map[string]any{"state": "merged"})
	}))
	if err := g.Merge(context.Background(), 10, "expected", "title"); err != nil {
		t.Fatal(err)
	}

	rejected := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		glWriteJSON(t, w, map[string]any{"state": "opened"})
	}))
	err := rejected.Merge(context.Background(), 10, "stale", "title")
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error = %v", err)
	}
}

func TestGitLabBaseBranchStatus(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/pipelines"):
			if r.URL.Query().Get("ref") != "main" {
				t.Errorf("ref = %q", r.URL.Query().Get("ref"))
			}
			glWriteJSON(t, w, []map[string]any{{"id": 33, "sha": "basesha", "ref": "main", "status": "failed"}})
		case strings.HasSuffix(p, "/pipelines/33/jobs"):
			glWriteJSON(t, w, []map[string]any{
				{"id": 330, "name": "lint", "status": "failed"},
				{"id": 331, "name": "docs", "status": "success"},
			})
		case strings.HasSuffix(p, "/jobs/330/trace"):
			_, _ = w.Write([]byte("lint failed: password = mysecretpassword123456"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			http.NotFound(w, r)
		}
	}))

	status, err := g.BaseBranchStatus(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if status.BaseBranch != "main" || status.HeadSHA != "basesha" || !status.Red() {
		t.Fatalf("status = %+v", status)
	}
	if len(status.FailedChecks) != 1 || status.FailedChecks[0].Name != "lint" || status.FailedChecks[0].State != core.CheckFailure {
		t.Fatalf("failed checks = %+v", status.FailedChecks)
	}
	if !strings.Contains(status.FailureExcerpt, "[REDACTED]") || strings.Contains(status.FailureExcerpt, "mysecretpassword123456") {
		t.Fatalf("excerpt = %q", status.FailureExcerpt)
	}
}

func TestGitLabFindOpenPRByBranch(t *testing.T) {
	var empty atomic.Bool
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/merge_requests") {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("state") != "opened" || q.Get("source_branch") != "feature/x" {
			t.Errorf("query = %v", q)
		}
		if empty.Load() {
			glWriteJSON(t, w, []any{})
			return
		}
		glWriteJSON(t, w, []map[string]any{{"iid": 42}})
	}))

	iid, err := g.FindOpenPRByBranch(context.Background(), "feature/x")
	if err != nil || iid != 42 {
		t.Fatalf("iid = %d, err = %v", iid, err)
	}
	empty.Store(true)
	_, err = g.FindOpenPRByBranch(context.Background(), "feature/x")
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("error = %v", err)
	}
}

// glObserveHandler serves a complete observation. When fail names a provider
// call, that call answers with HTTP 500.
func glObserveHandler(t *testing.T, fail string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		part := ""
		switch {
		case strings.HasSuffix(p, "/merge_requests/7"):
			part = "mr"
		case strings.HasSuffix(p, "/threads"):
			part = "threads"
		case strings.HasSuffix(p, "/approvals"):
			part = "approvals"
		case strings.HasSuffix(p, "/pipelines"):
			part = "pipelines"
		case strings.HasSuffix(p, "/jobs"):
			part = "jobs"
		case strings.HasSuffix(p, "/repository/branches/main"):
			part = "base"
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			http.NotFound(w, r)
			return
		}
		if part == fail {
			http.Error(w, "provider down", http.StatusInternalServerError)
			return
		}
		switch part {
		case "mr":
			glWriteJSON(t, w, map[string]any{
				"iid": 7, "state": "opened", "sha": "head1", "target_branch": "main",
				"diverged_commits_count": 0,
			})
		case "threads":
			glWriteJSON(t, w, []any{})
		case "approvals":
			glWriteJSON(t, w, map[string]any{"approved_by": []map[string]any{
				{"user": map[string]string{"username": "alice"}},
			}})
		case "pipelines":
			glWriteJSON(t, w, []map[string]any{{"id": 11, "sha": "head1", "status": "success"}})
		case "jobs":
			glWriteJSON(t, w, []map[string]any{{"id": 110, "name": "build", "status": "success"}})
		case "base":
			glWriteJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"id": "base1"}})
		}
	})
}

func TestGitLabObserveCompleteObservationMerges(t *testing.T) {
	g := glTestServer(t, glObserveHandler(t, ""))
	o, err := g.Observe(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if o.Incomplete || o.IncompleteReason != "" {
		t.Fatalf("incomplete = %t, reason = %q; want a complete observation", o.Incomplete, o.IncompleteReason)
	}
	if got := core.Decide(o, nil); got.Verdict != core.VerdictMerge {
		t.Fatalf("decision = %+v, want %s", got, core.VerdictMerge)
	}
}

func TestGitLabObserveProviderFailureWaits(t *testing.T) {
	for _, part := range []string{"threads", "approvals", "pipelines", "jobs", "base"} {
		t.Run(part, func(t *testing.T) {
			g := glTestServer(t, glObserveHandler(t, part))
			o, err := g.Observe(context.Background(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if !o.Incomplete {
				t.Fatal("Incomplete = false, want true")
			}
			if !strings.Contains(o.IncompleteReason, part) {
				t.Fatalf("reason = %q, want it to name %q", o.IncompleteReason, part)
			}
			if got := core.Decide(o, nil); got.Verdict != core.VerdictWait {
				t.Fatalf("decision = %+v, want %s", got, core.VerdictWait)
			}
		})
	}
}

func TestGitLabObserveMalformedJobsWaits(t *testing.T) {
	good := glObserveHandler(t, "")
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			_, _ = w.Write([]byte(`[{"id":`))
			return
		}
		good.ServeHTTP(w, r)
	}))
	o, err := g.Observe(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Incomplete || !strings.Contains(o.IncompleteReason, "jobs") {
		t.Fatalf("incomplete = %t, reason = %q; want a jobs failure", o.Incomplete, o.IncompleteReason)
	}
	if got := core.Decide(o, nil); got.Verdict != core.VerdictWait {
		t.Fatalf("decision = %+v, want %s", got, core.VerdictWait)
	}
}

func TestGitLabObserveMalformedMRFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"empty object": `{}`,
		"broken json":  `{"iid": 7,`,
	} {
		t.Run(name, func(t *testing.T) {
			g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			_, err := g.Observe(context.Background(), 7)
			if core.CodeOf(err) != core.CodeUnavailable {
				t.Fatalf("err = %v, want unavailable", err)
			}
		})
	}
}

func TestGitLabUpdateBranchMalformedResponse(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sha":`))
	}))
	_, err := g.UpdateBranch(context.Background(), 9, "old")
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestGitLabMergeRequiresConfirmedState(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code core.ErrorCode
	}{
		{"malformed json", `{"state":`, core.CodeUnavailable},
		{"no state", `{}`, core.CodeConflict},
		{"not merged", `{"state":"opened"}`, core.CodeConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			err := g.Merge(context.Background(), 10, "expected", "title")
			if core.CodeOf(err) != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestGitLabBreakerStopsRepeatedFailures(t *testing.T) {
	var calls atomic.Int64
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	for i := 0; i < 6; i++ {
		if _, err := g.request(context.Background(), http.MethodGet, "/fail", nil, nil); err == nil {
			t.Fatal("failing request returned nil error")
		}
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("server calls = %d, want 6", got)
	}
	if _, err := g.request(context.Background(), http.MethodGet, "/fail", nil, nil); err == nil {
		t.Fatal("request after the breaker opened returned nil error")
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("server calls after open = %d, want 6", got)
	}
}
