// Package wake adapts Beacon's wake pointer to the host harness.
// Consolidated from PRs #281, #286, #282, #287, #285, #290, #291, #288, #283, #284, #289, #276.
package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// ===== база wake (#281) =====

func TestEnvelopeWhitelistsFields(t *testing.T) {
	p := domain.WakePayload{WatchID: "watch-1", ObligationID: "obl-1", Revision: 7, Verdict: "ready"}
	env := Envelope(p)
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"watch-1"`, `"obligationID":"obl-1"`, `"revision":7`, `"verdict":"ready"`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %q: %s", want, env)
		}
	}
}

func TestOpenCodeDelivererPostsSteerPrompt(t *testing.T) {
	var gotArgs []string
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"data":{"id":"msg_1","sessionID":"ses_test"}}`), nil
	}}
	err := d.Deliver(context.Background(), "ses_test", domain.WakePayload{
		WatchID: "watch-1", ObligationID: domain.ObligationKey("watch-1", 1, "ses_test"), Revision: 1, Verdict: "ready",
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(gotArgs) < 5 {
		t.Fatalf("args = %q", gotArgs)
	}
	if gotArgs[0] != "api" || gotArgs[1] != "POST" || gotArgs[2] != "/api/session/ses_test/prompt" {
		t.Fatalf("unexpected api call: %q", gotArgs)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(gotArgs[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
	if !strings.HasPrefix(sent.Text, "<beacon-event>") || !strings.HasSuffix(sent.Text, "</beacon-event>") {
		t.Fatalf("missing envelope: %q", sent.Text)
	}
}

func TestOpenCodeDelivererRejectsBadSessionAndMissingAck(t *testing.T) {
	d := &OpenCodeDeliverer{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if err := d.Deliver(context.Background(), "not-a-session", domain.WakePayload{}); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	p := domain.WakePayload{WatchID: "watch-2", ObligationID: domain.ObligationKey("watch-2", 1, "ses_test"), Revision: 1}
	if err := d.Deliver(context.Background(), "ses_test", p); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

// A pointer names an obligation of one subscriber. Deliver must not steer a
// different session even when that session id is otherwise well formed.
func TestOpenCodeDelivererRejectsForeignSession(t *testing.T) {
	var prompts int
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		prompts++
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	const owner = "ses_owner"
	p := domain.WakePayload{
		WatchID:      "watch-3",
		ObligationID: domain.ObligationKey("watch-3", 4, owner),
		Revision:     4,
		Verdict:      "ready",
	}
	if err := d.Deliver(context.Background(), "ses_other", p); err == nil {
		t.Fatal("Deliver accepted a session that is not the obligation subscriber")
	}
	if prompts != 0 {
		t.Fatalf("prompt calls = %d, want 0 for a foreign session", prompts)
	}
	if err := d.Deliver(context.Background(), owner, p); err != nil {
		t.Fatalf("Deliver for the owner: %v", err)
	}
	if prompts != 1 {
		t.Fatalf("prompt calls = %d, want 1 for the owner", prompts)
	}
}

// A repeated wake with the same pointer must be a no-op. A new revision of the
// same obligation is a new wake and gets its own prompt.
func TestOpenCodeDelivererRepeatedPointerPostsOnePrompt(t *testing.T) {
	var prompts int
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		prompts++
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	const sessionID = "ses_test"
	p := domain.WakePayload{
		WatchID:      "watch-4",
		ObligationID: domain.ObligationKey("watch-4", 7, sessionID),
		Revision:     7,
		Verdict:      "ready",
	}
	for i := 0; i < 3; i++ {
		if err := d.Deliver(context.Background(), sessionID, p); err != nil {
			t.Fatalf("Deliver #%d: %v", i+1, err)
		}
	}
	if prompts != 1 {
		t.Fatalf("prompt calls = %d, want 1 after repeated wakes", prompts)
	}

	next := domain.WakePayload{
		WatchID:      p.WatchID,
		ObligationID: domain.ObligationKey(p.WatchID, p.Revision+1, sessionID),
		Revision:     p.Revision + 1,
		Verdict:      "ready",
	}
	if err := d.Deliver(context.Background(), sessionID, next); err != nil {
		t.Fatalf("Deliver on new revision: %v", err)
	}
	if prompts != 2 {
		t.Fatalf("prompt calls = %d, want 2 after a new revision", prompts)
	}
}

// T3.230: the wake target is the orchestrator session of a real Watch. A
// substituted watchID or an obligation of another subscriber must not wake it.
func TestDeliverToWatchUsesRealWatchTarget(t *testing.T) {
	const (
		watchID = "watch:refusedguy/beacon:286:177bf240"
		owner   = "ses_orchestrator"
	)
	w := domain.Watch{
		ID: watchID,
		Subscribers: []domain.Subscriber{
			{ID: "ses_executor", Role: domain.RoleExecutor},
			{ID: owner, Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	p := domain.WakePayload{
		WatchID:      watchID,
		ObligationID: domain.ObligationKey(watchID, 1, owner),
		Revision:     1,
		Verdict:      "ready",
	}

	var targets []string
	d := &recordingWakeDeliverer{fn: func(_ context.Context, sessionID string, _ domain.WakePayload) error {
		targets = append(targets, sessionID)
		return nil
	}}
	if err := DeliverToWatch(context.Background(), d, w, p); err != nil {
		t.Fatalf("DeliverToWatch: %v", err)
	}
	if !reflect.DeepEqual(targets, []string{owner}) {
		t.Fatalf("targets = %v, want [%s]", targets, owner)
	}

	stubbed := p
	stubbed.WatchID = "watch:stubbed"
	if err := DeliverToWatch(context.Background(), d, w, stubbed); err == nil {
		t.Fatal("a substituted watchID must not deliver a wake")
	}
	foreign := p
	foreign.ObligationID = domain.ObligationKey(watchID, 1, "ses_executor")
	if err := DeliverToWatch(context.Background(), d, w, foreign); err == nil {
		t.Fatal("an obligation of another subscriber must not wake the orchestrator")
	}
	if len(targets) != 1 {
		t.Fatalf("prompt targets = %v, want exactly one", targets)
	}
}

func TestDeliverToWatchPostsSteerAndReadsMessageID(t *testing.T) {
	const (
		watchID = "watch:refusedguy/beacon:286:177bf240"
		owner   = "ses_orchestrator"
	)
	w := domain.Watch{ID: watchID, Subscribers: []domain.Subscriber{
		{ID: owner, Role: domain.RoleOrchestrator},
	}}
	p := domain.WakePayload{
		WatchID:      watchID,
		ObligationID: domain.ObligationKey(watchID, 3, owner),
		Revision:     3,
		Verdict:      "ready",
	}
	var gotArgs []string
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"data":{"id":"msg_1f72fc9f","delivery":"steer"}}`), nil
	}}
	if err := DeliverToWatch(context.Background(), d, w, p); err != nil {
		t.Fatalf("DeliverToWatch: %v", err)
	}
	if len(gotArgs) < 5 || gotArgs[2] != "/api/session/"+owner+"/prompt" {
		t.Fatalf("api call = %v", gotArgs)
	}
	var body struct {
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(gotArgs[4]), &body); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if body.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", body.Delivery)
	}
}

type recordingWakeDeliverer struct {
	fn func(context.Context, string, domain.WakePayload) error
}

func (r *recordingWakeDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	return r.fn(ctx, sessionID, p)
}

// ===== T3.202 steer/queue и interrupt (#287) =====

func TestHooksApplyOnlyToPromptT202(t *testing.T) {
	cases := map[T202InputKind]bool{
		T202InputPrompt:    true,
		T202InputSynthetic: false,
		T202InputShell:     false,
		T202InputCompact:   false,
		T202InputMove:      false,
	}
	for kind, want := range cases {
		if got := T202HooksApply(kind); got != want {
			t.Errorf("T202HooksApply(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestDefaultDeliveryT202(t *testing.T) {
	for _, kind := range []T202InputKind{T202InputPrompt, T202InputSynthetic, T202InputCompact} {
		if got := T202DefaultDelivery(kind); got != T202DeliverySteer {
			t.Errorf("T202DefaultDelivery(%q) = %q, want steer", kind, got)
		}
	}
}

func TestPlanAdmissionSteerAtStepBoundaryT202(t *testing.T) {
	active := T202PlanAdmission(T202DeliverySteer, T202ExecActive)
	if !active.SchedulesLoop || !active.AtStepBoundary || active.Parked {
		t.Fatalf("steer/active = %+v, want scheduled at step boundary", active)
	}

	idle := T202PlanAdmission(T202DeliverySteer, T202ExecIdle)
	if !idle.SchedulesLoop || !idle.IdlePriority || idle.AtStepBoundary || idle.Parked {
		t.Fatalf("steer/idle = %+v, want scheduled idle with priority", idle)
	}
}

func TestPlanAdmissionQueueAlwaysParksT202(t *testing.T) {
	for _, state := range []T202ExecutionState{T202ExecIdle, T202ExecActive} {
		a := T202PlanAdmission(T202DeliveryQueue, state)
		if a.SchedulesLoop || !a.Parked || a.AtStepBoundary {
			t.Fatalf("queue/%s = %+v, want parked", state, a)
		}
	}
}

func TestQueueStaysParkedAfterInterruptT202(t *testing.T) {
	for _, resume := range []bool{true, false} {
		q := T202AfterInterrupt(T202DeliveryQueue, resume)
		if !q.Parked || q.SchedulesLoop {
			t.Fatalf("queue after interrupt resume=%v = %+v, want parked", resume, q)
		}
		s := T202AfterInterrupt(T202DeliverySteer, resume)
		if !s.AtStepBoundary || s.Parked {
			t.Fatalf("steer after interrupt resume=%v = %+v, want step boundary", resume, s)
		}
	}
}

func TestNextInputSteersWinAtIdleT202(t *testing.T) {
	pending := []T202PendingInput{
		{ID: "q1", Mode: T202DeliveryQueue},
		{ID: "s2", Mode: T202DeliverySteer},
		{ID: "q3", Mode: T202DeliveryQueue},
	}
	got, ok := T202NextInput(pending)
	if !ok || got.ID != "s2" {
		t.Fatalf("T202NextInput = %+v, %v; want steer s2", got, ok)
	}

	queued := []T202PendingInput{{ID: "q1", Mode: T202DeliveryQueue}, {ID: "q2", Mode: T202DeliveryQueue}}
	got, ok = T202NextInput(queued)
	if !ok || got.ID != "q1" {
		t.Fatalf("T202NextInput(queued) = %+v, %v; want first queued", got, ok)
	}

	if _, ok := T202NextInput(nil); ok {
		t.Fatal("T202NextInput(nil) must report no input")
	}
}

func Test409IsDuplicateIDNotBusySessionT202(t *testing.T) {
	if got := T202ClassifyHTTP(409); got != T202StatusDuplicateID {
		t.Fatalf("T202ClassifyHTTP(409) = %q, want duplicate-id", got)
	}
	if got := T202ClassifyHTTP(200); got != T202StatusAdmitted {
		t.Fatalf("T202ClassifyHTTP(200) = %q, want admitted", got)
	}
	if got := T202ClassifyHTTP(500); got != T202StatusError {
		t.Fatalf("T202ClassifyHTTP(500) = %q, want error", got)
	}
	if T202BusySession(T202StatusDuplicateID) || T202BusySession(T202StatusAdmitted) || T202BusySession(T202StatusError) {
		t.Fatal("no status may be read as a busy session")
	}
}

func T202captureAPI(t *testing.T, response string) (*T202API, *[]string) {
	t.Helper()
	got := new([]string)
	api := &T202API{T202Runner: func(_ context.Context, args ...string) ([]byte, error) {
		*got = args
		return []byte(response), nil
	}}
	return api, got
}

func TestPromptSendsExplicitDeliveryModeT202(t *testing.T) {
	for _, mode := range []T202DeliveryMode{T202DeliverySteer, T202DeliveryQueue} {
		api, got := T202captureAPI(t, `{"data":{"id":"msg_1"}}`)
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

// A wake usually reaches a subscriber whose last turn already succeeded. The
// prompt must ask the host to resume that session, not only steer a new turn.
func TestPromptResumesCompletedSessionT202(t *testing.T) {
	var got []string
	api := &T202API{T202Runner: func(_ context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`{"data":{"id":"msg_resumed","sessionID":"ses_done"}}`), nil
	}}
	const text = `<beacon-event>{"watchID":"w","obligationID":"o","revision":1,"verdict":"ready"}</beacon-event>`
	id, err := api.Prompt(context.Background(), "ses_done", text, T202DeliverySteer)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if id != "msg_resumed" {
		t.Fatalf("id = %q, want msg_resumed", id)
	}
	if len(got) != 5 || got[0] != "api" || got[1] != "POST" ||
		got[2] != "/api/session/ses_done/prompt" || got[3] != "--data" {
		t.Fatalf("api call = %q", got)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   *bool  `json:"resume"`
	}
	if err := json.Unmarshal([]byte(got[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Resume == nil || !*sent.Resume {
		t.Fatalf("resume = %v, want true in %s", sent.Resume, got[4])
	}
	if sent.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
	if sent.Text != text {
		t.Fatalf("text = %q, want %q", sent.Text, text)
	}
}

func TestPromptRejectsInvalidSessionIDWithoutRunnerT202(t *testing.T) {
	called := false
	api := &T202API{T202Runner: func(context.Context, ...string) ([]byte, error) {
		called = true
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	if _, err := api.Prompt(context.Background(), "not-a-session", "wake", T202DeliverySteer); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if called {
		t.Fatal("runner must not be called for an invalid session id")
	}
}

func TestParkSendsQueueDeliveryT202(t *testing.T) {
	api, got := T202captureAPI(t, `{"data":{"id":"msg_parked"}}`)
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

func TestInterruptResumeQueryT202(t *testing.T) {
	for _, resume := range []bool{true, false} {
		api, got := T202captureAPI(t, `{}`)
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

func TestPromotePatchesInboxToSteerT202(t *testing.T) {
	api, got := T202captureAPI(t, `{}`)
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

func TestEnvelopeWhitelistsFieldsT202(t *testing.T) {
	env := T202Envelope(T202Pointer{WatchID: "w-1", ObligationID: "o-1", Revision: 7, Verdict: "ready"})
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"w-1"`, `"obligationID":"o-1"`, `"revision":7`, `"verdict":"ready"`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %s: %s", want, env)
		}
	}
}

func TestPromptRejectsBadSessionAndMissingAckT202(t *testing.T) {
	api := &T202API{T202Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if _, err := api.Prompt(context.Background(), "not-a-session", "x", T202DeliverySteer); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if _, err := api.Prompt(context.Background(), "ses_test", "x", T202DeliverySteer); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

// ===== T3.210-T3.215 =====

type t210FakeAdapter struct {
	binds    int
	delivers int
	observes int
	target   T210Target
}

func (a *t210FakeAdapter) Bind(p T210WakePointer, sessions *T211SessionRegistry) (T210Target, bool) {
	a.binds++
	if a.target != "" {
		return a.target, true
	}
	_, ok := sessions.Lookup(p.Adapter, p.Target)
	return "", ok
}

func (a *t210FakeAdapter) Deliver(p T210WakePointer, _ T210Target) (T210Receipt, error) {
	a.delivers++
	return T210Receipt{ObligationID: p.ObligationID, Attempt: p.Attempt, Delivery: p.Delivery, Outcome: T210Delivered}, nil
}

func (a *t210FakeAdapter) Observe(_ T210WakePointer, _ T210Receipt) error {
	a.observes++
	return nil
}

func TestT210BindThroughRegistryAndSkipUnknown(t *testing.T) {
	sessions := T211NewSessionRegistry()
	adapters := T211NewAdapterRegistry()
	deliveries := T211NewDeliveryRegistry()
	a := &t210FakeAdapter{}
	adapters.Register("bridge-a", a)

	unknown := T210WakePointer{Adapter: "bridge-b", ObligationID: "o-0"}
	if _, ok := T210Deliver(context.Background(), unknown, nil, sessions, adapters, deliveries); ok {
		t.Fatal("unknown adapter was delivered")
	}
	missing := T210WakePointer{Adapter: "bridge-a", Target: "t-missing", ObligationID: "o-1"}
	if _, ok := T210Deliver(context.Background(), missing, nil, sessions, adapters, deliveries); ok {
		t.Fatal("unregistered target was delivered")
	}
	if a.delivers != 0 {
		t.Fatalf("adapter delivered %d times before a registry match", a.delivers)
	}

	sessions.Add("bridge-a", "t-1", "ses_1")
	p := T210WakePointer{Adapter: "bridge-a", Target: "t-1", ObligationID: "o-2", Attempt: 3, Delivery: "steer"}
	r, ok := T210Deliver(context.Background(), p, nil, sessions, adapters, deliveries)
	if !ok || r.Outcome != T210Delivered || r.ObligationID != "o-2" {
		t.Fatalf("delivery = %+v, ok = %v", r, ok)
	}
	if a.delivers != 1 || a.observes != 2 {
		t.Fatalf("delivers = %d, observes = %d, want 1 and 2", a.delivers, a.observes)
	}
}

func TestT211RegistriesKeepTargetsOpaqueAndImportReceipts(t *testing.T) {
	sessions := T211NewSessionRegistry()
	if !sessions.AddFromMeta("bridge-a", map[string]string{"ai.opencode/sessionID": "ses_meta"}, "t-ignored") {
		t.Fatal("AddFromMeta rejected a valid _meta")
	}
	if sid, ok := sessions.Lookup("bridge-a", "ses_meta"); !ok || sid != "ses_meta" {
		t.Fatalf("_meta lookup = %q, %v", sid, ok)
	}
	if !sessions.AddFromMeta("bridge-a", map[string]string{}, "notify-target") {
		t.Fatal("AddFromMeta rejected notify.target")
	}
	if sid, ok := sessions.Lookup("bridge-a", "notify-target"); !ok || sid != "notify-target" {
		t.Fatalf("notify lookup = %q, %v", sid, ok)
	}

	adapters := T211NewAdapterRegistry()
	adapters.Register("bridge-a", &t210FakeAdapter{})
	adapters.Register("bridge-b", &t210FakeAdapter{})
	if _, ok := adapters.Get("bridge-a"); !ok {
		t.Fatal("registered adapter missing")
	}
	if _, ok := adapters.Get("bridge-c"); ok {
		t.Fatal("unknown adapter found")
	}

	d := T211NewDeliveryRegistry()
	batch := []T210Receipt{
		{ObligationID: "o-1", Attempt: 1, Outcome: T210Delivered},
		{ObligationID: "o-1", Attempt: 1, Outcome: T210Delivered},
		{ObligationID: "o-1", Attempt: 2, Outcome: T210Queued},
	}
	if got := d.ImportReceipts(batch); got != 2 {
		t.Fatalf("ImportReceipts added %d, want 2", got)
	}
	if !d.Has("o-1", 2) || d.Has("o-1", 3) {
		t.Fatal("delivery registry has wrong keys")
	}
	if r, ok := d.Get("o-1", 1); !ok || r.Outcome != T210Delivered {
		t.Fatalf("stored receipt = %+v, %v", r, ok)
	}
}

func TestT212PromptTextHasIdentifiersOnly(t *testing.T) {
	p := T210WakePointer{
		WatchID:      `watch<&>"'` + "\n\x00",
		ObligationID: "obl-9",
		Revision:     42,
		VerdictRef:   "watch-9/revision/42",
	}
	text := T212buildPromptText(p)
	if !strings.HasPrefix(text, "<beacon-event>") || !strings.HasSuffix(text, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", text)
	}
	if len(text) > 512 {
		t.Fatalf("prompt is %d bytes, want at most 512", len(text))
	}
	for _, forbidden := range []string{"checkName", "prTitle", "branch", "<watch", "\n"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("prompt contains %q: %s", forbidden, text)
		}
	}
	for _, want := range []string{`"watchID"`, `"obligationID"`, `"revision"`, `"verdictRef"`, `"obl-9"`, "42"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q: %s", want, text)
		}
	}
	if !strings.Contains(text, `\u0026lt;`) || !strings.Contains(text, `\\u0000`) {
		t.Fatalf("escapeUntrusted did not escape markup and control chars: %q", text)
	}
	long := p
	long.WatchID = strings.Repeat("w", 5000)
	if got := len(T212buildPromptText(long)); got > 512 {
		t.Fatalf("long prompt is %d bytes, want at most 512", got)
	}
}

// ===== T3.222 канонический ключ _meta для сессии (#320) =====

func TestSessionIDFromMetaPrefersNeutralKey(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"neutral", map[string]any{SessionIDMetaKey: "ses_neutral"}, "ses_neutral"},
		{"legacy", map[string]any{LegacySessionIDMetaKey: "ses_legacy"}, "ses_legacy"},
		{"neutral wins", map[string]any{SessionIDMetaKey: "ses_neutral", LegacySessionIDMetaKey: "ses_legacy"}, "ses_neutral"},
		{"wrong type", map[string]any{SessionIDMetaKey: 42}, ""},
		{"empty", map[string]any{SessionIDMetaKey: ""}, ""},
		{"none", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionIDFromMeta(tc.meta); got != tc.want {
				t.Fatalf("SessionIDFromMeta = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveSessionTargetSkipsWithoutMetaOrNotify(t *testing.T) {
	if _, ok := ResolveSessionTarget(nil, ""); ok {
		t.Fatal("no _meta and no notify.target must resolve to skip")
	}
	if _, ok := ResolveSessionTarget(nil, "   "); ok {
		t.Fatal("blank notify.target must resolve to skip")
	}
	if got, ok := ResolveSessionTarget(nil, "notify-target"); !ok || got != "notify-target" {
		t.Fatalf("notify target = %q, %v; want notify-target, true", got, ok)
	}
	got, ok := ResolveSessionTarget(map[string]any{SessionIDMetaKey: "ses_1"}, "notify-target")
	if !ok || got != "ses_1" {
		t.Fatalf("target = %q, %v; want the _meta session id", got, ok)
	}
}

func TestSessionRegistryBindsWatchToTarget(t *testing.T) {
	r := NewSessionRegistry()
	if target, err := r.BindWatch("watch-1", "sub-1", map[string]any{LegacySessionIDMetaKey: "ses_old"}, ""); err != nil || target != "ses_old" {
		t.Fatalf("BindWatch = %q, %v; want ses_old, nil", target, err)
	}
	if target, ok := r.Target("watch-1", "sub-1"); !ok || target != "ses_old" {
		t.Fatalf("Target = %q, %v; want ses_old, true", target, ok)
	}
	if rows := r.Rows(); rows["watch-1"] != "ses_old" {
		t.Fatalf("Rows = %v, want a watch-1 -> ses_old row", rows)
	}
	if _, err := r.BindWatch("watch-2", "sub-2", nil, ""); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("BindWatch without a source = %v, want ErrNoTarget", err)
	}
	if _, ok := r.Rows()["watch-2"]; ok {
		t.Fatal("a skipped watch must not create a SessionRegistry row")
	}
}

type boundDelivererStub struct {
	calls    int
	sessions []string
}

func (s *boundDelivererStub) Deliver(_ context.Context, sessionID string, _ domain.WakePayload) error {
	s.calls++
	s.sessions = append(s.sessions, sessionID)
	return nil
}

func TestBoundDelivererSkipsUnboundWatch(t *testing.T) {
	inner := &boundDelivererStub{}
	d := &BoundDeliverer{Registry: NewSessionRegistry(), Inner: inner}
	p := domain.WakePayload{WatchID: "watch-1", ObligationID: "obl-1"}

	if err := d.Deliver(context.Background(), "ses_ghost", p); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("Deliver without binding = %v, want ErrNoTarget", err)
	}
	if inner.calls != 0 {
		t.Fatalf("inner deliverer called %d times on a skipped watch", inner.calls)
	}

	if _, err := d.Registry.BindWatch("watch-1", "sub-1", map[string]any{SessionIDMetaKey: "ses_1"}, ""); err != nil {
		t.Fatalf("BindWatch: %v", err)
	}
	if err := d.Deliver(context.Background(), "ses_1", p); err != nil {
		t.Fatalf("Deliver after binding: %v", err)
	}
	if inner.calls != 1 || inner.sessions[0] != "ses_1" {
		t.Fatalf("inner got %+v, want one delivery to ses_1", inner)
	}
	if err := d.Deliver(context.Background(), "ses_other", p); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("Deliver to an unbound session = %v, want ErrNoTarget", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner called %d times, want 1: no prompt for an unbound session", inner.calls)
	}
}

// ===== T3.223 живучесть локации OpenCode (#320) =====

func TestValidateLocationLiveness(t *testing.T) {
	cfg := LocationConfig{Mitigation: LocationMitigationFalse, IdleTimeout: "false"}
	good := LocationObservation{
		Mitigation:   LocationMitigationFalse,
		IdleFor:      W12LocationFacts.HeldWithMitigation,
		PluginActive: true,
	}
	if err := ValidateLocationLiveness(cfg, good); err != nil {
		t.Fatalf("ValidateLocationLiveness(good): %v", err)
	}
	short := good
	short.IdleFor = W12LocationFacts.UnloadedAfter
	if err := ValidateLocationLiveness(cfg, short); err == nil {
		t.Fatal("a 2m47s observation must not prove a 3 minute hold")
	}
	gone := good
	gone.PluginActive = false
	if err := ValidateLocationLiveness(cfg, gone); err == nil {
		t.Fatal("a vanished plugin must not prove liveness")
	}
	other := good
	other.Mitigation = LocationMitigationKeepalive
	if err := ValidateLocationLiveness(cfg, other); err == nil {
		t.Fatal("an observation of a different mitigation must not prove this one")
	}
}

func TestW12LocationFactsRecordMeasuredResult(t *testing.T) {
	if W12LocationFacts.UnloadedAfter >= locationHoldFloor {
		t.Fatalf("recorded unload %s should be below the 3 minute floor", W12LocationFacts.UnloadedAfter)
	}
	if W12LocationFacts.HeldWithMitigation <= locationHoldFloor {
		t.Fatalf("recorded hold %s should exceed the 3 minute floor", W12LocationFacts.HeldWithMitigation)
	}
	if W12LocationFacts.ChosenMitigation != LocationMitigationFalse {
		t.Fatalf("chosen mitigation = %q, want %q", W12LocationFacts.ChosenMitigation, LocationMitigationFalse)
	}
}

// ===== T3.224 дедупликация доставки в Cursor Cloud Agents (#320) =====

func TestCursorDelivererCoalescesDuplicate(t *testing.T) {
	calls := 0
	d := &CursorDeliverer{
		Journal: NewCursorDeliveryJournal(),
		Transport: func(_ context.Context, _ CursorSteer) (T206DeliveryStatus, error) {
			calls++
			return T206StatusDelivered, nil
		},
	}
	steer := NewCursorSteer("wake", "steer")
	first, err := d.DeliverSteer(context.Background(), "dlv-1", steer)
	if err != nil || first.Status != T206StatusDelivered {
		t.Fatalf("first = %+v, %v; want delivered", first, err)
	}
	second, err := d.DeliverSteer(context.Background(), "dlv-1", steer)
	if err != nil || second.Status != T206StatusDuplicate || !second.Coalesced {
		t.Fatalf("repeat = %+v, %v; want coalesced duplicate", second, err)
	}
	if calls != 1 {
		t.Fatalf("transport called %d times, want 1", calls)
	}
}

func TestCursorDelivererRejectsReusedIDWithDifferentBody(t *testing.T) {
	d := &CursorDeliverer{
		Journal: NewCursorDeliveryJournal(),
		Transport: func(_ context.Context, _ CursorSteer) (T206DeliveryStatus, error) {
			return T206StatusDelivered, nil
		},
	}
	if _, err := d.DeliverSteer(context.Background(), "dlv-1", NewCursorSteer("first", "steer")); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	_, err := d.DeliverSteer(context.Background(), "dlv-1", NewCursorSteer("second", "steer"))
	if !errors.Is(err, T206ErrDeliveryIDReuse) {
		t.Fatalf("reused id with a new body = %v, want T206ErrDeliveryIDReuse", err)
	}
}

func TestCursorDelivererRetryAfterNetworkErrorIsDuplicate(t *testing.T) {
	calls := 0
	d := &CursorDeliverer{
		Journal: NewCursorDeliveryJournal(),
		Transport: func(_ context.Context, _ CursorSteer) (T206DeliveryStatus, error) {
			calls++
			if calls == 1 {
				return "", errors.New("network error: the steer may have arrived")
			}
			return T206StatusDelivered, nil
		},
	}
	steer := NewCursorSteer("wake", "steer")
	if _, err := d.DeliverSteer(context.Background(), "dlv-1", steer); err == nil {
		t.Fatal("first transport error should be reported")
	}
	retry, err := d.DeliverSteer(context.Background(), "dlv-1", steer)
	if err != nil || retry.Status != T206StatusDuplicate {
		t.Fatalf("retry = %+v, %v; want a coalesced duplicate", retry, err)
	}
	if calls != 1 {
		t.Fatalf("transport called %d times after a retry, want 1", calls)
	}
}

func TestCursorSteerBodyHasNoDeliveryID(t *testing.T) {
	body, err := json.Marshal(NewCursorSteer("wake", "steer"))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), "deliveryId") {
		t.Fatalf("Cursor steer body = %s, want only prompt and delivery", body)
	}
}

// ===== T3.250: history search with an explicit backend =====

func historyExitOne(t *testing.T) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("expected a non-zero exit status")
	}
	return err
}

func TestHistorySearchUsesRipgrepAndStripsANSI(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("\x1b[31m12:ERROR\x1b[0m boot failed\n15:ready\n"), nil
	}
	res, err := HistorySearch(context.Background(), "transcript.txt", "ERROR|ready", runner, 0, 0)
	if err != nil {
		t.Fatalf("HistorySearch: %v", err)
	}
	if gotName != "rg" || res.Backend != HistorySearchRipgrep {
		t.Fatalf("backend = %q, command = %q; want rg", res.Backend, gotName)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--color=never") {
		t.Fatalf("rg args = %v; want --color=never", gotArgs)
	}
	if len(res.Matches) != 2 || strings.Contains(strings.Join(res.Matches, ""), "\x1b") {
		t.Fatalf("matches = %q; want two ANSI-free lines", res.Matches)
	}
	if res.Truncated {
		t.Fatal("unexpected truncation")
	}
}

func TestHistorySearchFallsBackToGrepWhenRipgrepAbsent(t *testing.T) {
	grepCalled := false
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "rg":
			return nil, exec.ErrNotFound
		case "grep":
			grepCalled = true
			if !strings.Contains(strings.Join(args, " "), "-E") {
				t.Fatalf("grep args = %v; want -E", args)
			}
			return []byte("7:needle found\n"), nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}
	res, err := HistorySearch(context.Background(), "chat.txt", "needle", runner, 0, 0)
	if err != nil {
		t.Fatalf("HistorySearch: %v", err)
	}
	if !grepCalled || res.Backend != HistorySearchGrep {
		t.Fatalf("backend = %q, grepCalled = %v; want grep -E", res.Backend, grepCalled)
	}
	if len(res.Matches) != 1 || !strings.Contains(res.Matches[0], "needle") {
		t.Fatalf("matches = %q", res.Matches)
	}
}

