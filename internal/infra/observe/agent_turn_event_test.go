package observe

import (
	"errors"
	"testing"
	"time"
)

func stopPayload(pane, turn string) []byte {
	return []byte(`{"event":"Stop","pane_id":"` + pane + `","turn_id":"` + turn +
		`","session_id":"ses_abc","transcript_path":"/tmp/abc.jsonl","timestamp":"2026-11-22T18:00:00Z"}`)
}

func TestParseAgentTurnEventValid(t *testing.T) {
	want, err := time.Parse(time.RFC3339, "2026-11-22T18:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAgentTurnEvent(stopPayload("pane-1", "turn-1"))
	if err != nil {
		t.Fatalf("ParseAgentTurnEvent: %v", err)
	}
	if got.Event != AgentTurnStop || got.PaneID != "pane-1" || got.TurnID != "turn-1" {
		t.Fatalf("identity fields = %+v", got)
	}
	if got.SessionID != "ses_abc" || got.TranscriptPath != "/tmp/abc.jsonl" {
		t.Fatalf("carried fields = %+v", got)
	}
	if !got.Timestamp.Equal(want) {
		t.Fatalf("timestamp = %v, want %v", got.Timestamp, want)
	}
}

func TestParseAgentTurnEventHookEventName(t *testing.T) {
	data := []byte(`{"hook_event_name":"Stop","pane_id":"%1","turn_id":"t"}`)
	got, err := ParseAgentTurnEvent(data)
	if err != nil {
		t.Fatalf("ParseAgentTurnEvent: %v", err)
	}
	if got.Event != AgentTurnStop {
		t.Fatalf("event = %q, want %q", got.Event, AgentTurnStop)
	}
	if !got.Timestamp.IsZero() {
		t.Fatalf("omitted timestamp became %v", got.Timestamp)
	}
}

func TestParseAgentTurnEventRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error
	}{
		{"not json", `{`, ErrAgentTurnJSON},
		{"json array", `[]`, ErrAgentTurnJSON},
		{"missing event", `{"pane_id":"p","turn_id":"t"}`, ErrAgentTurnEvent},
		{"missing pane", `{"event":"Stop","turn_id":"t"}`, ErrAgentTurnPane},
		{"missing turn", `{"event":"Stop","pane_id":"p"}`, ErrAgentTurnTurn},
		{"bad timestamp", `{"event":"Stop","pane_id":"p","turn_id":"t","timestamp":"yesterday"}`, ErrAgentTurnJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentTurnEvent([]byte(tc.data))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAgentTurnTrackerStopAppliesIdle(t *testing.T) {
	event, err := ParseAgentTurnEvent(stopPayload("pane-1", "turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	result := NewAgentTurnTracker().Apply(event, "pane-1")
	if !result.Applied || result.Reason != AgentTurnApplied {
		t.Fatalf("result = %+v", result)
	}
	want, _ := time.Parse(time.RFC3339, "2026-11-22T18:00:00Z")
	tr := result.Transition
	if tr.Status != AgentSessionIdle {
		t.Fatalf("status = %q, want %q", tr.Status, AgentSessionIdle)
	}
	if tr.SessionID != "ses_abc" || tr.TurnID != "turn-1" || tr.TranscriptPath != "/tmp/abc.jsonl" {
		t.Fatalf("transition = %+v", tr)
	}
	// The transition timestamp comes from the hook, not from a clock read at
	// apply time. No process observation is involved in producing it.
	if !tr.At.Equal(want) {
		t.Fatalf("at = %v, want %v", tr.At, want)
	}
}

func TestAgentTurnTrackerRejectsForeignPane(t *testing.T) {
	event, err := ParseAgentTurnEvent(stopPayload("pane-other", "turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewAgentTurnTracker()
	result := tracker.Apply(event, "pane-mine")
	if result.Applied || result.Reason != AgentTurnForeignPane {
		t.Fatalf("result = %+v", result)
	}
	if result.Transition != (SessionStatusTransition{}) {
		t.Fatalf("rejected delivery produced %+v", result.Transition)
	}
	// A rejected delivery is not remembered. The pane that owns the turn can
	// still apply the same event later.
	if again := tracker.Apply(event, "pane-other"); !again.Applied {
		t.Fatalf("valid delivery after rejection = %+v", again)
	}
}

func TestAgentTurnTrackerIgnoresOtherEvents(t *testing.T) {
	event, err := ParseAgentTurnEvent([]byte(`{"event":"PostToolUse","pane_id":"pane-1","turn_id":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := NewAgentTurnTracker().Apply(event, "pane-1")
	if result.Applied || result.Reason != AgentTurnNotStop {
		t.Fatalf("result = %+v", result)
	}
}

func TestAgentTurnTrackerDeduplicatesReplay(t *testing.T) {
	event, err := ParseAgentTurnEvent(stopPayload("pane-1", "turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewAgentTurnTracker()
	first := tracker.Apply(event, "pane-1")
	second := tracker.Apply(event, "pane-1")
	if !first.Applied {
		t.Fatalf("first delivery = %+v", first)
	}
	if second.Applied || second.Reason != AgentTurnDuplicate {
		t.Fatalf("replayed delivery = %+v", second)
	}
	if second.Transition != (SessionStatusTransition{}) {
		t.Fatalf("replay produced a transition: %+v", second.Transition)
	}
	// The dedup key is pane id plus turn id. Another pane reusing the turn id
	// is a different delivery and must still be applied.
	other, err := ParseAgentTurnEvent(stopPayload("pane-2", "turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	if result := tracker.Apply(other, "pane-2"); !result.Applied {
		t.Fatalf("other pane delivery = %+v", result)
	}
}

func TestAgentTurnTrackerZeroValueIsUsable(t *testing.T) {
	event, err := ParseAgentTurnEvent(stopPayload("pane-1", "turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	var tracker AgentTurnTracker
	if result := tracker.Apply(event, "pane-1"); !result.Applied {
		t.Fatalf("zero value tracker = %+v", result)
	}
}
