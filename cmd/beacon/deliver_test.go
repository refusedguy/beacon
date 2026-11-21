package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// promptCall is one recorded call into a stand-in deliveryTarget.
type promptCall struct {
	sessionID string
	text      string
}

// scriptTarget is a stand-in deliveryTarget. It records every Prompt call and
// returns one scripted error per call, or nil when the script runs out. It
// performs no I/O and builds no HTTP client.
type scriptTarget struct {
	results []error
	calls   []promptCall
}

func (s *scriptTarget) Prompt(sessionID, text string) error {
	s.calls = append(s.calls, promptCall{sessionID: sessionID, text: text})
	i := len(s.calls) - 1
	if i < len(s.results) {
		return s.results[i]
	}
	return nil
}

// withDeliverDeps installs stand-ins for the snapshot, the target and the
// retry sleeper, and restores the previous values when the test ends.
func withDeliverDeps(t *testing.T, snap mcp.StateSnapshot, snapErr error, target deliveryTarget, sleep func(time.Duration)) {
	t.Helper()
	oldSnap, oldTarget, oldSleep := deliverSnapshot, deliverTarget, deliverSleep
	t.Cleanup(func() {
		deliverSnapshot, deliverTarget, deliverSleep = oldSnap, oldTarget, oldSleep
	})
	deliverSnapshot = func() (mcp.StateSnapshot, error) { return snap, snapErr }
	deliverTarget = target
	if sleep == nil {
		sleep = func(time.Duration) {}
	}
	deliverSleep = sleep
}

func deliverWatch(id string, verdict domain.Verdict, subscribers ...domain.Subscriber) domain.Watch {
	return domain.Watch{
		ID:          id,
		Verdict:     verdict,
		Revision:    7,
		Subscribers: subscribers,
	}
}

func deliverSub(role domain.Role, target string) domain.Subscriber {
	return domain.Subscriber{ID: string(role), Role: role, Target: target}
}

// deliverView projects one test watch through the same boundary the command
// uses, so helpers are tested on the local type rather than a domain type.
func deliverView(w domain.Watch) deliverWatchView {
	return deliverViews(mcp.StateSnapshot{ActiveWatches: []domain.Watch{w}})[0]
}

// ---------------------------------------------------------------------------
// The delivery target port and the honest ADR 0026 outcome.
// ---------------------------------------------------------------------------

func TestSetDeliveryTarget(t *testing.T) {
	old := deliverTarget
	t.Cleanup(func() { deliverTarget = old })

	tgt := &scriptTarget{}
	SetDeliveryTarget(tgt)
	if deliverTarget != tgt {
		t.Fatal("SetDeliveryTarget did not install the target")
	}
	SetDeliveryTarget(nil)
	if deliverTarget != nil {
		t.Fatal("SetDeliveryTarget(nil) did not clear the target")
	}
}

func TestDeliverPromptSteer(t *testing.T) {
	tgt := &scriptTarget{}
	out, attempts := deliverPrompt(tgt, "session-A", "pointer", func(time.Duration) {})
	if out != outcomeSteer || attempts != 1 {
		t.Fatalf("outcome/attempts = %s/%d, want steer/1", out, attempts)
	}
	if deliveryOf(out) != deliverySteer || !delivered(out) {
		t.Fatalf("a nil Prompt error must be an honest steer delivery, got %s", deliveryOf(out))
	}
	if len(tgt.calls) != 1 || tgt.calls[0].sessionID != "session-A" || tgt.calls[0].text != "pointer" {
		t.Fatalf("calls = %+v, want one call with session-A/pointer", tgt.calls)
	}
}