func TestHistorySearchNoMatchDoesNotFallBack(t *testing.T) {
	exitOne := historyExitOne(t)
	grepCalled := false
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "rg" {
			return nil, exitOne
		}
		grepCalled = true
		return []byte("unexpected\n"), nil
	}
	res, err := HistorySearch(context.Background(), "transcript.txt", "absent", runner, 0, 0)
	if err != nil {
		t.Fatalf("HistorySearch: %v", err)
	}
	if grepCalled {
		t.Fatal("grep must not run when rg is present but finds nothing")
	}
	if res.Backend != HistorySearchRipgrep || len(res.Matches) != 0 {
		t.Fatalf("result = %+v; want an empty rg result", res)
	}
}

func TestHistorySearchLimitsLinesAndBytes(t *testing.T) {
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var b strings.Builder
		for i := 0; i < 50; i++ {
			fmt.Fprintf(&b, "%d:%s\n", i, strings.Repeat("x", 40))
		}
		return []byte(b.String()), nil
	}
	byLines, err := HistorySearch(context.Background(), "transcript.txt", "x", runner, 2, 0)
	if err != nil {
		t.Fatalf("HistorySearch by lines: %v", err)
	}
	if len(byLines.Matches) != 2 || !byLines.Truncated {
		t.Fatalf("by lines = %+v; want two lines and truncation", byLines)
	}
	byBytes, err := HistorySearch(context.Background(), "transcript.txt", "x", runner, 0, 100)
	if err != nil {
		t.Fatalf("HistorySearch by bytes: %v", err)
	}
	if len(byBytes.Matches) != 2 || !byBytes.Truncated {
		t.Fatalf("by bytes = %+v; want two lines within 100 bytes and truncation", byBytes)
	}
}

func TestHistorySearchRejectsEmptyPattern(t *testing.T) {
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatal("runner must not run with an empty pattern")
		return nil, nil
	}
	if _, err := HistorySearch(context.Background(), "transcript.txt", "", runner, 0, 0); err == nil {
		t.Fatal("empty pattern must fail before any search runs")
	}
}
