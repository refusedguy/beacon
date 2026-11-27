package scm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/refusedguy/beacon/pkg/core"
)

func ghChecksServer(t *testing.T, runs []*github.CheckRun) *GitHub {
	t.Helper()
	return ghObserveGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/check-runs") {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": len(runs),
			"check_runs":  runs,
		})
	}))
}

func TestGitHubChecksKeepsNewestAttemptPerName(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	run := func(name, conclusion string, completed time.Time) *github.CheckRun {
		return &github.CheckRun{
			Name:        github.String(name),
			Status:      github.String("completed"),
			Conclusion:  github.String(conclusion),
			CompletedAt: &github.Timestamp{Time: completed},
		}
	}
	g := ghChecksServer(t, []*github.CheckRun{
		run("lint", "cancelled", base),
		run("lint", "success", base.Add(time.Minute)),
		run("ci", "success", base.Add(2*time.Minute)),
	})
	checks, err := g.checks(context.Background(), "abc")
	if err != nil {
		t.Fatalf("g.checks: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks = %+v, want two unique names", checks)
	}
	for _, c := range checks {
		if c.State != core.CheckSuccess {
			t.Fatalf("check %s = %s, want success", c.Name, c.State)
		}
	}
}

func TestCheckStateKeepsCancelledDistinctFromSkipped(t *testing.T) {
	cases := []struct {
		conclusion string
		want       core.CheckState
	}{
		{"success", core.CheckSuccess},
		{"neutral", core.CheckSuccess},
		{"skipped", core.CheckSkipped},
		{"cancelled", core.CheckCancelled},
		{"", core.CheckPending},
		{"failure", core.CheckFailure},
	}
	for _, tc := range cases {
		c := &github.CheckRun{
			Status:     github.String("completed"),
			Conclusion: github.String(tc.conclusion),
		}
		if got := checkState(c); got != tc.want {
			t.Errorf("checkState(%q) = %s, want %s", tc.conclusion, got, tc.want)
		}
	}
}

func TestCheckRunNewerUsesCompletionTime(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	older := &github.CheckRun{CompletedAt: &github.Timestamp{Time: base}}
	newer := &github.CheckRun{CompletedAt: &github.Timestamp{Time: base.Add(time.Minute)}}
	if !checkRunNewer(newer, older) {
		t.Fatal("newer attempt must win")
	}
	if checkRunNewer(older, newer) {
		t.Fatal("older attempt must not win")
	}
}
