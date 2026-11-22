// This file holds the store side of deadline reminders. It declares the port
// types in the inner layer so the adapter never imports internal/app. The app
// notifier aliases these types, and cmd wires the two together.
package taskstore

import (
	"context"
	"sync"
	"time"
)

// ReminderTask is one deadline-carrying task read from storage. A zero
// DeadlineAt means the task has no deadline.
type ReminderTask struct {
	ID         string
	DeadlineAt time.Time
}

// Reminder is one deadline reminder produced for an overdue task.
type Reminder struct {
	TaskID     string
	DeadlineAt time.Time
	At         time.Time
	Reason     string
}

// ReminderPorts is the I/O boundary of reminder generation. It is declared
// here, in the inner layer, so this package can implement it without an
// internal/infra -> internal/app edge. The app notifier aliases this interface
// and its two data types.
type ReminderPorts interface {
	// OverdueTasks returns tasks whose deadline is at or before at.
	OverdueTasks(ctx context.Context, at time.Time) ([]ReminderTask, error)
	// SendReminder delivers one reminder. An implementation suppresses a
	// second delivery for the same task id and deadline.
	SendReminder(ctx context.Context, reminder Reminder) error
}

// ReminderKey returns the deduplication key of a task and the deadline a
// reminder covers. UTC and a fixed layout keep the key stable across zones and
// monotonic readings. The app notifier delegates to this function so both
// sides use the same key.
func ReminderKey(taskID string, deadline time.Time) string {
	return taskID + "\x00" + deadline.UTC().Format(time.RFC3339Nano)
}

// ReminderLoad reads tasks whose deadline is at or before at. The daemon
// supplies a loader backed by its store handle.
type ReminderLoad func(ctx context.Context, at time.Time) ([]ReminderTask, error)

// ReminderDeliver sends one reminder to its channel. The daemon supplies that
// channel; this adapter owns only deduplication.
type ReminderDeliver func(ctx context.Context, reminder Reminder) error

// ReminderStore is the store adapter behind ReminderPorts. It suppresses a
// second delivery for the same task and deadline with an in-process key set.
// A restart clears the set, so the app notifier keeps its own ledger as well.
type ReminderStore struct {
	load    ReminderLoad
	deliver ReminderDeliver

	mu       sync.Mutex
	sent     map[string]struct{}
	inflight map[string]struct{}
}

var _ ReminderPorts = (*ReminderStore)(nil)

// NewReminderStore builds the adapter. A nil loader returns no tasks; a nil
// deliverer accepts reminders without sending them.
func NewReminderStore(load ReminderLoad, deliver ReminderDeliver) *ReminderStore {
	return &ReminderStore{
		load:     load,
		deliver:  deliver,
		sent:     make(map[string]struct{}),
		inflight: make(map[string]struct{}),
	}
}

// OverdueTasks reads the current overdue set through the injected loader.
func (s *ReminderStore) OverdueTasks(ctx context.Context, at time.Time) ([]ReminderTask, error) {
	if s == nil || s.load == nil {
		return nil, nil
	}
	return s.load(ctx, at)
}

// SendReminder delivers a reminder once per task and deadline. The key check
// and the in-flight reservation share the lock, so two concurrent passes send
// the reminder once. Delivery runs without the lock: a slow channel cannot
// block other reminders, and a callback that reaches the same store cannot
// deadlock. A failed delivery releases the reservation and leaves the key
// unmarked, so the app notifier retries on its next pass.
func (s *ReminderStore) SendReminder(ctx context.Context, reminder Reminder) error {
	if s == nil {
		return nil
	}
	key := ReminderKey(reminder.TaskID, reminder.DeadlineAt)

	s.mu.Lock()
	if _, ok := s.sent[key]; ok {
		s.mu.Unlock()
		return nil
	}
	if _, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		return nil
	}
	s.inflight[key] = struct{}{}
	s.mu.Unlock()

	var err error
	if s.deliver != nil {
		err = s.deliver(ctx, reminder)
	}

	s.mu.Lock()
	delete(s.inflight, key)
	if err == nil {
		s.sent[key] = struct{}{}
	}
	s.mu.Unlock()
	return err
}
