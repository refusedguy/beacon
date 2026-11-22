package domain

import (
	"strings"
	"testing"
	"time"
)

func testSub(now time.Time, lifetime, window time.Duration) EventSubscription {
	return EventSubscription{
		ID:             "sub-1",
		SourceKind:     SourceGitHubPRReview,
		OwnerSessionID: "ses_owner",
		ExpiresAt:      now.Add(lifetime),
		CoalesceWindow: window,
	}
}

func TestSourceKindValid(t *testing.T) {
	for _, k := range []SourceKind{
		SourceGitHubPRReview, SourceGitHubCI, SourceSlackThread,
		SourceLinearIssue, SourceTimer,
	} {
		if !k.Valid() {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []SourceKind{"", "github_discussion", "email"} {
		if k.Valid() {
			t.Errorf("%q should be rejected", k)
		}
	}
}

func TestNewEventSubscriptionRejectsUnknownSource(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	_, err := NewEventSubscription("sub-1", SourceKind("carrier_pigeon"), "ses_owner", now, 24*time.Hour, time.Minute)
	if err == nil {
		t.Fatal("unknown source must be rejected")
	}
	appErr, ok := err.(*AppError)
	if !ok || appErr.Code != CodeInvalidInput {
		t.Fatalf("want INVALID_INPUT, got %v", err)
	}
}

func TestNewEventSubscriptionRejectsLifetimeAndWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lifetime time.Duration
		window   time.Duration
		code     ErrorCode
	}{
		{"over 180 days", 181 * 24 * time.Hour, time.Minute, CodeLimitExceeded},
		{"zero lifetime", 0, time.Minute, CodeWatchExpired},
		{"negative window", 24 * time.Hour, -time.Second, CodeInvalidInput},
		{"zero window", 24 * time.Hour, 0, CodeInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewEventSubscription("sub-1", SourceGitHubCI, "ses_owner", now, tc.lifetime, tc.window)
			appErr, ok := err.(*AppError)
			if !ok || appErr.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
	if _, err := NewEventSubscription("sub-1", SourceTimer, "ses_owner", now, 24*time.Hour, time.Minute); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
}

func TestThreeEventsInWindowGiveOneDelivery(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSub(now, 24*time.Hour, 30*time.Second)
	events := []ExternalEvent{
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/1", Preview: "first", At: now},
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/2", Preview: "second", At: now.Add(5 * time.Second)},
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/3", Preview: "third", At: now.Add(10 * time.Second)},
	}

	wakes := CoalesceEvents(events, map[string]EventSubscription{sub.ID: sub}, now.Add(11*time.Second))
	if len(wakes) != 1 {
		t.Fatalf("want 1 wake, got %d", len(wakes))
	}
	w := wakes[0]
	if w.EventCount != 3 {
		t.Errorf("want 3 coalesced events, got %d", w.EventCount)
	}
	if !w.RereadSource {
		t.Error("coalesced wake must require a source re-read")
	}
	if w.SourceRef != "pr/3" {
		t.Errorf("want newest source ref pr/3, got %q", w.SourceRef)
	}
	if !w.Deliverable() {
		t.Error("wake should be deliverable")
	}
}

func TestEventAfterWindowGivesSecondDelivery(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSub(now, 24*time.Hour, 30*time.Second)
	events := []ExternalEvent{
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/1", At: now},
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/2", At: now.Add(5 * time.Second)},
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/3", At: now.Add(10 * time.Second)},
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/4", At: now.Add(40 * time.Second)},
	}

	wakes := CoalesceEvents(events, map[string]EventSubscription{sub.ID: sub}, now.Add(41*time.Second))
	if len(wakes) != 2 {
		t.Fatalf("want 2 wakes, got %d", len(wakes))
	}
	if wakes[0].EventCount != 3 || wakes[1].EventCount != 1 {
		t.Errorf("want batches of 3 and 1, got %d and %d", wakes[0].EventCount, wakes[1].EventCount)
	}
	if wakes[1].SourceRef != "pr/4" {
		t.Errorf("second wake should point at pr/4, got %q", wakes[1].SourceRef)
	}
}

func TestLastDeliveryAtAnchorsTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSub(now, 24*time.Hour, 30*time.Second)
	sub.LastDeliveryAt = now.Add(1 * time.Second)
	events := []ExternalEvent{
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/1", At: now.Add(20 * time.Second)},
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/2", At: now.Add(25 * time.Second)},
	}
	wakes := CoalesceEvents(events, map[string]EventSubscription{sub.ID: sub}, now.Add(26*time.Second))
	if len(wakes) != 1 || wakes[0].EventCount != 2 {
		t.Fatalf("events inside the anchored window should batch into one wake, got %+v", wakes)
	}
}

func TestExpiredSubscriptionDeliversNothing(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSub(now, time.Minute, 30*time.Second)
	after := now.Add(2 * time.Minute)
	events := []ExternalEvent{
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/1", At: after},
	}
	wakes := CoalesceEvents(events, map[string]EventSubscription{sub.ID: sub}, after)
	if len(wakes) != 0 {
		t.Fatalf("expired subscription delivered %d wakes", len(wakes))
	}
}

func TestCoalesceEventsDropsUnknownSources(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	sub := testSub(now, 24*time.Hour, time.Minute)
	unknown := sub
	unknown.ID = "sub-unknown"
	unknown.SourceKind = SourceKind("carrier_pigeon")

	events := []ExternalEvent{
		{SubscriptionID: sub.ID, SourceKind: sub.SourceKind, Ref: "pr/1", At: now},
		{SubscriptionID: unknown.ID, SourceKind: unknown.SourceKind, Ref: "x", At: now},
	}
	wakes := CoalesceEvents(events, map[string]EventSubscription{sub.ID: sub, unknown.ID: unknown}, now)
	if len(wakes) != 1 || wakes[0].SubscriptionID != sub.ID {
		t.Fatalf("only the valid subscription should deliver, got %+v", wakes)
	}
}

func TestWakeInstructionRequiresReread(t *testing.T) {
	w := WakeRequest{SubscriptionID: "sub-1", OwnerSessionID: "ses_owner", RereadSource: true}
	instruction := w.Instruction()
	if !strings.Contains(instruction, "Перечитай") || !strings.Contains(instruction, "источник") {
		t.Fatalf("instruction must demand a source re-read, got %q", instruction)
	}
}

func TestAggregateCIResultWaitsForAllChecks(t *testing.T) {
	pending := AggregateCIResult("sha1", map[string]Check{
		"build": {Conclusion: "SUCCESS"},
		"docs":  {State: "PENDING"},
	})
	if pending.AllTerminal || pending.Deliverable() || pending.Ready() {
		t.Fatalf("a pending check must hold the result: %+v", pending)
	}

	green := AggregateCIResult("sha2", map[string]Check{
		"build": {Conclusion: "SUCCESS"},
		"audit": {Conclusion: "NEUTRAL"},
	})
	if !green.AllTerminal || !green.Deliverable() || !green.Ready() || len(green.FailedChecks) != 0 {
		t.Fatalf("all-green commit should be ready: %+v", green)
	}

	failed := AggregateCIResult("sha3", map[string]Check{
		"build": {Conclusion: "FAILURE"},
		"audit": {Conclusion: "SUCCESS"},
	})
	if !failed.Deliverable() || failed.Ready() || len(failed.FailedChecks) != 1 || failed.FailedChecks[0] != "build" {
		t.Fatalf("failed commit should deliver a failure list: %+v", failed)
	}
}

func TestAggregateCIResultActionRequiredIsNotAFailedCheck(t *testing.T) {
	r := AggregateCIResult("sha4", map[string]Check{
		"stuck": {Conclusion: "ACTION_REQUIRED"},
		"build": {Conclusion: "SUCCESS"},
	})
	if !r.ActionRequired {
		t.Error("action_required must be recorded separately")
	}
	if len(r.FailedChecks) != 0 {
		t.Errorf("action_required must not become a failed check, got %v", r.FailedChecks)
	}
	if !r.Deliverable() {
		t.Error("action_required must not block delivery")
	}
	if r.Ready() {
		t.Error("action_required must withhold readiness")
	}
}
