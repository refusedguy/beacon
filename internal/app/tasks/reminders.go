// Package tasks turns task deadlines into reminders. The decision is pure:
// Plan reads no clock, store or network. The caller supplies "now", the task
// snapshot and the ledger of reminders already sent, so equal inputs always
// give an equal plan. The port and its data types live in internal/infra and
// are aliased here, so the store adapter can implement them without an
// internal/infra -> internal/app edge.
package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// ReminderReasonDeadline is the reason carried by a deadline reminder. It is
// the only reason this package emits, so a consumer can filter on it without
// knowing the task's internals.
const ReminderReasonDeadline = "deadline_overdue"

// Task is one deadline-carrying task. ID is the stable task identifier and
// DeadlineAt is the absolute instant after which the task is overdue. A zero
// deadline means the task has no deadline and never produces a reminder. The
// type is an alias to the infra shape so the store adapter can use it directly.
type Task = taskstore.ReminderTask

// Reminder is one reminder generated for an overdue task. At is the injected
// "now" of the pass that produced it. ReminderKey(TaskID, DeadlineAt) is the
// deduplication key, so a new deadline produces a fresh reminder while a
// repeat of the same pass does not. It aliases the infra delivery shape.
type Reminder = taskstore.Reminder

// ReminderKey builds the deduplication key for a task and the deadline a
// reminder covers. It delegates to the infra definition so the store adapter
// and this package never drift apart.
func ReminderKey(taskID string, deadline time.Time) string {
	return taskstore.ReminderKey(taskID, deadline)
}

// reminderKeyOf returns the key of a reminder already built from a task.
func reminderKeyOf(r Reminder) string {
	return ReminderKey(r.TaskID, r.DeadlineAt)
}

// ReminderLedger is the set of reminders already emitted, keyed by task and
// the deadline each reminder covered. The zero value is an empty ledger. The
// type is a snapshot: Added returns a new ledger and never mutates the old one.
type ReminderLedger struct {
	keys map[string]struct{}
}

// Has reports whether a reminder for taskID and deadline is in the ledger.
func (l ReminderLedger) Has(taskID string, deadline time.Time) bool {
	_, ok := l.keys[ReminderKey(taskID, deadline)]
	return ok
}

// Added returns a new ledger containing the keys of l and of every reminder.
// It leaves l unchanged, so passing an old ledger to Plan still sees the
// reminders that existed when it was built.
func (l ReminderLedger) Added(reminders []Reminder) ReminderLedger {
	out := ReminderLedger{keys: make(map[string]struct{}, len(l.keys)+len(reminders))}
	for k := range l.keys {
		out.keys[k] = struct{}{}
	}
	for _, r := range reminders {
		out.keys[reminderKeyOf(r)] = struct{}{}
	}
	return out
}

// Plan builds reminders for tasks whose deadline is at or before now and that
// are not already in sent. It returns the reminders in input order and a new
// ledger that includes them. Plan reads no clock and touches no I/O, so a
// caller that feeds the returned ledger back on the next pass never sends the
// same reminder twice. A task with a zero or future deadline is skipped.
func Plan(now time.Time, tasks []Task, sent ReminderLedger) ([]Reminder, ReminderLedger) {
	reminders := make([]Reminder, 0, len(tasks))
	for _, task := range tasks {
		if task.DeadlineAt.IsZero() || task.DeadlineAt.After(now) {
			continue
		}
		if sent.Has(task.ID, task.DeadlineAt) {
			continue
		}
		reminders = append(reminders, Reminder{
			TaskID:     task.ID,
			DeadlineAt: task.DeadlineAt,
			At:         now,
			Reason:     ReminderReasonDeadline,
		})
	}
	return reminders, sent.Added(reminders)
}

// ReminderPorts is the I/O boundary of reminder generation. An adapter owns
// the store and the delivery channel. SendReminder must be idempotent by task
// id and deadline, because a restart loses this package's in-memory ledger and
// can replay a reminder whose delivery was never confirmed. The interface is
// declared in internal/infra and aliased here to respect the layer matrix.
type ReminderPorts = taskstore.ReminderPorts

// ReminderFailure records one reminder that the port could not deliver.
type ReminderFailure struct {
	Reminder Reminder
	Err      error
}

// NotifyReport is the outcome of one Notify pass. Planned counts reminders
// Plan produced, Sent counts those the port accepted and Failed holds the
// rest. A failed reminder stays out of the ledger, so a later pass retries it.
type NotifyReport struct {
	Candidates int
	Planned    int
	Sent       int
	Failed     []ReminderFailure
}

// ErrNilReminderPorts is reported when Notify has no port to read or deliver.
var ErrNilReminderPorts = errors.New("tasks: nil reminder ports")

// Notifier applies the pure plan through ReminderPorts. It keeps the ledger
// across passes, so repeated calls on one Notifier do not resend a reminder.
// Do not share one Notifier across goroutines; give each loop its own.
type Notifier struct {
	ports ReminderPorts
	now   func() time.Time
	sent  ReminderLedger
}

// NewNotifier builds a Notifier over ports. now is injected so tests and
// callers control the instant; a nil now falls back to time.Now.
func NewNotifier(ports ReminderPorts, now func() time.Time) *Notifier {
	if now == nil {
		now = time.Now
	}
	return &Notifier{ports: ports, now: now}
}

// Notify reads overdue tasks at the injected now, plans reminders and sends
// each one through the port. It records only the reminders the port accepted,
// so a failed delivery is retried on the next pass. A port read error stops the
// pass and leaves the ledger unchanged. A delivery error is reported and does
// not block the remaining reminders in the batch.
func (n *Notifier) Notify(ctx context.Context) (NotifyReport, error) {
	if n == nil || n.ports == nil {
		return NotifyReport{}, ErrNilReminderPorts
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := n.now()
	tasks, err := n.ports.OverdueTasks(ctx, now)
	if err != nil {
		return NotifyReport{}, err
	}

	reminders, _ := Plan(now, tasks, n.sent)
	report := NotifyReport{Candidates: len(tasks), Planned: len(reminders)}

	sentNow := make([]Reminder, 0, len(reminders))
	for _, reminder := range reminders {
		if err := n.ports.SendReminder(ctx, reminder); err != nil {
			report.Failed = append(report.Failed, ReminderFailure{Reminder: reminder, Err: err})
			continue
		}
		sentNow = append(sentNow, reminder)
		report.Sent++
	}
	n.sent = n.sent.Added(sentNow)
	return report, nil
}
