package session

import (
	"fmt"
	"testing"
	"time"
)

func TestBuildSummaryThrottlesOneLineUpdates(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	summary := ActivitySummary{SessionID: "ses-1", State: ActivityActive}
	updates := 0
	lastLine := ""

	for i := 0; i < 60; i++ {
		now := base.Add(time.Duration(i) * time.Second)
		line := fmt.Sprintf("step %d", i)
		events := []OutputEvent{{At: now, Line: line}}
		summary = BuildSummary(events, summary, now, DefaultSummaryThrottle)
		if summary.OneLine != lastLine {
			updates++
			lastLine = summary.OneLine
		}
	}

	if updates == 0 || updates > 4 {
		t.Fatalf("60 ticks with output produced %d OneLine updates, want 1..4", updates)
	}
}

func TestBuildSummarySilenceOnlyGrowsNoOutputFor(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	first := BuildSummary(
		[]OutputEvent{{At: base, Line: "working on card CO-05"}},
		ActivitySummary{SessionID: "ses-1"},
		base,
		DefaultSummaryThrottle,
	)

	now := base.Add(30 * time.Second)
	got := BuildSummary(nil, first, now, DefaultSummaryThrottle)

	if got.NoOutputFor != 30*time.Second {
		t.Fatalf("NoOutputFor = %s, want 30s", got.NoOutputFor)
	}
	if got.OneLine != first.OneLine {
		t.Fatalf("OneLine changed during silence: %q -> %q", first.OneLine, got.OneLine)
	}
	if got.LastOutputAt != first.LastOutputAt {
		t.Fatalf("LastOutputAt changed during silence: %s -> %s", first.LastOutputAt, got.LastOutputAt)
	}
	if got.State != first.State {
		t.Fatalf("State changed before the stall threshold: %q -> %q", first.State, got.State)
	}
	if got.SessionID != first.SessionID {
		t.Fatalf("SessionID changed during silence: %q -> %q", first.SessionID, got.SessionID)
	}
}

func TestBuildSummaryStallThenResume(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	active := BuildSummary(
		[]OutputEvent{{At: base, Line: "working"}},
		ActivitySummary{SessionID: "ses-1"},
		base,
		DefaultSummaryThrottle,
	)

	quietAt := base.Add(DefaultStallAfter)
	stalled := BuildSummary(nil, active, quietAt, DefaultSummaryThrottle)
	if stalled.State != ActivityStalled {
		t.Fatalf("State = %q after the stall threshold, want stalled", stalled.State)
	}
	if stalled.OneLine != active.OneLine {
		t.Fatalf("stall rewrote OneLine: %q -> %q", active.OneLine, stalled.OneLine)
	}

	resumeAt := quietAt.Add(time.Second)
	resumed := BuildSummary(
		[]OutputEvent{{At: resumeAt, Line: "back at it"}},
		stalled,
		resumeAt,
		DefaultSummaryThrottle,
	)
	if resumed.State == ActivityStalled || resumed.State != ActivityActive {
		t.Fatalf("State = %q after output resumed, want active", resumed.State)
	}
	if resumed.NoOutputFor != 0 {
		t.Fatalf("NoOutputFor = %s after output resumed, want 0", resumed.NoOutputFor)
	}
	if resumed.OneLine != "back at it" {
		t.Fatalf("OneLine = %q after output resumed, want the new line", resumed.OneLine)
	}
}

func TestBuildSummaryIdleNeverBecomesReady(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	idle := ActivitySummary{
		SessionID:    "ses-2",
		State:        ActivityIdle,
		OneLine:      "waiting for input",
		LastOutputAt: base,
	}

	quiet := BuildSummary(nil, idle, base.Add(30*time.Second), DefaultSummaryThrottle)
	if quiet.State != ActivityIdle {
		t.Fatalf("State = %q after brief silence, want idle", quiet.State)
	}

	late := BuildSummary(nil, idle, base.Add(DefaultStallAfter), DefaultSummaryThrottle)
	if late.State == "ready" || late.State != ActivityStalled {
		t.Fatalf("State = %q after long silence, want stalled and never ready", late.State)
	}
}

func TestBuildSummaryUsesLastMeaningfulLine(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	events := []OutputEvent{
		{At: base, Line: "first line"},
		{At: base.Add(time.Second), Line: "   "},
		{At: base.Add(2 * time.Second), Line: "  final line  "},
	}
	got := BuildSummary(events, ActivitySummary{}, base.Add(2*time.Second), DefaultSummaryThrottle)

	if got.OneLine != "final line" {
		t.Fatalf("OneLine = %q, want the last trimmed non-empty line", got.OneLine)
	}
	if got.LastOutputAt != base.Add(2*time.Second) {
		t.Fatalf("LastOutputAt = %s, want the time of the last non-empty line", got.LastOutputAt)
	}
}
