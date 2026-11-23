package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/worktree"
)

// mockWorktreeDurations replaces the gc seams with fakes and pins the clock
// used to time collection and apply. The clock advances one millisecond per
// read, so each timed section is positive without waiting on real time.
func mockWorktreeDurations(t *testing.T) {
	t.Helper()
	oldList, oldHeads, oldStatus, oldRemove, oldNow := gcWorktreeList, gcWorktreeHeads, gcWorktreeStatus, gcWorktreeRemove, gcNow
	t.Cleanup(func() {
		gcWorktreeList, gcWorktreeHeads, gcWorktreeStatus, gcWorktreeRemove, gcNow = oldList, oldHeads, oldStatus, oldRemove, oldNow
	})

	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	step := 0
	gcNow = func() time.Time {
		step++
		return base.Add(time.Duration(step) * time.Millisecond)
	}

	entries := []worktree.Entry{
		{Dir: "/repo/main", Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Branch: "main"},
		{Dir: "/repo/clean", Head: "1111111111111111111111111111111111111111", Branch: "clean"},
	}
	gcWorktreeList = func() ([]worktree.Entry, error) { return entries, nil }
	gcWorktreeHeads = func(*CLI) (map[string]string, error) { return nil, nil }
	gcWorktreeStatus = func(string) (string, error) { return "", nil }
	gcWorktreeRemove = func(string) error { return nil }
}

func gcWorktreeJSON(t *testing.T, args ...string) gcWorktreeReport {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &CLI{out: out, errOut: errOut, root: t.TempDir()}
	if code := runGCWorktree(c, args); code != 0 {
		t.Fatalf("runGCWorktree code = %d, stderr = %s", code, errOut.String())
	}
	var report gcWorktreeReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v; out = %s", err, out.String())
	}
	return report
}

// TestGCWorktreeJSONReportsDurations proves the apply path prints both
// millisecond fields with a positive value when worktrees are present.
func TestGCWorktreeJSONReportsDurations(t *testing.T) {
	mockWorktreeDurations(t)
	report := gcWorktreeJSON(t, "--format", "json", "--apply")
	if report.Mode != "applied" {
		t.Fatalf("mode = %q, want applied", report.Mode)
	}
	if report.CollectMS <= 0 {
		t.Errorf("collect_ms = %d, want > 0", report.CollectMS)
	}
	if report.ApplyMS <= 0 {
		t.Errorf("apply_ms = %d, want > 0", report.ApplyMS)
	}
}

// TestGCWorktreeDryRunLeavesApplyMSZero proves a dry run times only the plan.
func TestGCWorktreeDryRunLeavesApplyMSZero(t *testing.T) {
	mockWorktreeDurations(t)
	report := gcWorktreeJSON(t, "--format", "json")
	if report.Mode != "dry-run" {
		t.Fatalf("mode = %q, want dry-run", report.Mode)
	}
	if report.CollectMS <= 0 {
		t.Errorf("collect_ms = %d, want > 0", report.CollectMS)
	}
	if report.ApplyMS != 0 {
		t.Errorf("apply_ms = %d, want 0 in dry-run", report.ApplyMS)
	}
}
