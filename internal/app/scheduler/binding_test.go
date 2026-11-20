package scheduler

import (
	"errors"
	"testing"
)

func newBinding(scheduleID string, typ ScheduleType, targetID string) Binding {
	return Binding{ScheduleID: scheduleID, Type: typ, TargetID: targetID}
}

func sampleBinding() Binding {
	return newBinding("sched-1", ScheduleInterval, "ses_1")
}

func TestBindStoresBinding(t *testing.T) {
	bs := NewBindings()
	want := sampleBinding()

	created, err := bs.Bind(want)
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	if !created {
		t.Fatal("Bind() created = false, want true for a new binding")
	}
	if got := bs.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
	got, ok := bs.Get("sched-1")
	if !ok {
		t.Fatal("Get() ok = false, want true after Bind")
	}
	if got != want {
		t.Fatalf("Get() = %+v, want %+v", got, want)
	}
	target, bound := bs.TargetOf("sched-1")
	if !bound || target != "ses_1" {
		t.Fatalf("TargetOf() = (%q, %t), want (%q, true)", target, bound, "ses_1")
	}
}

func TestBindRepeatedDoesNotDuplicate(t *testing.T) {
	bs := NewBindings()
	want := sampleBinding()

	if _, err := bs.Bind(want); err != nil {
		t.Fatalf("first Bind() error = %v", err)
	}
	created, err := bs.Bind(want)
	if err != nil {
		t.Fatalf("second Bind() error = %v, want nil for an identical repeat", err)
	}
	if created {
		t.Fatal("second Bind() created = true, want false for an identical repeat")
	}
	if got := bs.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1 after a repeated Bind", got)
	}
	got, _ := bs.Get("sched-1")
	if got != want {
		t.Fatalf("stored binding changed on repeat: %+v, want %+v", got, want)
	}
}

func TestUnbindRemovesBinding(t *testing.T) {
	bs := NewBindings()
	if _, err := bs.Bind(sampleBinding()); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	if !bs.Unbind("sched-1") {
		t.Fatal("Unbind() = false, want true for an existing binding")
	}
	if got := bs.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0 after Unbind", got)
	}
	if _, ok := bs.Get("sched-1"); ok {
		t.Fatal("Get() ok = true, want false after Unbind")
	}
	if target, bound := bs.TargetOf("sched-1"); bound || target != "" {
		t.Fatalf("TargetOf() = (%q, %t), want (\"\", false) after Unbind", target, bound)
	}
}

func TestUnbindUnknownIsNoOp(t *testing.T) {
	bs := NewBindings()
	if bs.Unbind("missing") {
		t.Fatal("Unbind() = true, want false for an unknown schedule")
	}
	if got := bs.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
}

func TestBindConflictingTarget(t *testing.T) {
	bs := NewBindings()
	first := sampleBinding()
	if _, err := bs.Bind(first); err != nil {
		t.Fatalf("first Bind() error = %v", err)
	}

	conflict := newBinding("sched-1", ScheduleInterval, "ses_2")
	created, err := bs.Bind(conflict)
	if created {
		t.Fatal("conflicting Bind() created = true, want false")
	}
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("conflicting Bind() error = %v, want ErrBindingConflict", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error type = %T, want *ConflictError", err)
	}
	if ce.Existing != first || ce.Requested != conflict {
		t.Fatalf("ConflictError = %+v, want existing %+v and requested %+v", ce, first, conflict)
	}
	got, _ := bs.Get("sched-1")
	if got != first {
		t.Fatalf("stored binding changed after conflict: %+v, want %+v", got, first)
	}
}

func TestBindConflictingType(t *testing.T) {
	bs := NewBindings()
	first := sampleBinding()
	if _, err := bs.Bind(first); err != nil {
		t.Fatalf("first Bind() error = %v", err)
	}

	conflict := newBinding("sched-1", ScheduleFixed, "ses_1")
	_, err := bs.Bind(conflict)
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("Bind() with a different type error = %v, want ErrBindingConflict", err)
	}
}

