package main

import (
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// refusedModel loads a run, sends a control the daemon refuses, and delivers
// the actionMsg. The returned model holds a refusal in its dedicated field.
func refusedModel(t *testing.T) (model, string) {
	t.Helper()
	const reason = "run is closed"
	f := newControlFixture(t, "run-refusal", `{"ok":false,"action":"approve","run_id":"run-refusal","applied":false,"reason":"`+reason+`"}`)
	f.status = http.StatusConflict
	m := f.loaded(t)

	msg, ok := m.apply(ActionApprove)().(actionMsg)
	if !ok {
		t.Fatal("apply returned no actionMsg")
	}
	next, followUp := m.Update(msg)
	if followUp != nil {
		t.Fatalf("refusal returned an unexpected command: %v", followUp)
	}
	refused := next.(model)
	if refused.refusal == "" {
		t.Fatal("refusal was not stored on the model")
	}
	return refused, reason
}

// TestRefusalSurvivesRefreshes is the task check. A refusal reaches the panel,
// then several refresh messages replace the status line. The stored refusal
// must stay on screen while the run list refreshes underneath it.
func TestRefusalSurvivesRefreshes(t *testing.T) {
	m, reason := refusedModel(t)
	if view := m.View(); !strings.Contains(view, reason) || !strings.Contains(view, "refused") {
		t.Fatalf("initial refusal view is missing the reason:\n%s", view)
	}

	cur := m
	for i := 0; i < 4; i++ {
		// A quiet connection still sends notifications, and each one produces
		// a runs read whose runsMsg overwrites status with the loaded count.
		notified, cmd := cur.Update(refreshMsg{event: Event{ID: int64(i + 1), Type: pingEvent}})
		if cmd == nil {
			t.Fatalf("refresh %d scheduled no runs read", i)
		}
		if notified.(model).refusal == "" {
			t.Fatalf("refresh %d cleared the stored refusal", i)
		}

		raw := notified.(model).reload()()
		msg, ok := raw.(runsMsg)
		if !ok || msg.err != nil {
			t.Fatalf("refresh %d = %T %v, want runsMsg", i, raw, msg.err)
		}
		updated, followUp := notified.Update(msg)
		if followUp != nil {
			t.Fatalf("refresh %d returned an unexpected command: %v", i, followUp)
		}
		cur = updated.(model)

		view := cur.View()
		if !strings.Contains(view, reason) {
			t.Fatalf("refresh %d dropped the refusal reason %q:\n%s", i, reason, view)
		}
		if !strings.Contains(view, "refused") {
			t.Fatalf("refresh %d dropped the refusal status:\n%s", i, view)
		}
		if !strings.Contains(view, "loaded 1 run(s)") {
			t.Fatalf("refresh %d did not refresh the run list:\n%s", i, view)
		}
	}
	if cur.refusal == "" {
		t.Fatal("refusals field was cleared by refreshes")
	}
}

// TestRefusalClearsOnSuccess proves a successful control removes the stored
// refusal, so an old reason cannot outlive the action that replaced it.
func TestRefusalClearsOnSuccess(t *testing.T) {
	const reason = "run is closed"
	f := newControlFixture(t, "run-refusal", `{"ok":false,"action":"approve","run_id":"run-refusal","applied":false,"reason":"`+reason+`"}`)
	f.status = http.StatusConflict
	m := f.loaded(t)

	refused := m.apply(ActionApprove)().(actionMsg)
	afterRefusal, _ := m.Update(refused)
	m = afterRefusal.(model)
	if !strings.Contains(m.View(), reason) {
		t.Fatalf("refusal view missing reason:\n%s", m.View())
	}

	f.status = 0
	f.control = `{"ok":true,"action":"approve","run_id":"run-refusal","applied":true}`
	succeeded := m.apply(ActionApprove)().(actionMsg)
	next, cmd := m.Update(succeeded)
	if cmd == nil {
		t.Fatal("successful control must schedule a reload")
	}
	cleared := next.(model)
	if cleared.refusal != "" {
		t.Fatalf("refusal field after success = %q, want empty", cleared.refusal)
	}
	if view := cleared.View(); strings.Contains(view, reason) || strings.Contains(view, "refused") {
		t.Fatalf("success view still shows the old refusal:\n%s", view)
	}
}

// TestRefusalClearsOnNextActionKey proves the operator taking the next action
// dismisses the previous refusal before the new result arrives.
func TestRefusalClearsOnNextActionKey(t *testing.T) {
	m, reason := refusedModel(t)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd == nil {
		t.Fatal("action key must send a control command")
	}
	cleared := next.(model)
	if cleared.refusal != "" {
		t.Fatalf("refusal field after action key = %q, want empty", cleared.refusal)
	}
	if view := cleared.View(); strings.Contains(view, reason) {
		t.Fatalf("view still shows the old reason %q:\n%s", reason, view)
	}
}