func TestDeliverPromptQueueIsNotSteer(t *testing.T) {
	tgt := &scriptTarget{results: []error{fmt.Errorf("session busy: %w", errPromptQueued)}}
	out, attempts := deliverPrompt(tgt, "session-A", "pointer", func(time.Duration) {})
	if out != outcomeQueue || attempts != 1 {
		t.Fatalf("outcome/attempts = %s/%d, want queue/1", out, attempts)
	}
	if deliveryOf(out) != deliveryQueue {
		t.Fatalf("delivery = %s, want queue", deliveryOf(out))
	}
	if !delivered(out) {
		t.Fatal("queue is an honest delivery and must be reported as one")
	}
	if deliveryOf(out) == deliverySteer {
		t.Fatal("a queued follow-up must never be printed as a current-turn steer")
	}
}

// TestDeliverUnsupportedIsNotSuccess is the ADR 0026 guard: an environment
// that cannot deliver to an open session must not be reported as success.
func TestDeliverUnsupportedIsNotSuccess(t *testing.T) {
	tgt := &scriptTarget{results: []error{fmt.Errorf("no open session: %w", errPromptUnsupported)}}
	out, attempts := deliverPrompt(tgt, "session-A", "pointer", func(time.Duration) {})
	if out != outcomeUnsupported || attempts != 1 {
		t.Fatalf("outcome/attempts = %s/%d, want unsupported/1", out, attempts)
	}
	if deliveryOf(out) != deliveryUnsupported {
		t.Fatalf("delivery = %s, want unsupported", deliveryOf(out))
	}
	if delivered(out) {
		t.Fatal("unsupported must never count as a delivered follow-up")
	}
}

func TestDeliverNoTargetIsUnsupported(t *testing.T) {
	out, attempts := deliverPrompt(nil, "session-A", "pointer", func(time.Duration) {})
	if out != outcomeUnsupported || attempts != 1 {
		t.Fatalf("outcome/attempts = %s/%d, want unsupported/1", out, attempts)
	}
	if delivered(out) || deliveryOf(out) != deliveryUnsupported {
		t.Fatal("a missing target must be an honest unsupported delivery")
	}
}

// ---------------------------------------------------------------------------
// ADR 0017: retry only transport-failed, with the same pointer, cap three.
// ---------------------------------------------------------------------------

func TestDeliverRetryOnlyTransportFailed(t *testing.T) {
	tgt := &scriptTarget{results: []error{
		errTransportFailed,
		fmt.Errorf("second try: %w", errTransportFailed),
		nil,
	}}
	var sleeps []time.Duration
	out, attempts := deliverPrompt(tgt, "session-A", "pointer@obligation-1", func(d time.Duration) {
		sleeps = append(sleeps, d)
	})
	if out != outcomeSteer || attempts != deliverMaxAttempts {
		t.Fatalf("outcome/attempts = %s/%d, want steer/%d", out, attempts, deliverMaxAttempts)
	}
	if len(tgt.calls) != deliverMaxAttempts {
		t.Fatalf("Prompt called %d times, want %d", len(tgt.calls), deliverMaxAttempts)
	}
	for i, call := range tgt.calls {
		if call.sessionID != "session-A" || call.text != "pointer@obligation-1" {
			t.Fatalf("call %d = %+v, retry changed the pointer or session", i, call)
		}
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleep %d = %v, want %v", i, sleeps[i], want[i])
		}
	}
}

func TestDeliverRetryCap(t *testing.T) {
	tgt := &scriptTarget{results: []error{errTransportFailed, errTransportFailed, errTransportFailed, nil}}
	sleeps := 0
	out, attempts := deliverPrompt(tgt, "session-A", "pointer", func(time.Duration) { sleeps++ })
	if out != outcomeTransportFailed || attempts != deliverMaxAttempts {
		t.Fatalf("outcome/attempts = %s/%d, want transport-failed/%d", out, attempts, deliverMaxAttempts)
	}
	if len(tgt.calls) != deliverMaxAttempts || sleeps != deliverMaxAttempts-1 {
		t.Fatalf("calls/sleeps = %d/%d, want %d/%d", len(tgt.calls), sleeps, deliverMaxAttempts, deliverMaxAttempts-1)
	}
	if delivered(out) || deliveryOf(out) != deliveryUnsupported {
		t.Fatal("an exhausted transport-failed retry must not be a delivery")
	}
}

