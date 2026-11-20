package wake

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHooksApplyOnlyToPrompt(t *testing.T) {
	cases := map[InputKind]bool{
		InputPrompt:    true,
		InputSynthetic: false,
		InputShell:     false,
		InputCompact:   false,
		InputMove:      false,
	}
	for kind, want := range cases {
		if got := HooksApply(kind); got != want {
			t.Errorf("HooksApply(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestDefaultDelivery(t *testing.T) {
	for _, kind := range []InputKind{InputPrompt, InputSynthetic, InputCompact} {
		if got := DefaultDelivery(kind); got != DeliverySteer {
			t.Errorf("DefaultDelivery(%q) = %q, want steer", kind, got)
		}
	}
}

func TestPlanAdmissionSteerAtStepBoundary(t *testing.T) {
	active := PlanAdmission(DeliverySteer, ExecActive)
	if !active.SchedulesLoop || !active.AtStepBoundary || active.Parked {
		t.Fatalf("steer/active = %+v, want scheduled at step boundary", active)
	}

	idle := PlanAdmission(DeliverySteer, ExecIdle)
	if !idle.SchedulesLoop || !idle.IdlePriority || idle.AtStepBoundary || idle.Parked {
		t.Fatalf("steer/idle = %+v, want scheduled idle with priority", idle)
	}
}

func TestPlanAdmissionQueueAlwaysParks(t *testing.T) {
	for _, state := range []ExecutionState{ExecIdle, ExecActive} {
		a := PlanAdmission(DeliveryQueue, state)
		if a.SchedulesLoop || !a.Parked || a.AtStepBoundary {
			t.Fatalf("queue/%s = %+v, want parked", state, a)
		}
	}
}

func TestQueueStaysParkedAfterInterrupt(t *testing.T) {
	for _, resume := range []bool{true, false} {
		q := AfterInterrupt(DeliveryQueue, resume)
		if !q.Parked || q.SchedulesLoop {
			t.Fatalf("queue after interrupt resume=%v = %+v, want parked", resume, q)
		}
		s := AfterInterrupt(DeliverySteer, resume)
		if !s.AtStepBoundary || s.Parked {
			t.Fatalf("steer after interrupt resume=%v = %+v, want step boundary", resume, s)
		}
	}
}

func TestNextInputSteersWinAtIdle(t *testing.T) {
	pending := []PendingInput{
		{ID: "q1", Mode: DeliveryQueue},
		{ID: "s2", Mode: DeliverySteer},
		{ID: "q3", Mode: DeliveryQueue},
	}
	got, ok := NextInput(pending)
	if !ok || got.ID != "s2" {
		t.Fatalf("NextInput = %+v, %v; want steer s2", got, ok)
	}

	queued := []PendingInput{{ID: "q1", Mode: DeliveryQueue}, {ID: "q2", Mode: DeliveryQueue}}
	got, ok = NextInput(queued)
	if !ok || got.ID != "q1" {
		t.Fatalf("NextInput(queued) = %+v, %v; want first queued", got, ok)
	}

	if _, ok := NextInput(nil); ok {
		t.Fatal("NextInput(nil) must report no input")
	}
}

func Test409IsDuplicateIDNotBusySession(t *testing.T) {
	if got := ClassifyHTTP(409); got != StatusDuplicateID {
		t.Fatalf("ClassifyHTTP(409) = %q, want duplicate-id", got)
	}
	if got := ClassifyHTTP(200); got != StatusAdmitted {
		t.Fatalf("ClassifyHTTP(200) = %q, want admitted", got)
	}
	if got := ClassifyHTTP(500); got != StatusError {
		t.Fatalf("ClassifyHTTP(500) = %q, want error", got)
	}
	if BusySession(StatusDuplicateID) || BusySession(StatusAdmitted) || BusySession(StatusError) {
		t.Fatal("no status may be read as a busy session")
	}
}

func captureAPI(t *testing.T, response string) (*API, *[]string) {
	t.Helper()
	got := new([]string)
	api := &API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		*got = args
		return []byte(response), nil
	}}
	return api, got
}

func TestPromptSendsExplicitDeliveryMode(t *testing.T) {
	for _, mode := range []DeliveryMode{DeliverySteer, DeliveryQueue} {
		api, got := captureAPI(t, `{"data":{"id":"msg_1"}}`)
		id, err := api.Prompt(context.Background(), "ses_test", "wake", mode)
		if err != nil || id != "msg_1" {
			t.Fatalf("Prompt(%q) = %q, %v", mode, id, err)
		}
		args := *got
		if len(args) != 5 || args[0] != "api" || args[1] != "POST" ||
			args[2] != "/api/session/ses_test/prompt" || args[3] != "--data" {
			t.Fatalf("unexpected api call for %q: %q", mode, args)
		}
		var sent struct {
			Text     string `json:"text"`
			Delivery string `json:"delivery"`
		}
		if err := json.Unmarshal([]byte(args[4]), &sent); err != nil {
			t.Fatalf("decode --data: %v", err)
		}
		if sent.Delivery != string(mode) || sent.Text != "wake" {
			t.Fatalf("body for %q = %+v", mode, sent)
		}
	}
}

func TestParkSendsQueueDelivery(t *testing.T) {
	api, got := captureAPI(t, `{"data":{"id":"msg_parked"}}`)
	id, err := api.Park(context.Background(), "ses_test", "park")
	if err != nil || id != "msg_parked" {
		t.Fatalf("Park = %q, %v", id, err)
	}
	args := *got
	var sent struct {
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(args[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "queue" {
		t.Fatalf("Park delivery = %q, want queue", sent.Delivery)
	}
}

func TestInterruptResumeQuery(t *testing.T) {
	for _, resume := range []bool{true, false} {
		api, got := captureAPI(t, `{}`)
		if err := api.Interrupt(context.Background(), "ses_test", resume); err != nil {
			t.Fatalf("Interrupt(%v): %v", resume, err)
		}
		args := *got
		want := "/api/session/ses_test/interrupt?resume=" + map[bool]string{true: "true", false: "false"}[resume]
		if len(args) != 3 || args[0] != "api" || args[1] != "POST" || args[2] != want {
			t.Fatalf("Interrupt(%v) args = %q, want %q", resume, args, want)
		}
	}
}

func TestPromotePatchesInboxToSteer(t *testing.T) {
	api, got := captureAPI(t, `{}`)
	if err := api.Promote(context.Background(), "ses_test", "msg_parked"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	args := *got
	if len(args) != 5 || args[0] != "api" || args[1] != "PATCH" ||
		args[2] != "/api/session/ses_test/inbox/msg_parked" || args[3] != "--data" {
		t.Fatalf("unexpected promote call: %q", args)
	}
	if !strings.Contains(args[4], `"delivery":"steer"`) {
		t.Fatalf("promote body = %q, want steer", args[4])
	}
}

func TestEnvelopeWhitelistsFields(t *testing.T) {
	env := Envelope(Pointer{WatchID: "w-1", ObligationID: "o-1", Revision: 7, Verdict: "ready"})
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"w-1"`, `"obligationID":"o-1"`, `"revision":7`, `"verdict":"ready"`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %s: %s", want, env)
		}
	}
}

func TestPromptRejectsBadSessionAndMissingAck(t *testing.T) {
	api := &API{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if _, err := api.Prompt(context.Background(), "not-a-session", "x", DeliverySteer); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if _, err := api.Prompt(context.Background(), "ses_test", "x", DeliverySteer); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}
