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

type fakeControl struct {
	actions       []string
	ids           []core.RunID
	approveSHA    string
	nudgeMessages []string
	err           error
}

func (f *fakeControl) Approve(_ context.Context, id core.RunID, sha string) error {
	f.actions = append(f.actions, "approve")
	f.ids = append(f.ids, id)
	f.approveSHA = sha
	return f.err
}

func (f *fakeControl) Unblock(_ context.Context, id core.RunID) error {
	f.actions = append(f.actions, "unblock")
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeControl) Cancel(_ context.Context, id core.RunID) error {
	f.actions = append(f.actions, "cancel")
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeControl) Retry(_ context.Context, id core.RunID) error {
	f.actions = append(f.actions, "retry")
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeControl) Nudge(_ context.Context, id core.RunID, message string) error {
	f.actions = append(f.actions, "nudge")
	f.ids = append(f.ids, id)
	f.nudgeMessages = append(f.nudgeMessages, message)
	return f.err
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
	if err := validateControl("approve", ""); err == nil {
		t.Error("empty run ID was accepted")
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

func TestApplyControlWithPRRoutesEveryAction(t *testing.T) {
	for _, action := range []string{"approve", "unblock", "cancel", "retry"} {
		t.Run(action, func(t *testing.T) {
			ctrl := &fakeControl{}
			if err := applyControlWithPR(context.Background(), ctrl, action, "r1", "abc", 0); err != nil {
				t.Fatalf("applyControlWithPR(context.Background(), ctrl, action, \"r1\", \"abc\", 0): %v", err)
			}
			if len(ctrl.actions) != 1 || ctrl.actions[0] != action {
				t.Fatalf("actions = %v", ctrl.actions)
			}
			if len(ctrl.ids) != 1 || ctrl.ids[0] != "r1" {
				t.Fatalf("ids = %v", ctrl.ids)
			}
			if action == "approve" && ctrl.approveSHA != "abc" {
				t.Fatalf("approve sha = %q", ctrl.approveSHA)
			}
		})
	}
}

func TestApplyControlWithPRForwardsNudgeMessage(t *testing.T) {
	ctrl := &fakeControl{}
	if err := applyControlWithPR(context.Background(), ctrl, "nudge", "r1", "", 0, "please rebase first"); err != nil {
		t.Fatalf("applyControlWithPR(context.Background(), ctrl, \"nudge\", \"r1\", \"\", 0, \"please rebase first\"): %v", err)
	}
	if len(ctrl.actions) != 1 || ctrl.actions[0] != "nudge" {
		t.Fatalf("actions = %v", ctrl.actions)
	}
	if len(ctrl.nudgeMessages) != 1 || ctrl.nudgeMessages[0] != "please rebase first" {
		t.Fatalf("messages = %v", ctrl.nudgeMessages)
	}
}