func TestDeliverNoRetryOnUncertainOutcomes(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"posted receipt", errPromptPosted},
		{"indeterminate", fmt.Errorf("lost reply: %w", errIndeterminate)},
		{"unknown error", errors.New("unexpected reply")},
		{"unsupported", errPromptUnsupported},
		{"queued", errPromptQueued},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt := &scriptTarget{results: []error{tc.err}}
			sleeps := 0
			out, attempts := deliverPrompt(tgt, "session-A", "pointer", func(time.Duration) { sleeps++ })
			if attempts != 1 || len(tgt.calls) != 1 || sleeps != 0 {
				t.Fatalf("attempts/calls/sleeps = %d/%d/%d, want 1/1/0", attempts, len(tgt.calls), sleeps)
			}
			if retryable(out) {
				t.Fatalf("outcome %s must never be retried", out)
			}
			if out == outcomeSteer {
				t.Fatalf("outcome %s must not be a delivered steer", out)
			}
		})
	}
}

func TestDeliverTextCarriesStableObligation(t *testing.T) {
	w := deliverView(deliverWatch("w-1", domain.VerdictReady))
	first := deliverText(w, "session-A")
	second := deliverText(w, "session-A")
	if first != second {
		t.Fatal("the same watch and session produced different pointers")
	}
	wantObligation := domain.ObligationKey(w.id, w.revision, "session-A")
	if !strings.Contains(first, wantObligation) {
		t.Fatalf("pointer %q does not carry obligation %q", first, wantObligation)
	}
	if !strings.Contains(first, "watch=w-1") || !strings.Contains(first, "verdict=ready") {
		t.Fatalf("pointer %q does not name the watch and verdict", first)
	}
	changed := deliverText(deliverWatchView{id: "w-1", verdict: "ready", revision: 8}, "session-A")
	if changed == first {
		t.Fatal("a new revision must produce a new pointer")
	}
}

// ---------------------------------------------------------------------------
// Watch and subscriber selection.
// ---------------------------------------------------------------------------

func TestDeliverSubscriberSelection(t *testing.T) {
	exec := deliverSub(domain.RoleExecutor, "exec-session")
	orch := deliverSub(domain.RoleOrchestrator, "orch-session")

	ready := deliverView(deliverWatch("w-1", domain.VerdictReady, exec, orch))
	if got, ok := deliverySubscriber(ready); !ok || got.target != "orch-session" {
		t.Fatalf("ready subscriber = %+v/%v, want orchestrator", got, ok)
	}
	needs := deliverView(deliverWatch("w-2", domain.VerdictNeedsInput, exec, orch))
	if got, ok := deliverySubscriber(needs); !ok || got.target != "exec-session" {
		t.Fatalf("needs_input subscriber = %+v/%v, want executor", got, ok)
	}
	onlyExec := deliverView(deliverWatch("w-3", domain.VerdictReady, exec))
	if got, ok := deliverySubscriber(onlyExec); !ok || got.target != "exec-session" {
		t.Fatalf("fallback subscriber = %+v/%v, want the only live session", got, ok)
	}
	none := deliverView(deliverWatch("w-4", domain.VerdictReady, deliverSub(domain.RoleExecutor, "")))
	if _, ok := deliverySubscriber(none); ok {
		t.Fatal("a watch with no live session must not yield a delivery target")
	}
}

func TestFindDeliverWatch(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-1", domain.VerdictReady),
		deliverWatch("w-2", domain.VerdictNeedsInput),
	}}
	views := deliverViews(snap)
	if w, ok := findDeliverWatch(views, "w-2"); !ok || w.id != "w-2" {
		t.Fatalf("findDeliverWatch = %+v/%v, want w-2", w, ok)
	}
	if _, ok := findDeliverWatch(views, "missing"); ok {
		t.Fatal("a missing watch must not be found")
	}
	if actionableVerdict(string(domain.VerdictWatching)) ||
		!actionableVerdict(string(domain.VerdictReady)) ||
		!actionableVerdict(string(domain.VerdictNeedsInput)) {
		t.Fatal("only ready and needs_input are actionable verdicts")
	}
}

