package core

import (
	"reflect"
	"testing"
)

func TestControlActionVocabulary(t *testing.T) {
	want := []ControlAction{
		ControlApprove, ControlUnblock, ControlCancel, ControlRetry, ControlNudge, ControlWatch,
	}
	for _, action := range want {
		t.Run(action.String(), func(t *testing.T) {
			got := ParseControlAction(action.String())
			if got != action || !got.Valid() {
				t.Fatalf("ParseControlAction(%q) = %q, valid = %v", action, got, got.Valid())
			}
			if got.String() != action.String() {
				t.Fatalf("String() = %q, want %q", got.String(), action)
			}
		})
	}
	if !reflect.DeepEqual(ControlActions[:], want) {
		t.Fatalf("ControlActions = %v, want %v", ControlActions, want)
	}
	if got, expected := ControlActionNames(), []string{
		"approve", "unblock", "cancel", "retry", "nudge", "watch",
	}; !reflect.DeepEqual(got, expected) {
		t.Fatalf("ControlActionNames() = %v, want %v", got, expected)
	}
}

func TestParseControlActionRejectsUnknownVerbs(t *testing.T) {
	for _, raw := range []string{"", "bind", "nudge-all", "APPROVE", " watch"} {
		if got := ParseControlAction(raw); got.Valid() {
			t.Errorf("ParseControlAction(%q) = %q was accepted", raw, got)
		}
	}
}
