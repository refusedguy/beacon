package scheduler

import (
	"testing"
	"time"
	_ "time/tzdata" // embedded IANA zones keep the DST test independent of the host
)

func atUTC(hour, minute int) time.Time {
	return time.Date(2026, 3, 10, hour, minute, 0, 0, time.UTC)
}

func TestIntervalPlansNextBoundary(t *testing.T) {
	s, err := Interval("15m", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	now := atUTC(10, 0)
	first, err := s.NextRun(now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if want := atUTC(10, 15); !first.At.Equal(want) {
		t.Fatalf("first next_run_at = %v, want %v", first.At, want)
	}
	if first.Done || first.Skipped != 0 {
		t.Fatalf("first plan = %+v, want recurring with no skips", first)
	}

	second, err := s.NextRun(atUTC(10, 15), atUTC(10, 15))
	if err != nil {
		t.Fatal(err)
	}
	if want := atUTC(10, 30); !second.At.Equal(want) {
		t.Fatalf("second next_run_at = %v, want %v", second.At, want)
	}
}

func TestFixedTimeFiresOnceInServerZone(t *testing.T) {
	s, err := FixedTime("0 9 * * *", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.loc != time.Local {
		t.Fatalf("nil location = %v, want time.Local", s.loc)
	}

	// Planning before 09:00 yields that morning's run.
	next, err := s.NextRun(time.Date(2026, 3, 10, 8, 0, 0, 0, time.Local), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)
	if !next.At.Equal(want) || next.Done {
		t.Fatalf("plan before 09:00 = %+v, want %v not done", next, want)
	}

	// Planning during the 09:00 minute still returns that same run, so it
	// fires once even if the planner is called a few seconds late.
	late, err := s.NextRun(want.Add(30*time.Second), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !late.At.Equal(want) || late.Done {
		t.Fatalf("late plan = %+v, want the same %v", late, want)
	}

	// Once a run is recorded the schedule is consumed, not repeated.
	done, err := s.NextRun(want, want)
	if err != nil {
		t.Fatal(err)
	}
	if !done.Done || !done.At.IsZero() {
		t.Fatalf("plan after run = %+v, want done with no next_run_at", done)
	}
}

func TestWebhookMark(t *testing.T) {
	s, err := Webhook("deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Accepts("deploy") || !s.Accepts(" deploy ") {
		t.Fatal("matching mark was not accepted")
	}
	if s.Accepts("push") || s.Accepts("deploy-extra") {
		t.Fatal("unrelated mark was accepted")
	}
	if s.Kind() != KindMark {
		t.Fatalf("kind = %v, want mark", s.Kind())
	}

	// A mark schedule has no time-based next_run_at.
	next, err := s.NextRun(atUTC(10, 0), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Done || !next.At.IsZero() {
		t.Fatalf("mark plan = %+v, want done with no next_run_at", next)
	}
	if _, err := Webhook("  "); err == nil {
		t.Fatal("empty mark was accepted")
	}
}

func TestDSTSpringForwardSkipsNonexistentTime(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-03-08 jumps from 02:00 to 03:00: 02:30 does not exist that day.
	s, err := FixedTime("30 2 * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.NextRun(time.Date(2026, 3, 8, 1, 0, 0, 0, loc), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 9, 2, 30, 0, 0, loc)
	if !next.At.Equal(want) {
		t.Fatalf("next 02:30 after spring forward = %v, want %v", next.At, want)
	}
	if next.At.UTC().Hour() != 6 || next.At.UTC().Minute() != 30 {
		t.Fatalf("returned a nonexistent local time: %v UTC", next.At.UTC())
	}
}

func TestDSTFallBackUsesFirstOccurrence(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-01 repeats 01:00-01:59: 01:30 happens twice. Pick the first.
	s, err := FixedTime("30 1 * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.NextRun(time.Date(2026, 11, 1, 0, 30, 0, 0, loc), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	wantUTC := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // 01:30 EDT, UTC-4
	if !next.At.Equal(wantUTC) {
		t.Fatalf("next 01:30 after fall back = %v UTC, want %v UTC", next.At.UTC(), wantUTC)
	}
}

func TestMissedIntervalIsNotDuplicated(t *testing.T) {
	s, err := Interval("1h", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	last := atUTC(9, 0)
	now := time.Date(2026, 3, 10, 12, 30, 0, 0, time.UTC)
	next, err := s.NextRun(now, last)
	if err != nil {
		t.Fatal(err)
	}
	want := atUTC(13, 0)
	if !next.At.Equal(want) {
		t.Fatalf("next_run_at = %v, want %v", next.At, want)
	}
	if next.Skipped != 3 {
		t.Fatalf("skipped = %d, want 3 missed boundaries", next.Skipped)
	}
	if !next.At.After(now) {
		t.Fatalf("missed intervals were replayed: next %v is not after now %v", next.At, now)
	}

	// A boundary exactly at now is due, not a missed interval.
	due, err := s.NextRun(atUTC(12, 0), atUTC(11, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !due.At.Equal(atUTC(12, 0)) || due.Skipped != 0 {
		t.Fatalf("exact boundary plan = %+v, want 12:00 with no skips", due)
	}
}

func TestInvalidSchedulesAreRefused(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"zero interval", func() error { _, err := Interval("0", time.UTC); return err }},
		{"bad duration", func() error { _, err := Interval("often", time.UTC); return err }},
		{"short cron", func() error { _, err := FixedTime("0 9 * *", time.UTC); return err }},
		{"out of range", func() error { _, err := FixedTime("60 9 * * *", time.UTC); return err }},
		{"empty mark", func() error { _, err := Webhook(" "); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