// ---------------------------------------------------------------------------
// Command-level checks with a stand-in snapshot and target.
// ---------------------------------------------------------------------------

func runDeliverWith(t *testing.T, args []string, snap mcp.StateSnapshot, snapErr error, target deliveryTarget) (int, string, string) {
	t.Helper()
	withDeliverDeps(t, snap, snapErr, target, nil)
	var out, errOut bytes.Buffer
	code := runDeliver(&CLI{out: &out, errOut: &errOut}, args)
	return code, out.String(), errOut.String()
}

func TestRunDeliverReadySteer(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-1", domain.VerdictReady,
			deliverSub(domain.RoleExecutor, "exec-session"),
			deliverSub(domain.RoleOrchestrator, "orch-session")),
	}}
	tgt := &scriptTarget{}
	code, out, errOut := runDeliverWith(t, []string{"w-1"}, snap, nil, tgt)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q, want 0", code, errOut)
	}
	if !strings.Contains(out, "watch=w-1") || !strings.Contains(out, "delivery=steer") {
		t.Fatalf("stdout = %q, want watch and delivery=steer", out)
	}
	if len(tgt.calls) != 1 || tgt.calls[0].sessionID != "orch-session" {
		t.Fatalf("calls = %+v, want one write to orch-session", tgt.calls)
	}
	wantObligation := domain.ObligationKey("w-1", 7, "orch-session")
	if !strings.Contains(tgt.calls[0].text, wantObligation) {
		t.Fatalf("pointer %q does not carry obligation %q", tgt.calls[0].text, wantObligation)
	}
}

func TestRunDeliverNeedsInputQueue(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-2", domain.VerdictNeedsInput,
			deliverSub(domain.RoleExecutor, "exec-session")),
	}}
	tgt := &scriptTarget{results: []error{fmt.Errorf("busy: %w", errPromptQueued)}}
	code, out, errOut := runDeliverWith(t, []string{"w-2"}, snap, nil, tgt)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q, want 0 for a queued follow-up", code, errOut)
	}
	if !strings.Contains(out, "delivery=queue") {
		t.Fatalf("stdout = %q, want delivery=queue", out)
	}
	if strings.Contains(out, "delivery=steer") {
		t.Fatalf("stdout = %q, queue was printed as steer", out)
	}
	if len(tgt.calls) != 1 || tgt.calls[0].sessionID != "exec-session" {
		t.Fatalf("calls = %+v, want one write to exec-session", tgt.calls)
	}
}

// TestRunDeliverUnsupportedExitsNonZero checks the command-level half of the
// ADR 0026 guarantee: an unsupported delivery is not a successful exit.
func TestRunDeliverUnsupportedExitsNonZero(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-3", domain.VerdictReady, deliverSub(domain.RoleOrchestrator, "orch-session")),
	}}
	tgt := &scriptTarget{results: []error{fmt.Errorf("no open session: %w", errPromptUnsupported)}}
	code, out, errOut := runDeliverWith(t, []string{"w-3"}, snap, nil, tgt)
	if code == 0 {
		t.Fatalf("unsupported delivery exited 0, stdout = %q", out)
	}
	if !strings.Contains(out, "delivery=unsupported") {
		t.Fatalf("stdout = %q, want delivery=unsupported", out)
	}
	if errOut == "" {
		t.Fatal("an unsupported delivery must state the reason on stderr")
	}
}

