package taskstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

var reminderDeadline = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestReminderStoreOverdueTasks(t *testing.T) {
	loadCalls := 0
	s := NewReminderStore(func(_ context.Context, got time.Time) ([]ReminderTask, error) {
		loadCalls++
		if !got.Equal(reminderDeadline) {
			t.Fatalf("load at = %v, want %v", got, reminderDeadline)
		}
		return []ReminderTask{{ID: "T3.65", DeadlineAt: reminderDeadline}}, nil
	}, nil)

	tasks, err := s.OverdueTasks(context.Background(), reminderDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T3.65" {
		t.Fatalf("tasks = %+v, want one task T3.65", tasks)
	}
	if loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls)
	}
}

func TestReminderStoreSuppressesDuplicateDelivery(t *testing.T) {
	deliveries := 0
	s := NewReminderStore(nil, func(_ context.Context, _ Reminder) error {
		deliveries++
		return nil
	})
	r := Reminder{TaskID: "T3.65", DeadlineAt: reminderDeadline, Reason: "deadline_overdue"}

	for i := 0; i < 3; i++ {
		if err := s.SendReminder(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestReminderStoreRetriesFailedDelivery(t *testing.T) {
	boom := errors.New("channel down")
	deliveries := 0
	s := NewReminderStore(nil, func(_ context.Context, _ Reminder) error {
		deliveries++
		if deliveries == 1 {
			return boom
		}
		return nil
	})
	r := Reminder{TaskID: "T3.65", DeadlineAt: reminderDeadline}

	if err := s.SendReminder(context.Background(), r); !errors.Is(err, boom) {
		t.Fatalf("first error = %v, want %v", err, boom)
	}
	if err := s.SendReminder(context.Background(), r); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if deliveries != 2 {
		t.Fatalf("deliveries = %d, want 2", deliveries)
	}
}

func TestReminderKeyStableAcrossZones(t *testing.T) {
	utc := reminderDeadline
	other := reminderDeadline.In(time.FixedZone("plus2", 2*60*60))
	if ReminderKey("T3.65", utc) != ReminderKey("T3.65", other) {
		t.Fatal("the key changed with the deadline zone")
	}
	if ReminderKey("T3.65", utc) == ReminderKey("T3.66", utc) {
		t.Fatal("two tasks share a key")
	}
}
