package tasks

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// fixedNow is a zone-stable instant without a monotonic reading.
var fixedNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

var _ ReminderPorts = (*fakeReminderPorts)(nil)

// fakeReminderPorts records what Notify reads and sends. It owns no dedup: the
// Notifier's ledger is what the repeat tests exercise.
type fakeReminderPorts struct {
	tasks []Task

	atCalls []time.Time
	sent    []Reminder

	readErr error
	sendErr map[string]error
}

func (f *fakeReminderPorts) OverdueTasks(_ context.Context, at time.Time) ([]Task, error) {
	f.atCalls = append(f.atCalls, at)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.tasks, nil
}

func (f *fakeReminderPorts) SendReminder(_ context.Context, reminder Reminder) error {
	f.sent = append(f.sent, reminder)
	if f.sendErr != nil {
		return f.sendErr[reminderKeyOf(reminder)]
	}
	return nil
}

func TestPlanOverdueTask(t *testing.T) {
	deadline := fixedNow.Add(-time.Minute)
	tasks := []Task{{ID: "T3.65", DeadlineAt: deadline}}

	reminders, ledger := Plan(fixedNow, tasks, ReminderLedger{})

	if len(reminders) != 1 {
		t.Fatalf("reminders = %+v, want one overdue reminder", reminders)
	}
	want := Reminder{TaskID: "T3.65", DeadlineAt: deadline, At: fixedNow, Reason: ReminderReasonDeadline}
	if reminders[0] != want {
		t.Fatalf("reminder = %+v, want %+v", reminders[0], want)
	}
	if !ledger.Has("T3.65", deadline) {
		t.Fatal("ledger does not record the sent reminder")
	}
}

func TestPlanNotOverdueTask(t *testing.T) {
	tasks := []Task{
		{ID: "future", DeadlineAt: fixedNow.Add(time.Minute)},
		{ID: "no-deadline"},
	}

	reminders, ledger := Plan(fixedNow, tasks, ReminderLedger{})

	if len(reminders) != 0 {
		t.Fatalf("reminders = %+v, want none for a future or zero deadline", reminders)
	}
	if ledger.Has("future", fixedNow.Add(time.Minute)) || ledger.Has("no-deadline", time.Time{}) {
		t.Fatal("ledger changed for tasks with no reminder")
	}
}

func TestPlanRepeatDoesNotDuplicate(t *testing.T) {
	deadline := fixedNow.Add(-time.Hour)
	tasks := []Task{{ID: "T3.65", DeadlineAt: deadline}}

	first, ledger := Plan(fixedNow, tasks, ReminderLedger{})
	if len(first) != 1 {
		t.Fatalf("first pass = %+v, want one reminder", first)
	}

	second, ledger2 := Plan(fixedNow, tasks, ledger)
	if len(second) != 0 {
		t.Fatalf("repeat pass = %+v, want no duplicate reminder", second)
	}
	if !reflect.DeepEqual(ledger2, ledger) {
		t.Fatalf("repeat changed the ledger: got %+v, want %+v", ledger2, ledger)
	}

	// A new deadline is a new reminder, not a duplicate of the old one.
	extended := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	fresh, _ := Plan(fixedNow.Add(2*time.Hour), []Task{{ID: "T3.65", DeadlineAt: extended}}, ledger)
	if len(fresh) != 1 || fresh[0].DeadlineAt != extended {
		t.Fatalf("new deadline = %+v, want one reminder for the extended deadline", fresh)
	}
}

func TestPlanEmptyState(t *testing.T) {
	reminders, ledger := Plan(fixedNow, nil, ReminderLedger{})

	if len(reminders) != 0 {
		t.Fatalf("reminders = %+v, want none for empty state", reminders)
	}
	if ledger.Has("T3.65", fixedNow) {
		t.Fatal("empty state produced a ledger entry")
	}
}

func TestPlanIsPure(t *testing.T) {
	deadline := fixedNow.Add(-time.Minute)
	tasks := []Task{{ID: "T3.65", DeadlineAt: deadline}}
	before := ReminderLedger{}.Added([]Reminder{{TaskID: "other", DeadlineAt: deadline}})

	_, after := Plan(fixedNow, tasks, before)

	if tasks[0] != (Task{ID: "T3.65", DeadlineAt: deadline}) {
		t.Fatalf("Plan mutated its tasks: %+v", tasks)
	}
	if before.Has("T3.65", deadline) || !after.Has("T3.65", deadline) {
		t.Fatalf("ledgers = before %+v, after %+v; want only after to hold the new key", before, after)
	}
}