func TestRunDeliverNoSessionIsUnsupported(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-4", domain.VerdictReady, deliverSub(domain.RoleOrchestrator, "")),
	}}
	tgt := &scriptTarget{}
	code, out, errOut := runDeliverWith(t, []string{"w-4"}, snap, nil, tgt)
	if code == 0 || !strings.Contains(out, "delivery=unsupported") {
		t.Fatalf("exit/stdout = %d/%q, want non-zero and delivery=unsupported", code, out)
	}
	if len(tgt.calls) != 0 {
		t.Fatalf("a missing session must not be prompted, got %+v", tgt.calls)
	}
	if errOut == "" {
		t.Fatal("a missing session must be explained on stderr")
	}
}

func TestRunDeliverNoDeliveryForOtherVerdict(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-5", domain.VerdictWatching, deliverSub(domain.RoleOrchestrator, "orch-session")),
	}}
	tgt := &scriptTarget{}
	code, out, errOut := runDeliverWith(t, []string{"w-5"}, snap, nil, tgt)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q, want 0 for a no-op verdict", code, errOut)
	}
	if len(tgt.calls) != 0 {
		t.Fatalf("a watching verdict must not be written, got %+v", tgt.calls)
	}
	if !strings.Contains(out, "доставка не требуется") {
		t.Fatalf("stdout = %q, want a no-op message", out)
	}
}

func TestRunDeliverErrors(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-6", domain.VerdictReady, deliverSub(domain.RoleOrchestrator, "orch-session")),
	}}
	tgt := &scriptTarget{}

	if code, _, errOut := runDeliverWith(t, nil, snap, nil, tgt); code != 2 || errOut == "" {
		t.Fatalf("no args: exit/stderr = %d/%q, want 2 and a message", code, errOut)
	}
	if code, _, errOut := runDeliverWith(t, []string{"w-6", "extra"}, snap, nil, tgt); code != 2 || errOut == "" {
		t.Fatalf("extra args: exit/stderr = %d/%q, want 2 and a message", code, errOut)
	}
	if code, _, errOut := runDeliverWith(t, []string{"missing"}, snap, nil, tgt); code != 2 || errOut == "" {
		t.Fatalf("missing watch: exit/stderr = %d/%q, want 2 and a message", code, errOut)
	}
	if code, _, errOut := runDeliverWith(t, []string{"w-6"}, snap, errors.New("store down"), tgt); code != 2 || errOut == "" {
		t.Fatalf("snapshot error: exit/stderr = %d/%q, want 2 and a message", code, errOut)
	}
	if len(tgt.calls) != 0 {
		t.Fatalf("no error case should write a follow-up, got %+v", tgt.calls)
	}
}

// ---------------------------------------------------------------------------
// Pi RPC adapter with an injected provider (T3.217).
// ---------------------------------------------------------------------------

type mockPiProbe struct {
	methods     []string
	sessions    []string
	texts       []string
	deadlines   []bool
	followUpErr error
	steerErr    error
}

func (m *mockPiProbe) record(method string, ctx context.Context, sessionID, text string) {
	m.methods = append(m.methods, method)
	m.sessions = append(m.sessions, sessionID)
	m.texts = append(m.texts, text)
	_, ok := ctx.Deadline()
	m.deadlines = append(m.deadlines, ok)
}

func (m *mockPiProbe) Prompt(ctx context.Context, sessionID, text string) error {
	m.record("prompt", ctx, sessionID, text)
	return nil
}

func (m *mockPiProbe) Steer(ctx context.Context, sessionID, text string) error {
	m.record("steer", ctx, sessionID, text)
	return m.steerErr
}

func (m *mockPiProbe) FollowUp(ctx context.Context, sessionID, text string) error {
	m.record("followUp", ctx, sessionID, text)
	return m.followUpErr
}

func (m *mockPiProbe) Abort(ctx context.Context, sessionID string) error {
	m.record("abort", ctx, sessionID, "")
	return nil
}

func (m *mockPiProbe) GetState(ctx context.Context, sessionID string) (string, error) {
	m.record("get_state", ctx, sessionID, "")
	return "processing", nil
}

func (m *mockPiProbe) WaitForIdle(ctx context.Context, sessionID string) error {
	m.record("waitForIdle", ctx, sessionID, "")
	return nil
}

