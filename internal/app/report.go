package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// DefaultReportSince is the shift length used when beacon report has no
// --since flag.
const DefaultReportSince = 8 * time.Hour

// DefaultReportIDLimit caps the IDs printed for one report section. Counts
// still cover the whole window.
const DefaultReportIDLimit = 20

// ReportStore is the read-only time-range surface for a windowed report. Every
// method takes the window start; none of them writes.
type ReportStore interface {
	DecisionsSince(ctx context.Context, since time.Time) ([]Decision, error)
	EventsSince(ctx context.Context, since time.Time) ([]RunEvent, error)
	IncidentsSince(ctx context.Context, since time.Time) ([]*core.BaseIncident, error)
	RunsSince(ctx context.Context, since time.Time) ([]*core.Run, error)
}

// ReportSection is one grouped outcome. Count covers the whole window while
// IDs is capped so an operator sees a short list.
type ReportSection struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// Report is a bounded view of one shift. It changes no run, card or decision.
type Report struct {
	GeneratedAt       time.Time     `json:"generated_at"`
	Since             time.Time     `json:"since"`
	Merges            ReportSection `json:"merges"`
	Blocks            ReportSection `json:"blocks"`
	IncidentsOpened   ReportSection `json:"incidents_opened"`
	IncidentsResolved ReportSection `json:"incidents_resolved"`
	IncidentsOpen     ReportSection `json:"incidents_open"`
	FixerAttempts     ReportSection `json:"fixer_attempts"`
	OrphanClosures    ReportSection `json:"orphan_closures"`
	Stalled           ReportSection `json:"stalled"`
}

// ReportService groups time-range reads into a short operator report.
type ReportService struct {
	Store ReportStore
	Limit int
}

// Report collects the window ending at now. A zero now uses the current UTC
// time. A non-positive window is rejected before any store call.
func (s ReportService) Report(ctx context.Context, window time.Duration, now time.Time) (Report, error) {
	if s.Store == nil {
		return Report{}, errors.New("report needs a store")
	}
	if window <= 0 {
		return Report{}, errors.New("report window must be positive")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	since := now.Add(-window)
	decisions, err := s.Store.DecisionsSince(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("decisions: %w", err)
	}
	events, err := s.Store.EventsSince(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("events: %w", err)
	}
	incidents, err := s.Store.IncidentsSince(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("incidents: %w", err)
	}
	runs, err := s.Store.RunsSince(ctx, since)
	if err != nil {
		return Report{}, fmt.Errorf("runs: %w", err)
	}
	return groupReport(now, since, decisions, events, incidents, runs, s.limit()), nil
}

// limit falls back to the default when the service carries no cap.
func (s ReportService) limit() int {
	if s.Limit <= 0 {
		return DefaultReportIDLimit
	}
	return s.Limit
}

// groupReport derives every section from the four windowed reads. Merges and
// blocks keep one ID per run even when a run produced several records.
func groupReport(now, since time.Time, decisions []Decision, events []RunEvent, incidents []*core.BaseIncident, runs []*core.Run, limit int) Report {
	out := Report{GeneratedAt: now, Since: since}
	out.Merges = sectionFromIDs(eventRunIDs(events, string(core.RunMerged)), limit)
	out.Blocks = sectionFromIDs(decisionRunIDs(decisions, string(core.VerdictBlocked)), limit)

	var opened, resolved, open []string
	for _, in := range incidents {
		if in == nil {
			continue
		}
		if in.CreatedAt >= since.UnixNano() {
			opened = append(opened, in.ID)
		}
		if in.ResolvedAt >= since.UnixNano() {
			resolved = append(resolved, in.ID)
		}
		if in.Status == "open" {
			open = append(open, in.ID)
		}
	}
	out.IncidentsOpened = sectionFromIDs(opened, limit)
	out.IncidentsResolved = sectionFromIDs(resolved, limit)
	out.IncidentsOpen = sectionFromIDs(open, limit)

	var fixer, orphan, stalled []string
	for _, r := range runs {
		if r == nil {
			continue
		}
		if r.IsFixer && !r.CreatedAt.Before(since) {
			fixer = append(fixer, string(r.ID))
		}
		if r.State == core.RunClosed && r.PRNumber == 0 && !r.UpdatedAt.Before(since) {
			orphan = append(orphan, string(r.ID))
		}
		if !r.State.Terminal() && r.UpdatedAt.Before(since) {
			stalled = append(stalled, string(r.ID))
		}
	}
	out.FixerAttempts = sectionFromIDs(fixer, limit)
	out.OrphanClosures = sectionFromIDs(orphan, limit)
	out.Stalled = sectionFromIDs(stalled, limit)
	return out
}

// eventRunIDs collects one run ID per run that emitted kind in the window.
func eventRunIDs(events []RunEvent, kind string) []string {
	seen := make(map[string]bool)
	ids := []string{}
	for _, ev := range events {
		if ev.Kind != kind || ev.RunID == "" || seen[ev.RunID] {
			continue
		}
		seen[ev.RunID] = true
		ids = append(ids, ev.RunID)
	}
	return ids
}

// decisionRunIDs collects one run ID per run whose verdict matches.
func decisionRunIDs(decisions []Decision, verdict string) []string {
	seen := make(map[string]bool)
	ids := []string{}
	for _, d := range decisions {
		if d.Verdict != verdict || d.RunID == "" || seen[d.RunID] {
			continue
		}
		seen[d.RunID] = true
		ids = append(ids, d.RunID)
	}
	return ids
}

// sectionFromIDs caps the visible IDs while keeping the full count.
func sectionFromIDs(ids []string, limit int) ReportSection {
	out := ReportSection{Count: len(ids), IDs: []string{}}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out.IDs = append(out.IDs, ids...)
	return out
}

// FormatReport renders a report as short operator text. Each line gives a
// count and, when present, the IDs that need attention.
func FormatReport(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "report since %s\n", r.Since.Format(time.RFC3339))
	writeSection(&b, "merges", r.Merges)
	writeSection(&b, "blocks", r.Blocks)
	writeSection(&b, "incidents opened", r.IncidentsOpened)
	writeSection(&b, "incidents resolved", r.IncidentsResolved)
	writeSection(&b, "incidents open", r.IncidentsOpen)
	writeSection(&b, "fixer attempts", r.FixerAttempts)
	writeSection(&b, "orphan closures", r.OrphanClosures)
	writeSection(&b, "stalled", r.Stalled)
	return b.String()
}

func writeSection(b *strings.Builder, name string, s ReportSection) {
	fmt.Fprintf(b, "%s: %d", name, s.Count)
	if len(s.IDs) > 0 {
		fmt.Fprintf(b, " [%s]", strings.Join(s.IDs, " "))
	}
	b.WriteByte('\n')
}
