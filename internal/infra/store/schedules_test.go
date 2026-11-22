package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
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

func assertScheduleEqual(t *testing.T, got, want Schedule) {
	t.Helper()
	if got.ID != want.ID || got.Kind != want.Kind || got.Spec != want.Spec ||
		got.TargetCard != want.TargetCard || !got.NextRun.Equal(want.NextRun) || got.Enabled != want.Enabled {
		t.Fatalf("schedule = %+v, want %+v", got, want)
	}
}

func TestScheduleCRUD(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	base := Schedule{
		ID: "job", Kind: ScheduleKindInterval, Spec: "15m",
		TargetCard: "card-a", NextRun: now.Add(time.Hour), Enabled: true,
	}

	if err := d.CreateSchedule(ctx, base); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	got, err := d.GetSchedule(ctx, "job")
	if err != nil {
		t.Fatalf("GetSchedule after create: %v", err)
	}
	assertScheduleEqual(t, got, base)

	list, err := d.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSchedules returned %d rows, want 1: %+v", len(list), list)
	}
	assertScheduleEqual(t, list[0], base)

	updated := base
	updated.Kind = ScheduleKindCron
	updated.Spec = "0 3 * * *"
	updated.TargetCard = "card-b"
	updated.NextRun = now.Add(2 * time.Hour)
	updated.Enabled = false
	if err := d.UpdateSchedule(ctx, updated); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	got, err = d.GetSchedule(ctx, "job")
	if err != nil {
		t.Fatalf("GetSchedule after update: %v", err)
	}
	assertScheduleEqual(t, got, updated)

	list, err = d.ListSchedules(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSchedules after update = %+v, err = %v", list, err)
	}
	assertScheduleEqual(t, list[0], updated)

	if err := d.DeleteSchedule(ctx, "job"); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, err := d.GetSchedule(ctx, "job"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("GetSchedule after delete = %v, want ErrScheduleNotFound", err)
	}
	list, err = d.ListSchedules(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("ListSchedules after delete = %+v, err = %v", list, err)
	}
}

func TestCreateScheduleRejectsDuplicate(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour)
	first := Schedule{
		ID: "dup", Kind: ScheduleKindWebhook, Spec: "push",
		TargetCard: "card-a", NextRun: next, Enabled: true,
	}
	if err := d.CreateSchedule(ctx, first); err != nil {
		t.Fatalf("first CreateSchedule: %v", err)
	}

	second := first
	second.Spec = "other"
	second.TargetCard = "card-b"
	if err := d.CreateSchedule(ctx, second); !errors.Is(err, ErrScheduleExists) {
		t.Fatalf("duplicate CreateSchedule = %v, want ErrScheduleExists", err)
	}
	got, err := d.GetSchedule(ctx, "dup")
	if err != nil {
		t.Fatalf("GetSchedule after duplicate: %v", err)
	}
	assertScheduleEqual(t, got, first)
}

func TestScheduleCRUDUnknownID(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour)
	missing := Schedule{
		ID: "missing", Kind: ScheduleKindInterval, Spec: "1m",
		TargetCard: "card", NextRun: next, Enabled: true,
	}

	if _, err := d.GetSchedule(ctx, "missing"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("GetSchedule unknown = %v, want ErrScheduleNotFound", err)
	}
	if err := d.UpdateSchedule(ctx, missing); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("UpdateSchedule unknown = %v, want ErrScheduleNotFound", err)
	}
	if err := d.DeleteSchedule(ctx, "missing"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("DeleteSchedule unknown = %v, want ErrScheduleNotFound", err)
	}
	if _, err := d.SetScheduleEnabled(ctx, "missing", false); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("SetScheduleEnabled unknown = %v, want ErrScheduleNotFound", err)
	}
	if err := d.DeleteSchedule(ctx, "missing"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("second DeleteSchedule = %v, want ErrScheduleNotFound", err)
	}
}