var _ wake.PiProbe = (*mockPiProbe)(nil)

type mockPiConn struct {
	methods []string
	calls   []wake.PiRPCCall
	reply   wake.PiRPCReply
	err     error
}

func (c *mockPiConn) Call(_ context.Context, method string, call wake.PiRPCCall) (wake.PiRPCReply, error) {
	c.methods = append(c.methods, method)
	c.calls = append(c.calls, call)
	if c.err != nil {
		return wake.PiRPCReply{}, c.err
	}
	return c.reply, nil
}

func (c *mockPiConn) Close() error { return nil }

var _ wake.PiRPCConnection = (*mockPiConn)(nil)

func withPiConfig(t *testing.T, adapter, mode, timeout string) {
	t.Helper()
	t.Setenv(deliverAdapterEnv, adapter)
	t.Setenv(deliverPiModeEnv, mode)
	t.Setenv(deliverPiTimeoutEnv, timeout)
}

func withPiProbe(t *testing.T, probe wake.PiProbe) {
	t.Helper()
	old := resolvePiProbe
	t.Cleanup(func() { resolvePiProbe = old })
	resolvePiProbe = func(deliverAdapterConfig) (wake.PiProbe, error) { return probe, nil }
}

func piDeliverSnapshot() mcp.StateSnapshot {
	return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
		deliverWatch("w-pi", domain.VerdictNeedsInput, deliverSub(domain.RoleExecutor, "pi-session")),
	}}
}

func TestPiRPCProbeUsesInjectedConnection(t *testing.T) {
	conn := &mockPiConn{reply: wake.PiRPCReply{State: "processing", MessageID: "msg-1"}}
	probe := &wake.PiRPCProbe{Conn: conn}

	if err := probe.FollowUp(context.Background(), "pi-session", "pointer"); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	state, err := probe.GetState(context.Background(), "pi-session")
	if err != nil || state != "processing" {
		t.Fatalf("GetState = %q/%v, want processing/nil", state, err)
	}
	if len(conn.methods) != 2 || conn.methods[0] != "followUp" || conn.methods[1] != "get_state" {
		t.Fatalf("methods = %v, want followUp then get_state", conn.methods)
	}
	if conn.calls[0].SessionID != "pi-session" || conn.calls[0].Text != "pointer" {
		t.Fatalf("call = %+v, want pi-session/pointer", conn.calls[0])
	}
}

func TestPiRPCProbeReportsReplyError(t *testing.T) {
	probe := &wake.PiRPCProbe{Conn: &mockPiConn{reply: wake.PiRPCReply{Error: "session closed"}}}
	if err := probe.Steer(context.Background(), "pi-session", "pointer"); err == nil || !strings.Contains(err.Error(), "session closed") {
		t.Fatalf("Steer error = %v, want the transport reply error", err)
	}
}

func TestDeliverPiRPCSuccess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		method   string
		delivery string
	}{
		{name: "queue by default", method: "followUp", delivery: "queue"},
		{name: "steer mode", mode: "steer", method: "steer", delivery: "steer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &mockPiProbe{}
			withPiConfig(t, "pi", tc.mode, "2s")
			withPiProbe(t, probe)

			code, out, errOut := runDeliverWith(t, []string{"w-pi"}, piDeliverSnapshot(), nil, nil)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %q, want 0", code, errOut)
			}
			if !strings.Contains(out, "delivery="+tc.delivery) || !strings.Contains(out, "outcome="+tc.delivery) {
				t.Fatalf("stdout = %q, want %s delivery and outcome", out, tc.delivery)
			}
			if len(probe.methods) != 1 || probe.methods[0] != tc.method {
				t.Fatalf("methods = %v, want one %s call", probe.methods, tc.method)
			}
			wantKey := domain.ObligationKey("w-pi", 7, "pi-session")
			if probe.sessions[0] != "pi-session" || !strings.Contains(probe.texts[0], wantKey) {
				t.Fatalf("call = %q/%q, want pi-session and obligation %q", probe.sessions[0], probe.texts[0], wantKey)
			}
			if !probe.deadlines[0] {
				t.Fatal("the pi probe did not receive the configured timeout")
			}
		})
	}
}

