package main

import (
	"context"
	"testing"
)

func TestActionVocabulary(t *testing.T) {
	for _, verb := range []string{"approve", "unblock", "cancel", "retry", "nudge", "watch"} {
		if a := ParseAction(verb); !a.Valid() || string(a) != verb {
			t.Errorf("ParseAction(%q) = %q, valid = %v", verb, a, a.Valid())
		}
	}
	for _, verb := range []string{"", "bind", "nudge-all", "APPROVE"} {
		if ParseAction(verb).Valid() {
			t.Errorf("action %q was accepted", verb)
		}
	}
}

func TestControlRejectsUnknownActionBeforeRequest(t *testing.T) {
	c := NewClient("http://127.0.0.1:7432", "tok")
	_, err := c.Control(context.Background(), ControlRequest{Action: ParseAction("bind"), RunID: "run-1"})
	if err == nil {
		t.Fatal("unknown action was accepted")
	}
}