func TestEmptyBindings(t *testing.T) {
	bs := NewBindings()
	if got := bs.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
	list := bs.List()
	if list == nil {
		t.Fatal("List() = nil, want an empty non-nil slice")
	}
	if len(list) != 0 {
		t.Fatalf("List() length = %d, want 0", len(list))
	}
	if _, ok := bs.Get("sched-1"); ok {
		t.Fatal("Get() ok = true on an empty set, want false")
	}
	if target, bound := bs.TargetOf("sched-1"); bound || target != "" {
		t.Fatalf("TargetOf() = (%q, %t), want (\"\", false) on an empty set", target, bound)
	}
}

func TestZeroValueBindingsIsUsable(t *testing.T) {
	var bs Bindings
	if _, err := bs.Bind(sampleBinding()); err != nil {
		t.Fatalf("Bind() on zero value error = %v", err)
	}
	if got := bs.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestMultipleSchedulesMayShareATarget(t *testing.T) {
	bs := NewBindings()
	first := newBinding("sched-1", ScheduleInterval, "ses_1")
	second := newBinding("sched-2", ScheduleWebhook, "ses_1")
	for _, b := range []Binding{first, second} {
		if _, err := bs.Bind(b); err != nil {
			t.Fatalf("Bind(%s) error = %v", b.ScheduleID, err)
		}
	}
	if got := bs.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	for _, b := range []Binding{first, second} {
		target, bound := bs.TargetOf(b.ScheduleID)
		if !bound || target != "ses_1" {
			t.Fatalf("TargetOf(%s) = (%q, %t), want (%q, true)", b.ScheduleID, target, bound, "ses_1")
		}
	}
}

func TestListIsSortedAndCopied(t *testing.T) {
	bs := NewBindings()
	b1 := newBinding("sched-b", ScheduleFixed, "ses_1")
	b2 := newBinding("sched-a", ScheduleInterval, "ses_2")
	for _, b := range []Binding{b1, b2} {
		if _, err := bs.Bind(b); err != nil {
			t.Fatalf("Bind() error = %v", err)
		}
	}

	list := bs.List()
	if len(list) != 2 {
		t.Fatalf("List() length = %d, want 2", len(list))
	}
	if list[0].ScheduleID != "sched-a" || list[1].ScheduleID != "sched-b" {
		t.Fatalf("List() order = [%s, %s], want [sched-a, sched-b]", list[0].ScheduleID, list[1].ScheduleID)
	}

	list[0].TargetID = "mutated"
	got, _ := bs.Get("sched-a")
	if got.TargetID != "ses_2" {
		t.Fatalf("stored binding changed through List(): %q, want %q", got.TargetID, "ses_2")
	}
}

func TestBindRejectsInvalidBinding(t *testing.T) {
	cases := []struct {
		name string
		b    Binding
	}{
		{"empty schedule id", newBinding("", ScheduleInterval, "ses_1")},
		{"unknown type", newBinding("sched-1", ScheduleType("daily"), "ses_1")},
		{"empty target id", newBinding("sched-1", ScheduleInterval, "")},
		{"zero value", Binding{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := NewBindings()
			created, err := bs.Bind(tc.b)
			if created {
				t.Fatal("Bind() created = true, want false for an invalid binding")
			}
			if !errors.Is(err, ErrInvalidBinding) {
				t.Fatalf("Bind() error = %v, want ErrInvalidBinding", err)
			}
			if got := bs.Len(); got != 0 {
				t.Fatalf("Len() = %d, want 0 after a rejected Bind", got)
			}
		})
	}
}

func TestScheduleTypeValidAndParse(t *testing.T) {
	for _, typ := range []ScheduleType{ScheduleInterval, ScheduleFixed, ScheduleWebhook} {
		if !typ.Valid() {
			t.Fatalf("%q.Valid() = false, want true", typ)
		}
		parsed, ok := ParseScheduleType("  " + string(typ) + "  ")
		if !ok || parsed != typ {
			t.Fatalf("ParseScheduleType(%q) = (%q, %t), want (%q, true)", string(typ), parsed, ok, typ)
		}
	}
	if (ScheduleType("")).Valid() {
		t.Fatal("zero ScheduleType.Valid() = true, want false")
	}
	if _, ok := ParseScheduleType("daily"); ok {
		t.Fatal("ParseScheduleType(\"daily\") ok = true, want false")
	}
}
