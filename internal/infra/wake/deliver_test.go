package wake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// controlHost is the fake OpenCode host for the T3.204 negative control. It
// emits session events only in response to real API calls: a queue prompt
// enqueues, and a PATCH to steer starts execution and then goes idle. A
// coincidental execution.started would have to be written into this host by
// hand, which is what TestVerifySteerCausalityRejectsCoincidentalStart does.
type controlHost struct {
	sessionID      string
	events         []SessionEvent
	inboxID        string
	promptDelivery string
	promptText     string
	patches        int
}

func newControlHost(sessionID string) *controlHost {
	return &controlHost{sessionID: sessionID}
}

func (h *controlHost) runner(_ context.Context, args ...string) ([]byte, error) {
	if len(args) < 3 || args[0] != "api" {
		return nil, fmt.Errorf("unexpected runner call: %q", args)
	}
	switch args[1] {
	case "POST":
		switch {
		case strings.HasSuffix(args[2], "/prompt"):
			data, err := dataArg(args)
			if err != nil {
				return nil, err
			}
			var body struct {
				Text     string `json:"text"`
				Delivery string `json:"delivery"`
			}
			if err := json.Unmarshal([]byte(data), &body); err != nil {
				return nil, err
			}
			h.promptDelivery = body.Delivery
			h.promptText = body.Text
			h.inboxID = "msg_1"
			h.events = append(h.events, SessionEvent{Type: EventInboxEnqueued, SessionID: h.sessionID})
			return []byte(`{"data":{"id":"msg_1"}}`), nil
		case strings.HasSuffix(args[2], "/interrupt"):
			return []byte(`{"interrupted":false}`), nil
		}
	case "GET":
		switch args[2] {
		case "/api/session/" + h.sessionID + "/inbox":
			return []byte(`{"data":[{"id":"msg_1","delivery":"queue","text":"parked task"}]}`), nil
		case "/api/session/active":
			return []byte(`{"data":[]}`), nil
		}
	case "PATCH":
		data, err := dataArg(args)
		if err != nil {
			return nil, err
		}
		var body struct {
			Delivery string `json:"delivery"`
		}
		if err := json.Unmarshal([]byte(data), &body); err != nil {
			return nil, err
		}
		h.patches++
		if body.Delivery == deliverySteer {
			h.events = append(h.events,
				SessionEvent{Type: EventDeliveryChanged, SessionID: h.sessionID},
				SessionEvent{Type: EventExecutionStarted, SessionID: h.sessionID},
				SessionEvent{Type: EventIdle, SessionID: h.sessionID},
			)
		}
		return nil, nil // 204 has no body
	}
	return nil, fmt.Errorf("unexpected api call: %q", args)
}

func dataArg(args []string) (string, error) {
	if len(args) < 5 || args[3] != "--data" {
		return "", fmt.Errorf("missing --data in %q", args)
	}
	return args[4], nil
}

func hasEvent(events []SessionEvent, eventType, sessionID string) bool {
	for _, event := range events {
		if event.Type == eventType && event.SessionID == sessionID {
			return true
		}
	}
	return false
}

func testPointer() Pointer {
	return Pointer{WatchID: "watch-1", ObligationID: "obl-1", Revision: 3, Verdict: "ready"}
}

// The first half of the control: a queue prompt without PATCH leaves the
// session idle. There is no execution.started, interrupt reports false and the
// session is absent from /api/session/active.
func TestParkedQueuePromptDoesNotStartExecution(t *testing.T) {
	const sessionID = "ses_test"
	host := newControlHost(sessionID)
	a := &API{Runner: host.runner}
	ctx := context.Background()

	inboxID, err := a.Park(ctx, sessionID, testPointer())
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if inboxID != "msg_1" {
		t.Fatalf("inboxID = %q, want msg_1", inboxID)
	}
	if host.promptDelivery != deliveryQueue {
		t.Fatalf("prompt delivery = %q, want queue", host.promptDelivery)
	}
	if !strings.HasPrefix(host.promptText, "<beacon-event>") ||
		!strings.HasSuffix(host.promptText, "</beacon-event>") {
		t.Fatalf("prompt text is not an envelope: %q", host.promptText)
	}
	if hasEvent(host.events, EventExecutionStarted, sessionID) {
		t.Fatal("queue prompt started execution without a steer switch")
	}
	if host.patches != 0 {
		t.Fatalf("patches = %d, want 0 before the steer switch", host.patches)
	}

	interrupted, err := a.Interrupt(ctx, sessionID)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if interrupted {
		t.Fatal("interrupt = true on an idle session, want false")
	}
	active, err := a.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active sessions = %q, want none for an idle session", active)
	}
	if err := VerifySteerCausality(host.events, sessionID); err == nil {
		t.Fatal("queue-only event log passed the causality check")
	}
}

