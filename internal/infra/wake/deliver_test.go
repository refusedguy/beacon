package wake

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitState polls until the delivery reaches want or the deadline passes.
func waitState(t *testing.T, d *Deliverer, id DeliveryId, want Verdict) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d.State(id) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state of %s = %q, want %q", id, d.State(id), want)
}

// waitEvent waits for an event of the given type carrying id.
func waitEvent(t *testing.T, d *Deliverer, typ EventType, id DeliveryId) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case e := <-d.Events():
			if e.Type == typ && e.DeliveryId == id {
				return
			}
		case <-deadline:
			t.Fatalf("no %s event for %s", typ, id)
		}
	}
}

// TestAcceptReturns202BeforeWork proves the call does not wait for the
// background job. The work blocks until the test releases it, yet Accept must
// have returned with 202 already.
func TestAcceptReturns202BeforeWork(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	work := func(ctx context.Context, req Request, id DeliveryId) {
		close(started)
		<-release
	}
	d := New(work)
	req := Request{Target: "session-1", Payload: "obligation-1@7", Mode: Hosted}

	done := make(chan Accepted, 1)
	go func() { done <- d.Accept(context.Background(), req) }()

	var got Accepted
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("Accept blocked on background work")
	}
	if got.Status != AcceptedStatus {
		t.Errorf("status = %d, want 202", got.Status)
	}
	if got.DeliveryId == "" {
		t.Error("deliveryId is empty")
	}
	<-started
	close(release)
}

// TestAcceptedHasNoResultField is the acceptance core: the 202 response has
// only status and deliveryId, so a caller cannot read finality from it.
func TestAcceptedHasNoResultField(t *testing.T) {
	d := New(nil)
	got := d.Accept(context.Background(), Request{Target: "s"})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 2 {
		t.Errorf("accepted fields = %v, want only status and deliveryId", fields)
	}
	for _, forbidden := range []string{"result", "verdict", "outcome", "done", "finished"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("accepted response carries %q; 202 must not contain a result", forbidden)
		}
	}
	if !strings.Contains(string(raw), `"status":202`) {
		t.Errorf("response = %s, want status 202", raw)
	}
}

// TestWorkRunsInBackgroundAndPublishesEvents proves the admitted delivery
// publishes hints after the 202 returned. The events carry the deliveryId.
func TestWorkRunsInBackgroundAndPublishesEvents(t *testing.T) {
	d := New(func(ctx context.Context, req Request, id DeliveryId) {})
	got := d.Accept(context.Background(), Request{Target: "session-1", Mode: Local})
	waitEvent(t, d, AcceptedEvent, got.DeliveryId)
	waitEvent(t, d, DeliveredEvent, got.DeliveryId)
}

