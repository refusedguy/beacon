package main

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

type watchRecordingControl struct {
	action string
	id     core.RunID
	pr     int
}

func (f *watchRecordingControl) Approve(context.Context, core.RunID, string) error {
	f.action = "approve"
	return nil
}

func (f *watchRecordingControl) Unblock(context.Context, core.RunID) error {
	f.action = "unblock"
	return nil
}

func (f *watchRecordingControl) Cancel(context.Context, core.RunID) error {
	f.action = "cancel"
	return nil
}

func (f *watchRecordingControl) Retry(context.Context, core.RunID) error {
	f.action = "retry"
	return nil
}

func (f *watchRecordingControl) Nudge(context.Context, core.RunID, string) error {
	f.action = "nudge"
	return nil
}

func (f *watchRecordingControl) WatchPR(_ context.Context, id core.RunID, pr int) error {
	f.action, f.id, f.pr = "watch", id, pr
	return nil
}

func TestValidateControlUsesOneVocabulary(t *testing.T) {
	for _, verb := range []string{"approve", "unblock", "cancel", "retry", "nudge", "watch"} {
		message := []string(nil)
		if verb == "nudge" {
			message = []string{"check the base branch"}
		}
		if err := validateControl(verb, "run-1", message...); err != nil {
			t.Errorf("action %q was rejected: %v", verb, err)
		}
	}
	for _, verb := range []string{"", "bind", "nudge-all", "APPROVE"} {
		if err := validateControl(verb, "run-1"); err == nil {
			t.Errorf("action %q was accepted", verb)
		}
	}
}

func TestApplyControlWithPRRoutesWatch(t *testing.T) {
	ctrl := &watchRecordingControl{}
	if err := applyControlWithPR(context.Background(), ctrl, "watch", "run-42", "", 77); err != nil {
		t.Fatal(err)
	}
	if ctrl.action != "watch" || ctrl.id != "run-42" || ctrl.pr != 77 {
		t.Fatalf("control = %+v, want watch on run-42 for PR 77", ctrl)
	}
	if err := applyControlWithPR(context.Background(), ctrl, "bind", "run-42", "", 77); err == nil {
		t.Fatal("unknown action was accepted")
	}
}