// The second half of the control: the same parked task starts only after the
// PATCH to steer, and the event order is enqueued, changed, started, idle.
func TestSteerAfterParkStartsExecution(t *testing.T) {
	const sessionID = "ses_test"
	host := newControlHost(sessionID)
	a := &API{Runner: host.runner}
	ctx := context.Background()

	inboxID, err := a.Park(ctx, sessionID, testPointer())
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if hasEvent(host.events, EventExecutionStarted, sessionID) {
		t.Fatal("execution started before the steer switch")
	}

	if err := a.SteerPending(ctx, sessionID, inboxID); err != nil {
		t.Fatalf("SteerPending: %v", err)
	}
	if host.patches != 1 {
		t.Fatalf("patches = %d, want 1", host.patches)
	}
	want := []string{
		EventInboxEnqueued,
		EventDeliveryChanged,
		EventExecutionStarted,
		EventIdle,
	}
	if len(host.events) != len(want) {
		t.Fatalf("events = %+v, want %d events", host.events, len(want))
	}
	for i, eventType := range want {
		if host.events[i].Type != eventType || host.events[i].SessionID != sessionID {
			t.Fatalf("events[%d] = %+v, want %s for %s", i, host.events[i], eventType, sessionID)
		}
	}
	if err := VerifySteerCausality(host.events, sessionID); err != nil {
		t.Fatalf("VerifySteerCausality: %v", err)
	}
}

// The control of the control: a log with a coincidental execution.started
// before the steer switch must be rejected. Without this, the positive test
// would pass even if the host started sessions for unrelated activity.
func TestVerifySteerCausalityRejectsCoincidentalStart(t *testing.T) {
	const sessionID = "ses_test"
	cases := []struct {
		name   string
		events []SessionEvent
		want   string
	}{
		{
			name: "execution started before steer",
			events: []SessionEvent{
				{Type: EventInboxEnqueued, SessionID: sessionID},
				{Type: EventExecutionStarted, SessionID: sessionID},
				{Type: EventDeliveryChanged, SessionID: sessionID},
				{Type: EventExecutionStarted, SessionID: sessionID},
				{Type: EventIdle, SessionID: sessionID},
			},
			want: "before steer",
		},
		{
			name: "steer without execution",
			events: []SessionEvent{
				{Type: EventInboxEnqueued, SessionID: sessionID},
				{Type: EventDeliveryChanged, SessionID: sessionID},
				{Type: EventIdle, SessionID: sessionID},
			},
			want: "after steer",
		},
		{
			name: "execution without idle",
			events: []SessionEvent{
				{Type: EventInboxEnqueued, SessionID: sessionID},
				{Type: EventDeliveryChanged, SessionID: sessionID},
				{Type: EventExecutionStarted, SessionID: sessionID},
			},
			want: "no session.idle",
		},
		{
			name: "only another session changed delivery",
			events: []SessionEvent{
				{Type: EventDeliveryChanged, SessionID: "ses_other"},
				{Type: EventExecutionStarted, SessionID: "ses_other"},
				{Type: EventIdle, SessionID: "ses_other"},
			},
			want: "no session.inbox.delivery.changed for ses_test",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifySteerCausality(tc.events, sessionID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("VerifySteerCausality = %v, want error containing %q", err, tc.want)
			}
		})
	}
}