func TestSetScheduleEnabled(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	overdue := now.Add(-time.Minute)
	if err := d.CreateSchedule(ctx, Schedule{
		ID: "switch", Kind: ScheduleKindInterval, Spec: "5m",
		TargetCard: "card", NextRun: overdue, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	row, err := d.SetScheduleEnabled(ctx, "switch", false)
	if err != nil {
		t.Fatalf("SetScheduleEnabled(false): %v", err)
	}
	if row.ID != "switch" || row.Enabled {
		t.Fatalf("SetScheduleEnabled(false) returned %+v, want disabled row", row)
	}
	got, err := d.GetSchedule(ctx, "switch")
	if err != nil || got.Enabled {
		t.Fatalf("GetSchedule after disable = %+v, err = %v", got, err)
	}
	due, err := d.DueSchedules(now)
	if err != nil || len(due) != 0 {
		t.Fatalf("disabled schedule is due: %+v, err = %v", due, err)
	}

	row, err = d.SetScheduleEnabled(ctx, "switch", true)
	if err != nil {
		t.Fatalf("SetScheduleEnabled(true): %v", err)
	}
	if !row.Enabled {
		t.Fatalf("SetScheduleEnabled(true) returned %+v, want enabled row", row)
	}
	got, err = d.GetSchedule(ctx, "switch")
	if err != nil || !got.Enabled {
		t.Fatalf("GetSchedule after re-enable = %+v, err = %v", got, err)
	}
	due, err = d.DueSchedules(now)
	if err != nil || len(due) != 1 {
		t.Fatalf("re-enabled schedule not due: %+v, err = %v", due, err)
	}
}

func TestScheduleCRUDPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.db")
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour)
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.CreateSchedule(ctx, Schedule{
		ID: "keep", Kind: ScheduleKindCron, Spec: "0 3 * * *",
		TargetCard: "card-a", NextRun: next, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSchedule keep: %v", err)
	}
	if err := d.CreateSchedule(ctx, Schedule{
		ID: "remove", Kind: ScheduleKindWebhook, Spec: "push",
		TargetCard: "card-b", NextRun: next, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSchedule remove: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	list, err := reopened.ListSchedules(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSchedules after reopen = %+v, err = %v", list, err)
	}
	if list[0].ID != "keep" || list[1].ID != "remove" {
		t.Fatalf("ListSchedules order = %+v, want keep then remove", list)
	}
	if err := reopened.DeleteSchedule(ctx, "remove"); err != nil {
		t.Fatalf("DeleteSchedule after reopen: %v", err)
	}
	changed := Schedule{
		ID: "keep", Kind: ScheduleKindCron, Spec: "30 4 * * *",
		TargetCard: "card-c", NextRun: next.Add(time.Hour), Enabled: false,
	}
	if err := reopened.UpdateSchedule(ctx, changed); err != nil {
		t.Fatalf("UpdateSchedule after reopen: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	third, err := Open(path)
	if err != nil {
		t.Fatalf("third Open: %v", err)
	}
	defer third.Close()
	got, err := third.GetSchedule(ctx, "keep")
	if err != nil {
		t.Fatalf("GetSchedule after second reopen: %v", err)
	}
	assertScheduleEqual(t, got, changed)
	if _, err := third.GetSchedule(ctx, "remove"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("deleted schedule survived reopen: %v", err)
	}
}

func TestScheduleCRUDValidation(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour)
	bad := []Schedule{
		{Kind: ScheduleKindInterval, Spec: "1m", NextRun: next},
		{ID: "x", Kind: "timer", Spec: "1m", NextRun: next},
		{ID: "x", Kind: ScheduleKindInterval, Spec: "1m"},
	}
	for _, s := range bad {
		if err := d.CreateSchedule(ctx, s); err == nil {
			t.Fatalf("CreateSchedule(%+v) returned no error", s)
		}
		if err := d.UpdateSchedule(ctx, s); err == nil {
			t.Fatalf("UpdateSchedule(%+v) returned no error", s)
		}
	}
	for _, id := range []string{"", "  "} {
		if _, err := d.GetSchedule(ctx, id); err == nil {
			t.Fatalf("GetSchedule(%q) returned no error", id)
		}
		if err := d.DeleteSchedule(ctx, id); err == nil {
			t.Fatalf("DeleteSchedule(%q) returned no error", id)
		}
		if _, err := d.SetScheduleEnabled(ctx, id, true); err == nil {
			t.Fatalf("SetScheduleEnabled(%q) returned no error", id)
		}
	}
	list, err := d.ListSchedules(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("invalid writes wrote rows: %+v, err = %v", list, err)
	}
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

func TestScheduleKindMarkStoredAsWebhook(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour)
	mark := Schedule{
		ID: "mark", Kind: ScheduleKindMark, Spec: "push",
		TargetCard: "card", NextRun: next, Enabled: true,
	}

	if err := d.CreateSchedule(ctx, mark); err != nil {
		t.Fatalf("CreateSchedule(mark): %v", err)
	}
	got, err := d.GetSchedule(ctx, "mark")
	if err != nil {
		t.Fatalf("GetSchedule after create: %v", err)
	}
	if got.Kind != ScheduleKindWebhook {
		t.Fatalf("created kind = %q, want %q", got.Kind, ScheduleKindWebhook)
	}
	list, err := d.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list) != 1 || list[0].Kind != ScheduleKindWebhook {
		t.Fatalf("ListSchedules = %+v, want one webhook row", list)
	}

	saved := mark
	saved.ID = "mark-save"
	if err := d.SaveSchedule(saved); err != nil {
		t.Fatalf("SaveSchedule(mark): %v", err)
	}
	got, err = d.GetSchedule(ctx, saved.ID)
	if err != nil || got.Kind != ScheduleKindWebhook {
		t.Fatalf("saved mark read back as %+v, err=%v", got, err)
	}

	updated := mark
	updated.Kind = ScheduleKindMark
	updated.Spec = "updated"
	if err := d.UpdateSchedule(ctx, updated); err != nil {
		t.Fatalf("UpdateSchedule(mark): %v", err)
	}
	got, err = d.GetSchedule(ctx, "mark")
	if err != nil || got.Kind != ScheduleKindWebhook || got.Spec != "updated" {
		t.Fatalf("updated mark read back as %+v, err=%v", got, err)
	}
}

func TestUnknownScheduleKindIsInvalidInput(t *testing.T) {
	d := openScheduleStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour)
	bad := Schedule{ID: "x", Kind: "timer", Spec: "1m", NextRun: next, Enabled: true}

	for name, write := range map[string]func() error{
		"SaveSchedule":   func() error { return d.SaveSchedule(bad) },
		"CreateSchedule": func() error { return d.CreateSchedule(ctx, bad) },
		"UpdateSchedule": func() error { return d.UpdateSchedule(ctx, bad) },
	} {
		t.Run(name, func(t *testing.T) {
			err := write()
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Fatalf("%s error = %v, want AppError/%s", name, err, domain.CodeInvalidInput)
			}
		})
	}
}