func TestDeliverPiRPCErrorIsNotDelivery(t *testing.T) {
	probe := &mockPiProbe{followUpErr: errors.New("rpc failed")}
	withPiConfig(t, "pi", "", "")
	withPiProbe(t, probe)

	code, out, errOut := runDeliverWith(t, []string{"w-pi"}, piDeliverSnapshot(), nil, nil)
	if code == 0 {
		t.Fatalf("a failed rpc exited 0, stdout = %q", out)
	}
	if !strings.Contains(out, "outcome=indeterminate") || !strings.Contains(out, "delivery=unsupported") {
		t.Fatalf("stdout = %q, want indeterminate outcome and unsupported delivery", out)
	}
	if len(probe.methods) != 1 {
		t.Fatalf("methods = %v, want one call without retries", probe.methods)
	}
	if errOut == "" {
		t.Fatal("a failed rpc must explain the reason on stderr")
	}
}

func TestDeliverPiRPCTimeoutRetriesThenUnsupported(t *testing.T) {
	probe := &mockPiProbe{followUpErr: context.DeadlineExceeded}
	withPiConfig(t, "pi", "", "1ms")
	withPiProbe(t, probe)

	code, out, errOut := runDeliverWith(t, []string{"w-pi"}, piDeliverSnapshot(), nil, nil)
	if code == 0 {
		t.Fatalf("a timed out rpc exited 0, stdout = %q", out)
	}
	if !strings.Contains(out, "outcome=transport-failed") ||
		!strings.Contains(out, "attempts=3") ||
		!strings.Contains(out, "delivery=unsupported") {
		t.Fatalf("stdout = %q, want transport-failed, three attempts and unsupported", out)
	}
	if len(probe.methods) != deliverMaxAttempts {
		t.Fatalf("methods = %v, want %d attempts", probe.methods, deliverMaxAttempts)
	}
	if !probe.deadlines[0] {
		t.Fatal("the pi probe did not receive the configured timeout")
	}
	if errOut == "" {
		t.Fatal("a timed out rpc must explain the reason on stderr")
	}
}

func TestDeliverPiKeepsCurrentTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter string
		mode    string
	}{
		{name: "no adapter"},
		{name: "other adapter", adapter: "opencode"},
		{name: "bad mode", adapter: "pi", mode: "bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &mockPiProbe{}
			withPiConfig(t, tc.adapter, tc.mode, "")
			withPiProbe(t, probe)

			tgt := &scriptTarget{}
			code, out, errOut := runDeliverWith(t, []string{"w-pi"}, piDeliverSnapshot(), nil, tgt)
			if code != 0 || !strings.Contains(out, "delivery=steer") {
				t.Fatalf("exit/stdout/stderr = %d/%q/%q, want the current target to steer", code, out, errOut)
			}
			if len(tgt.calls) != 1 {
				t.Fatalf("target calls = %+v, want one call", tgt.calls)
			}
			if len(probe.methods) != 0 {
				t.Fatalf("pi probe was called despite the fallback: %v", probe.methods)
			}
		})
	}
}

func TestDeliverPiWithoutProviderIsUnsupported(t *testing.T) {
	withPiConfig(t, "pi", "", "")
	old := resolvePiProbe
	t.Cleanup(func() { resolvePiProbe = old })
	resolvePiProbe = func(deliverAdapterConfig) (wake.PiProbe, error) { return nil, nil }

	code, out, errOut := runDeliverWith(t, []string{"w-pi"}, piDeliverSnapshot(), nil, nil)
	if code == 0 || !strings.Contains(out, "delivery=unsupported") {
		t.Fatalf("exit/stdout/stderr = %d/%q/%q, want honest unsupported", code, out, errOut)
	}
}
