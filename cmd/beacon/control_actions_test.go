package main

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
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

func (f *watchRecordingControl) Apply(_ context.Context, action core.ControlAction, args app.ControlArgs) error {
	f.action, f.id, f.pr = action.String(), args.RunID, args.PR
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

func (f *fakeControl) Apply(_ context.Context, action core.ControlAction, args app.ControlArgs) error {
	f.actions = append(f.actions, action.String())
	f.ids = append(f.ids, args.RunID)
	if action == core.ControlApprove {
		f.approveSHA = args.SHA
	}
	if action == core.ControlNudge {
		f.nudgeMessages = append(f.nudgeMessages, args.Message)
	}
	return f.err
}

// TestValidateControlUsesOneVocabulary derives its verbs from core, the same
// contract the TUI and plugin vocabularies are checked against in
// TestWireControlVocabulariesMatchCore.
func TestValidateControlUsesOneVocabulary(t *testing.T) {
	for _, verb := range core.ControlActionNames() {
		message := []string(nil)
		if verb == core.ControlNudge.String() {
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
	for _, action := range core.ControlActions {
		if action == core.ControlWatch {
			continue // watch uses the local watch port, covered in its own test
		}
		t.Run(action.String(), func(t *testing.T) {
			ctrl := &fakeControl{}
			if err := applyControlWithPR(context.Background(), ctrl, action.String(), "r1", "abc", 0, "guidance"); err != nil {
				t.Fatalf("applyControlWithPR(%q): %v", action, err)
			}
			if len(ctrl.actions) != 1 || ctrl.actions[0] != action.String() {
				t.Fatalf("actions = %v", ctrl.actions)
			}
			if len(ctrl.ids) != 1 || ctrl.ids[0] != "r1" {
				t.Fatalf("ids = %v", ctrl.ids)
			}
			if action == core.ControlApprove && ctrl.approveSHA != "abc" {
				t.Fatalf("approve sha = %q", ctrl.approveSHA)
			}
			if action == core.ControlNudge && (len(ctrl.nudgeMessages) != 1 || ctrl.nudgeMessages[0] != "guidance") {
				t.Fatalf("messages = %v", ctrl.nudgeMessages)
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

// recordingControlPort records the actions routed through the shared dispatch.
// It stands in for app.Control at the boundary-independent contract check.
type recordingControlPort struct {
	verbs []core.ControlAction
	args  []app.ControlArgs
}

func (p *recordingControlPort) record(verb core.ControlAction, args app.ControlArgs) error {
	p.verbs = append(p.verbs, verb)
	p.args = append(p.args, args)
	return nil
}

func (p *recordingControlPort) WatchPR(_ context.Context, id core.RunID, pr int) error {
	return p.record(core.ControlWatch, app.ControlArgs{RunID: id, PR: pr})
}

func (p *recordingControlPort) Approve(_ context.Context, id core.RunID, sha string) error {
	return p.record(core.ControlApprove, app.ControlArgs{RunID: id, SHA: sha})
}

func (p *recordingControlPort) Unblock(_ context.Context, id core.RunID) error {
	return p.record(core.ControlUnblock, app.ControlArgs{RunID: id})
}

func (p *recordingControlPort) Cancel(_ context.Context, id core.RunID) error {
	return p.record(core.ControlCancel, app.ControlArgs{RunID: id})
}

func (p *recordingControlPort) Retry(_ context.Context, id core.RunID) error {
	return p.record(core.ControlRetry, app.ControlArgs{RunID: id})
}

func (p *recordingControlPort) Nudge(_ context.Context, id core.RunID, message string) error {
	return p.record(core.ControlNudge, app.ControlArgs{RunID: id, Message: message})
}

// TestApplyControlRoutesEveryCoreVerb checks the one dispatch that all three
// boundaries share. The verb list comes from core, so a new verb fails here
// until the shared switch handles it.
func TestApplyControlRoutesEveryCoreVerb(t *testing.T) {
	port := &recordingControlPort{}
	args := app.ControlArgs{RunID: "r1", SHA: "abc", PR: 42, Message: "guidance"}
	for _, verb := range core.ControlActions {
		if err := app.ApplyControl(context.Background(), port, verb, args); err != nil {
			t.Fatalf("ApplyControl(%q): %v", verb, err)
		}
	}
	if len(port.verbs) != len(core.ControlActions) {
		t.Fatalf("verbs = %v, want %v", port.verbs, core.ControlActions)
	}
	for i, verb := range core.ControlActions {
		if port.verbs[i] != verb {
			t.Errorf("verb %d = %q, want %q", i, port.verbs[i], verb)
		}
		want := app.ControlArgs{RunID: "r1"}
		switch verb {
		case core.ControlApprove:
			want.SHA = "abc"
		case core.ControlWatch:
			want.PR = 42
		case core.ControlNudge:
			want.Message = "guidance"
		}
		if port.args[i] != want {
			t.Errorf("args %d = %+v, want %+v", i, port.args[i], want)
		}
	}
	if err := app.ApplyControl(context.Background(), port, core.ControlAction("bind"), app.ControlArgs{}); err == nil {
		t.Fatal("unknown action was accepted")
	}
}
