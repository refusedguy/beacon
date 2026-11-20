package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openScheduleStore(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return d
}

func TestDueSchedulesEmptyStore(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	due, err := d.DueSchedules(now)
	if err != nil {
		t.Fatalf("DueSchedules on empty store: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("empty store returned %d due schedules, want 0", len(due))
	}
}

func TestSaveScheduleAndDue(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	early := now.Add(-time.Hour)
	late := now.Add(time.Hour)

	if err := d.SaveSchedule(Schedule{
		ID: "later", Kind: ScheduleKindCron, Spec: "0 * * * *",
		TargetCard: "card-b", NextRun: late, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveSchedule later: %v", err)
	}
	if err := d.SaveSchedule(Schedule{
		ID: "earlier", Kind: ScheduleKindInterval, Spec: "30m",
		TargetCard: "card-a", NextRun: early, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveSchedule earlier: %v", err)
	}

	due, err := d.DueSchedules(now)
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due schedules, want 1: %+v", len(due), due)
	}
	got := due[0]
	if got.ID != "earlier" || got.Kind != ScheduleKindInterval || got.Spec != "30m" ||
		got.TargetCard != "card-a" || !got.NextRun.Equal(early) || !got.Enabled {
		t.Fatalf("unexpected due schedule: %+v", got)
	}

	// At the later time both rows are due, in next_run order.
	due, err = d.DueSchedules(late)
	if err != nil {
		t.Fatalf("DueSchedules later: %v", err)
	}
	if len(due) != 2 || due[0].ID != "earlier" || due[1].ID != "later" {
		t.Fatalf("due order = %+v, want earlier then later", due)
	}
}

func TestDueSchedulesSkipsDisabled(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	overdue := now.Add(-time.Minute)
	s := Schedule{
		ID: "off", Kind: ScheduleKindWebhook, Spec: "push",
		TargetCard: "card", NextRun: overdue, Enabled: false,
	}
	if err := d.SaveSchedule(s); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	due, err := d.DueSchedules(now)
	if err != nil || len(due) != 0 {
		t.Fatalf("disabled schedule is due: %+v, err=%v", due, err)
	}

	s.Enabled = true
	if err := d.SaveSchedule(s); err != nil {
		t.Fatalf("SaveSchedule re-enable: %v", err)
	}
	due, err = d.DueSchedules(now)
	if err != nil || len(due) != 1 || due[0].ID != "off" {
		t.Fatalf("re-enabled schedule not due: %+v, err=%v", due, err)
	}
}

func TestMarkTriggeredShiftsNextRun(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	overdue := now.Add(-time.Hour)
	if err := d.SaveSchedule(Schedule{
		ID: "tick", Kind: ScheduleKindInterval, Spec: "1h",
		TargetCard: "card", NextRun: overdue, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	due, err := d.DueSchedules(now)
	if err != nil || len(due) != 1 {
		t.Fatalf("precondition: due = %+v, err = %v", due, err)
	}

	next := now.Add(time.Hour)
	if err := d.MarkTriggered("tick", next); err != nil {
		t.Fatalf("MarkTriggered: %v", err)
	}
	due, err = d.DueSchedules(now)
	if err != nil {
		t.Fatalf("DueSchedules after trigger: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("triggered schedule still due at now: %+v", due)
	}

	due, err = d.DueSchedules(next)
	if err != nil || len(due) != 1 {
		t.Fatalf("schedule not due at new time: %+v, err = %v", due, err)
	}
	if !due[0].NextRun.Equal(next) {
		t.Fatalf("next_run = %v, want %v", due[0].NextRun, next)
	}
}

func TestMarkTriggeredUnknownID(t *testing.T) {
	d := openScheduleStore(t)
	err := d.MarkTriggered("missing", time.Now().UTC())
	if err == nil {
		t.Fatal("MarkTriggered on unknown id returned no error")
	}
}

func TestDisableSchedule(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := d.SaveSchedule(Schedule{
		ID: "job", Kind: ScheduleKindInterval, Spec: "10m",
		TargetCard: "card", NextRun: now.Add(-time.Minute), Enabled: true,
	}); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	if err := d.DisableSchedule("job"); err != nil {
		t.Fatalf("DisableSchedule: %v", err)
	}
	due, err := d.DueSchedules(now)
	if err != nil || len(due) != 0 {
		t.Fatalf("disabled schedule is due: %+v, err=%v", due, err)
	}
	if err := d.DisableSchedule("missing"); err == nil {
		t.Fatal("DisableSchedule on unknown id returned no error")
	}
}

func TestSaveScheduleUpsert(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	base := Schedule{
		ID: "same", Kind: ScheduleKindInterval, Spec: "5m",
		TargetCard: "old", NextRun: now.Add(-time.Hour), Enabled: true,
	}
	if err := d.SaveSchedule(base); err != nil {
		t.Fatalf("first SaveSchedule: %v", err)
	}
	updated := base
	updated.Kind = ScheduleKindWebhook
	updated.Spec = "dispatch"
	updated.TargetCard = "new"
	updated.NextRun = now.Add(time.Hour)
	if err := d.SaveSchedule(updated); err != nil {
		t.Fatalf("second SaveSchedule: %v", err)
	}

	due, err := d.DueSchedules(updated.NextRun)
	if err != nil || len(due) != 1 {
		t.Fatalf("upsert left %d rows: %+v, err=%v", len(due), due, err)
	}
	got := due[0]
	if got.ID != "same" || got.Kind != ScheduleKindWebhook || got.Spec != "dispatch" ||
		got.TargetCard != "new" || !got.NextRun.Equal(updated.NextRun) || !got.Enabled {
		t.Fatalf("row was not replaced: %+v", got)
	}
}

func TestSchedulesPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	next := now.Add(time.Hour)
	if err := d.SaveSchedule(Schedule{
		ID: "persist", Kind: ScheduleKindCron, Spec: "0 3 * * *",
		TargetCard: "card", NextRun: next, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	due, err := reopened.DueSchedules(next)
	if err != nil || len(due) != 1 {
		t.Fatalf("schedule lost across restart: %+v, err=%v", due, err)
	}
	if !due[0].NextRun.Equal(next) {
		t.Fatalf("next_run after restart = %v, want %v", due[0].NextRun, next)
	}
}

func TestSaveScheduleValidation(t *testing.T) {
	d := openScheduleStore(t)
	now := time.Now().UTC()
	tests := []struct {
		name string
		s    Schedule
	}{
		{"empty id", Schedule{Kind: ScheduleKindInterval, Spec: "1m", NextRun: now}},
		{"invalid kind", Schedule{ID: "x", Kind: "timer", Spec: "1m", NextRun: now}},
		{"zero next run", Schedule{ID: "x", Kind: ScheduleKindInterval, Spec: "1m"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := d.SaveSchedule(tc.s); err == nil {
				t.Fatalf("SaveSchedule(%+v) returned no error", tc.s)
			}
		})
	}
	due, err := d.DueSchedules(now.Add(time.Second))
	if err != nil || len(due) != 0 {
		t.Fatalf("invalid saves wrote a row: %+v, err=%v", due, err)
	}
}
