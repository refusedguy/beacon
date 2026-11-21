package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/tasks"
)

var daemonReminderNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// fakeReminderPorts stands in for the store adapter so the daemon callback can
// be tested without a database.
type fakeReminderPorts struct {
	tasks   []tasks.Task
	sent    []tasks.Reminder
	readErr error
}

func (f *fakeReminderPorts) OverdueTasks(context.Context, time.Time) ([]tasks.Task, error) {
	return f.tasks, f.readErr
}

func (f *fakeReminderPorts) SendReminder(_ context.Context, r tasks.Reminder) error {
	f.sent = append(f.sent, r)
	return nil
}

func TestDaemonReminderNotifySendsOnce(t *testing.T) {
	deadline := daemonReminderNow.Add(-time.Minute)
	ports := &fakeReminderPorts{tasks: []tasks.Task{
		{ID: "late", DeadlineAt: deadline},
		{ID: "early", DeadlineAt: daemonReminderNow.Add(time.Hour)},
	}}
	notify := daemonReminderNotify(tasks.NewNotifier(ports, func() time.Time { return daemonReminderNow }))

	if err := notify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ports.sent) != 1 || ports.sent[0].TaskID != "late" {
		t.Fatalf("sent = %+v, want only the overdue task", ports.sent)
	}

	if err := notify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ports.sent) != 1 {
		t.Fatalf("sent = %+v, want no second delivery", ports.sent)
	}
}

func TestDaemonReminderNotifyReportsReadError(t *testing.T) {
	boom := errors.New("store down")
	ports := &fakeReminderPorts{readErr: boom}
	notify := daemonReminderNotify(tasks.NewNotifier(ports, func() time.Time { return daemonReminderNow }))

	if err := notify(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}
