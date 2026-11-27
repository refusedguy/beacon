package app_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// fakeReportStore mirrors the window filters of the sqlite adapter. It exposes
// only the four reads a report needs and records the window it was given.
type fakeReportStore struct {
	decisions []app.Decision
	events    []app.RunEvent
	incidents []*core.BaseIncident
	runs      []*core.Run
	err       error
	calls     []string
	windows   []time.Time
}

func (f *fakeReportStore) DecisionsSince(_ context.Context, since time.Time) ([]app.Decision, error) {
	f.calls = append(f.calls, "decisions")
	f.windows = append(f.windows, since)
	if f.err != nil {
		return nil, f.err
	}
	out := []app.Decision{}
	for _, d := range f.decisions {
		if !d.At.Before(since) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeReportStore) EventsSince(_ context.Context, since time.Time) ([]app.RunEvent, error) {
	f.calls = append(f.calls, "events")
	f.windows = append(f.windows, since)
	if f.err != nil {
		return nil, f.err
	}
	out := []app.RunEvent{}
	for _, e := range f.events {
		if !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeReportStore) IncidentsSince(_ context.Context, since time.Time) ([]*core.BaseIncident, error) {
	f.calls = append(f.calls, "incidents")
	f.windows = append(f.windows, since)
	if f.err != nil {
		return nil, f.err
	}
	out := []*core.BaseIncident{}
	for _, in := range f.incidents {
		if in.CreatedAt >= since.UnixNano() || in.ResolvedAt >= since.UnixNano() || in.Status == "open" {
			out = append(out, in)
		}
	}
	return out, nil
}

func (f *fakeReportStore) RunsSince(_ context.Context, since time.Time) ([]*core.Run, error) {
	f.calls = append(f.calls, "runs")
	f.windows = append(f.windows, since)
	if f.err != nil {
		return nil, f.err
	}
	out := []*core.Run{}
	for _, r := range f.runs {
		if !r.UpdatedAt.Before(since) || !r.State.Terminal() {
			out = append(out, r)
		}
	}
	return out, nil
}

func reportTime(t *testing.T, raw string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", raw, err)
	}
	return at
}

func reportFixture(t *testing.T) (*fakeReportStore, time.Time, time.Time) {
	t.Helper()
	now := reportTime(t, "2026-11-27T20:00:00Z")
	since := reportTime(t, "2026-11-27T12:00:00Z")
	store := &fakeReportStore{
		events: []app.RunEvent{
			{ID: 1, RunID: "run-1", Kind: string(core.RunMerged), At: reportTime(t, "2026-11-27T13:00:00Z")},
			{ID: 2, RunID: "run-1", Kind: string(core.RunMerged), At: reportTime(t, "2026-11-27T14:00:00Z")},
			{ID: 3, RunID: "run-2", Kind: string(core.RunMerged), At: reportTime(t, "2026-11-27T15:00:00Z")},
			{ID: 4, RunID: "run-x", Kind: "started", At: reportTime(t, "2026-11-27T16:00:00Z")},
		},
		decisions: []app.Decision{
			{ID: 1, RunID: "run-4", Verdict: string(core.VerdictBlocked), At: reportTime(t, "2026-11-27T13:30:00Z")},
			{ID: 2, RunID: "run-4", Verdict: string(core.VerdictBlocked), At: reportTime(t, "2026-11-27T14:30:00Z")},
			{ID: 3, RunID: "run-5", Verdict: string(core.VerdictBlocked), At: reportTime(t, "2026-11-27T14:45:00Z")},
			{ID: 4, RunID: "run-6", Verdict: string(core.VerdictNeedsAgentFix), At: reportTime(t, "2026-11-27T14:50:00Z")},
			{ID: 5, RunID: "run-old", Verdict: string(core.VerdictBlocked), At: reportTime(t, "2026-11-27T09:00:00Z")},
		},
		incidents: []*core.BaseIncident{
			{ID: "inc-1", Status: "resolved", CreatedAt: reportTime(t, "2026-11-27T13:00:00Z").UnixNano(), ResolvedAt: reportTime(t, "2026-11-27T14:00:00Z").UnixNano()},
			{ID: "inc-2", Status: "open", CreatedAt: reportTime(t, "2026-11-27T11:00:00Z").UnixNano()},
			{ID: "inc-3", Status: "open", CreatedAt: reportTime(t, "2026-11-27T12:30:00Z").UnixNano()},
			{ID: "inc-4", Status: "resolved", CreatedAt: reportTime(t, "2026-11-27T10:00:00Z").UnixNano(), ResolvedAt: reportTime(t, "2026-11-27T11:00:00Z").UnixNano()},
		},
		runs: []*core.Run{
			{ID: "run-3", State: core.RunClosed, PRNumber: 0, UpdatedAt: reportTime(t, "2026-11-27T14:00:00Z")},
			{ID: "run-6", State: core.RunMerged, IsFixer: true, CreatedAt: reportTime(t, "2026-11-27T13:00:00Z"), UpdatedAt: reportTime(t, "2026-11-27T13:00:00Z")},
			{ID: "run-7", State: core.RunMerged, PRNumber: 12, UpdatedAt: reportTime(t, "2026-11-27T15:00:00Z")},
			{ID: "run-8", State: core.RunClosed, PRNumber: 55, UpdatedAt: reportTime(t, "2026-11-27T15:00:00Z")},
			{ID: "run-9", State: core.RunWaitingCI, CreatedAt: reportTime(t, "2026-11-27T08:00:00Z"), UpdatedAt: reportTime(t, "2026-11-27T09:00:00Z")},
			{ID: "run-10", State: core.RunMerged, PRNumber: 13, UpdatedAt: reportTime(t, "2026-11-27T09:00:00Z")},
		},
	}
	return store, now, since
}

func TestReportEmptyWindow(t *testing.T) {
	store, now, since := reportFixture(t)
	store.decisions = nil
	store.events = nil
	store.incidents = nil
	store.runs = nil

	got, err := (app.ReportService{Store: store}).Report(context.Background(), 8*time.Hour, now)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if !got.GeneratedAt.Equal(now) || !got.Since.Equal(since) {
		t.Fatalf("window = %v to %v, want %v to %v", got.Since, got.GeneratedAt, since, now)
	}
	want := []string{"decisions", "events", "incidents", "runs"}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
	for i, at := range store.windows {
		if !at.Equal(since) {
			t.Fatalf("window %d = %v, want %v", i, at, since)
		}
	}
	sections := map[string]app.ReportSection{
		"merges":             got.Merges,
		"blocks":             got.Blocks,
		"incidents_opened":   got.IncidentsOpened,
		"incidents_resolved": got.IncidentsResolved,
		"incidents_open":     got.IncidentsOpen,
		"fixer_attempts":     got.FixerAttempts,
		"orphan_closures":    got.OrphanClosures,
		"stalled":            got.Stalled,
	}
	for name, section := range sections {
		if section.Count != 0 || len(section.IDs) != 0 {
			t.Errorf("%s = %+v, want empty", name, section)
		}
		if section.IDs == nil {
			t.Errorf("%s IDs is nil, want an empty JSON array", name)
		}
	}
	text := app.FormatReport(got)
	if !strings.HasPrefix(text, "report since "+since.Format(time.RFC3339)) {
		t.Fatalf("text = %q, want a report header with the window start", text)
	}
	if strings.Count(text, ": 0\n") != len(sections) {
		t.Fatalf("text = %q, want one zero line per section", text)
	}
}

func TestReportMixedWindow(t *testing.T) {
	store, now, _ := reportFixture(t)
	got, err := (app.ReportService{Store: store}).Report(context.Background(), 8*time.Hour, now)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	cases := []struct {
		name string
		got  app.ReportSection
		want app.ReportSection
	}{
		{"merges", got.Merges, app.ReportSection{Count: 2, IDs: []string{"run-1", "run-2"}}},
		{"blocks", got.Blocks, app.ReportSection{Count: 2, IDs: []string{"run-4", "run-5"}}},
		{"incidents opened", got.IncidentsOpened, app.ReportSection{Count: 2, IDs: []string{"inc-1", "inc-3"}}},
		{"incidents resolved", got.IncidentsResolved, app.ReportSection{Count: 1, IDs: []string{"inc-1"}}},
		{"incidents open", got.IncidentsOpen, app.ReportSection{Count: 2, IDs: []string{"inc-2", "inc-3"}}},
		{"fixer attempts", got.FixerAttempts, app.ReportSection{Count: 1, IDs: []string{"run-6"}}},
		{"orphan closures", got.OrphanClosures, app.ReportSection{Count: 1, IDs: []string{"run-3"}}},
		{"stalled", got.Stalled, app.ReportSection{Count: 1, IDs: []string{"run-9"}}},
	}
	for _, tc := range cases {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s = %+v, want %+v", tc.name, tc.got, tc.want)
		}
	}
	text := app.FormatReport(got)
	for _, want := range []string{
		"merges: 2 [run-1 run-2]",
		"blocks: 2 [run-4 run-5]",
		"incidents open: 2 [inc-2 inc-3]",
		"stalled: 1 [run-9]",
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("text %q does not contain %q", text, want)
		}
	}
}