// TestVerdictComesFromSeparateChannel proves no verdict appears with the 202
// and that a decision arrives only on the verdict channel.
func TestVerdictComesFromSeparateChannel(t *testing.T) {
	d := New(nil)
	got := d.Accept(context.Background(), Request{Target: "s"})
	waitState(t, d, got.DeliveryId, Pending)
	select {
	case v := <-d.Verdicts():
		t.Fatalf("verdict %q appeared before any decision", v)
	default:
	}
	d.Decide(got.DeliveryId, Ready)
	select {
	case v := <-d.Verdicts():
		if v != Ready {
			t.Errorf("verdict = %q, want ready", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no verdict on the verdict channel")
	}
	waitState(t, d, got.DeliveryId, Ready)
}

// TestFinalStatusIsNotDerivedFrom202 proves the 202 never completes a task.
// Only an explicit ready verdict moves the state off pending.
func TestFinalStatusIsNotDerivedFrom202(t *testing.T) {
	for _, mode := range []Mode{Hosted, Local} {
		t.Run(string(mode), func(t *testing.T) {
			d := New(nil)
			got := d.Accept(context.Background(), Request{Target: "s", Mode: mode})
			if v := d.State(got.DeliveryId); v != Pending {
				t.Errorf("after 202 state = %q, want pending", v)
			}
			select {
			case v := <-d.Verdicts():
				t.Fatalf("after 202 a verdict %q was published", v)
			default:
			}
			d.Decide(got.DeliveryId, Ready)
			if v := d.State(got.DeliveryId); v != Ready {
				t.Errorf("after verdict state = %q, want ready", v)
			}
		})
	}
}

// TestAgentSignalsDoNotFinishTask is the acceptance core: FINISHED and IDLE
// (Cursor) and agent_end (Pi) are not final. Only verdict=ready finishes.
func TestAgentSignalsDoNotFinishTask(t *testing.T) {
	for _, mode := range []Mode{Hosted, Local} {
		t.Run(string(mode), func(t *testing.T) {
			d := New(nil)
			got := d.Accept(context.Background(), Request{Target: "s", Mode: mode})
			id := got.DeliveryId
			for _, signal := range []AgentSignal{Finished, Idle, AgentEnd} {
				if v := d.Observe(id, signal); v != Pending {
					t.Errorf("after %s state = %q, want pending", signal, v)
				}
				select {
				case v := <-d.Verdicts():
					t.Fatalf("after %s a verdict %q was published", signal, v)
				default:
				}
			}
			d.Decide(id, Ready)
			if v := d.State(id); v != Ready {
				t.Errorf("after verdict state = %q, want ready", v)
			}
		})
	}
}

// TestNonReadyVerdictKeepsTaskOpen proves a verdict that is not ready does not
// finish the task either.
func TestNonReadyVerdictKeepsTaskOpen(t *testing.T) {
	d := New(nil)
	got := d.Accept(context.Background(), Request{Target: "s"})
	d.Decide(got.DeliveryId, Pending)
	if v := d.State(got.DeliveryId); v != Pending {
		t.Errorf("after pending verdict state = %q, want pending", v)
	}
	select {
	case v := <-d.Verdicts():
		t.Fatalf("pending verdict published %q on the decision channel", v)
	default:
	}
}

// TestHostedAndLocalBehaveTheSame proves the hosting mode changes neither the
// acknowledgement nor the finality rule (ADR 0082).
func TestHostedAndLocalBehaveTheSame(t *testing.T) {
	run := func(mode Mode) (Accepted, Verdict) {
		d := New(nil)
		got := d.Accept(context.Background(), Request{Target: "s", Mode: mode})
		d.Decide(got.DeliveryId, Ready)
		return got, d.State(got.DeliveryId)
	}
	hosted, hostedState := run(Hosted)
	local, localState := run(Local)
	if hosted.Status != local.Status {
		t.Errorf("status hosted=%d local=%d, want equal", hosted.Status, local.Status)
	}
	if hostedState != localState {
		t.Errorf("state hosted=%q local=%q, want equal", hostedState, localState)
	}
	if hosted.Status != AcceptedStatus || hostedState != Ready {
		t.Errorf("unexpected result: ack=%+v state=%q", hosted, hostedState)
	}
}

// TestDuplicateDeliveryIdStartsOneJob closes the idempotence loop from ADR
// 0081: a repeat with the same deliveryId reuses the acknowledgement and does
// not start a second background job.
func TestDuplicateDeliveryIdStartsOneJob(t *testing.T) {
	var runs atomic.Int32
	d := New(func(ctx context.Context, req Request, id DeliveryId) { runs.Add(1) })
	req := Request{Target: "s", DeliveryId: DeliveryId("dlv_fixed")}
	first := d.Accept(context.Background(), req)
	second := d.Accept(context.Background(), req)
	if first != second {
		t.Errorf("duplicate ack = %+v, want %+v", second, first)
	}
	deadline := time.Now().Add(time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != 1 {
		t.Errorf("background runs = %d, want 1", runs.Load())
	}
}

// TestGeneratedDeliveryIdsAreDistinct proves two admissions without an
// explicit id get different keys, so their verdicts cannot cross over.
func TestGeneratedDeliveryIdsAreDistinct(t *testing.T) {
	d := New(nil)
	first := d.Accept(context.Background(), Request{Target: "s"})
	second := d.Accept(context.Background(), Request{Target: "s"})
	if first.DeliveryId == second.DeliveryId {
		t.Fatalf("both admissions got deliveryId %q", first.DeliveryId)
	}
	d.Decide(first.DeliveryId, Ready)
	if v := d.State(first.DeliveryId); v != Ready {
		t.Errorf("first state = %q, want ready", v)
	}
	if v := d.State(second.DeliveryId); v != Pending {
		t.Errorf("second state = %q, want pending", v)
	}
}