func TestNotifierSendsOverdueOnce(t *testing.T) {
	deadline := fixedNow.Add(-time.Minute)
	ports := &fakeReminderPorts{tasks: []Task{
		{ID: "late", DeadlineAt: deadline},
		{ID: "early", DeadlineAt: fixedNow.Add(time.Hour)},
	}}
	n := NewNotifier(ports, func() time.Time { return fixedNow })

	first, err := n.Notify(context.Background())
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if first.Candidates != 2 || first.Planned != 1 || first.Sent != 1 {
		t.Fatalf("first report = %+v, want 2 candidates, 1 planned, 1 sent", first)
	}
	if len(ports.sent) != 1 || ports.sent[0].TaskID != "late" {
		t.Fatalf("sent = %+v, want only the overdue task", ports.sent)
	}
	if !reflect.DeepEqual(ports.atCalls, []time.Time{fixedNow}) {
		t.Fatalf("OverdueTasks calls = %v, want the injected now", ports.atCalls)
	}

	second, err := n.Notify(context.Background())
	if err != nil {
		t.Fatalf("repeat Notify: %v", err)
	}
	if second.Planned != 0 || second.Sent != 0 || len(ports.sent) != 1 {
		t.Fatalf("repeat report = %+v, sent = %+v; want no second delivery", second, ports.sent)
	}
}

func TestNotifierEmptyState(t *testing.T) {
	ports := &fakeReminderPorts{}
	n := NewNotifier(ports, func() time.Time { return fixedNow })

	report, err := n.Notify(context.Background())
	if err != nil {
		t.Fatalf("Notify on empty state: %v", err)
	}
	if report.Candidates != 0 || report.Planned != 0 || report.Sent != 0 {
		t.Fatalf("report = %+v, want all zeros", report)
	}
	if len(ports.sent) != 0 {
		t.Fatalf("empty state sent reminders: %+v", ports.sent)
	}
	if len(ports.atCalls) != 1 {
		t.Fatalf("OverdueTasks calls = %v, want one read", ports.atCalls)
	}
}

func TestNotifierRetriesFailedDelivery(t *testing.T) {
	deadline := fixedNow.Add(-time.Minute)
	reminder := Reminder{TaskID: "late", DeadlineAt: deadline}
	boom := errors.New("delivery down")
	ports := &fakeReminderPorts{
		tasks:   []Task{{ID: "late", DeadlineAt: deadline}},
		sendErr: map[string]error{reminderKeyOf(reminder): boom},
	}
	n := NewNotifier(ports, func() time.Time { return fixedNow })

	failed, err := n.Notify(context.Background())
	if err != nil {
		t.Fatalf("Notify with a delivery failure: %v", err)
	}
	if failed.Sent != 0 || len(failed.Failed) != 1 || !errors.Is(failed.Failed[0].Err, boom) {
		t.Fatalf("report = %+v, want one failed delivery", failed)
	}

	ports.sendErr = nil
	retried, err := n.Notify(context.Background())
	if err != nil {
		t.Fatalf("retry Notify: %v", err)
	}
	if retried.Sent != 1 || len(ports.sent) != 2 {
		t.Fatalf("retry report = %+v, sent = %+v; want one successful retry", retried, ports.sent)
	}
}

func TestNotifierReadErrorStopsPass(t *testing.T) {
	boom := errors.New("store down")
	ports := &fakeReminderPorts{readErr: boom}
	n := NewNotifier(ports, func() time.Time { return fixedNow })

	_, err := n.Notify(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if len(ports.sent) != 0 {
		t.Fatalf("read error still sent reminders: %+v", ports.sent)
	}
}

func TestNotifierNilPorts(t *testing.T) {
	n := NewNotifier(nil, nil)

	_, err := n.Notify(context.Background())
	if !errors.Is(err, ErrNilReminderPorts) {
		t.Fatalf("error = %v, want %v", err, ErrNilReminderPorts)
	}
}