func TestReportOpenIncidentCrossingBoundary(t *testing.T) {
	store, now, _ := reportFixture(t)
	got, err := (app.ReportService{Store: store}).Report(context.Background(), 8*time.Hour, now)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if containsID(got.IncidentsOpened.IDs, "inc-2") {
		t.Errorf("inc-2 was opened before the window but appears in opened: %v", got.IncidentsOpened.IDs)
	}
	if !containsID(got.IncidentsOpen.IDs, "inc-2") {
		t.Errorf("open incident inc-2 missing from still open: %v", got.IncidentsOpen.IDs)
	}
	if !containsID(got.Stalled.IDs, "run-9") {
		t.Errorf("run stalled before the window missing from stalled: %v", got.Stalled.IDs)
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestReportLimitsIDsButKeepsCount(t *testing.T) {
	store, now, _ := reportFixture(t)
	got, err := (app.ReportService{Store: store, Limit: 1}).Report(context.Background(), 8*time.Hour, now)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if got.Merges.Count != 2 || !reflect.DeepEqual(got.Merges.IDs, []string{"run-1"}) {
		t.Fatalf("merges = %+v, want count 2 and one visible ID", got.Merges)
	}
}

func TestReportRejectsBadInput(t *testing.T) {
	store, now, _ := reportFixture(t)
	cases := []struct {
		name    string
		service app.ReportService
		window  time.Duration
		want    string
	}{
		{"no store", app.ReportService{}, time.Hour, "needs a store"},
		{"zero window", app.ReportService{Store: store}, 0, "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.service.Report(context.Background(), tc.window, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	store.err = errors.New("store unavailable")
	_, err := (app.ReportService{Store: store}).Report(context.Background(), time.Hour, now)
	if err == nil || !strings.Contains(err.Error(), "decisions: store unavailable") {
		t.Fatalf("error = %v, want the decisions read error", err)
	}
}
