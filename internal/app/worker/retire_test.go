package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

var _ RetirePorts = (*fakeRetirePorts)(nil)

// retireCall is one observed call into a fake port.
type retireCall struct {
	op        string
	effectKey string
	workerID  string
	reason    string
}

// fakeRetirePorts records calls and returns configured results. Each method
// appends in call order so tests can assert the retire sequence.
type fakeRetirePorts struct {
	calls []retireCall
	log   *[]string

	releaseErr   error
	heartbeatErr error
	completeErr  error
	publishErr   error

	released  bool
	completed bool
}

func (f *fakeRetirePorts) ReleaseLease(_ context.Context, effectKey, workerID string) (bool, error) {
	f.calls = append(f.calls, retireCall{op: "release", effectKey: effectKey, workerID: workerID})
	f.record("release")
	return f.released, f.releaseErr
}

func (f *fakeRetirePorts) CloseHeartbeat(_ context.Context, effectKey, workerID string) error {
	f.calls = append(f.calls, retireCall{op: "heartbeat", effectKey: effectKey, workerID: workerID})
	f.record("heartbeat")
	return f.heartbeatErr
}

func (f *fakeRetirePorts) MarkEffectComplete(_ context.Context, effectKey string) (bool, error) {
	f.calls = append(f.calls, retireCall{op: "complete", effectKey: effectKey})
	f.record("complete")
	return f.completed, f.completeErr
}

func (f *fakeRetirePorts) PublishRetire(_ context.Context, event RetireEvent) error {
	f.calls = append(f.calls, retireCall{
		op:        "publish",
		effectKey: event.EffectKey,
		workerID:  event.WorkerID,
		reason:    event.Reason,
	})
	f.record("publish")
	return f.publishErr
}

func (f *fakeRetirePorts) record(op string) {
	if f.log != nil {
		*f.log = append(*f.log, op)
	}
}

func liveRetireState() RetireState {
	return RetireState{
		EffectKey:     "effect-1",
		WorkerID:      "worker-1",
		LeaseHeld:     true,
		HeartbeatOpen: true,
		Status:        RetireStatusInProgress,
	}
}

func TestRetireSuccess(t *testing.T) {
	var log []string
	ports := &fakeRetirePorts{released: true, completed: true, log: &log}
	r := NewRetirer(ports)

	res, err := r.Retire(context.Background(), liveRetireState())
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if res.Noop {
		t.Fatal("retire reported noop on a live worker")
	}
	if !res.LeaseReleased || !res.HeartbeatClosed || !res.EffectCompleted || !res.EventPublished {
		t.Fatalf("result = %+v, want every cleanup action done", res)
	}
	wantOrder := []string{"release", "heartbeat", "complete", "publish"}
	if !reflect.DeepEqual(log, wantOrder) {
		t.Fatalf("order = %v, want %v", log, wantOrder)
	}
	if !res.State.Retired || res.State.Status != RetireStatusRetired {
		t.Fatalf("state = %+v, want retired", res.State)
	}
	if res.State.LeaseHeld || res.State.HeartbeatOpen {
		t.Fatalf("state = %+v, want lease and heartbeat cleared", res.State)
	}
	if got := ports.calls[3].reason; got != RetireReasonLost {
		t.Fatalf("event reason = %q, want %q", got, RetireReasonLost)
	}
}

func TestRetireRepeatIsNoop(t *testing.T) {
	ports := &fakeRetirePorts{released: true, completed: true}
	r := NewRetirer(ports)

	first, err := r.Retire(context.Background(), liveRetireState())
	if err != nil || !first.State.Retired {
		t.Fatalf("first retire = %+v, %v; want retired", first, err)
	}
	before := len(ports.calls)

	second, err := r.Retire(context.Background(), first.State)
	if err != nil {
		t.Fatalf("repeat retire: %v", err)
	}
	if !second.Noop {
		t.Fatal("repeat retire must be a no-op")
	}
	if len(ports.calls) != before {
		t.Fatalf("repeat called ports: %+v", ports.calls[before:])
	}
}

func TestRetireLeaseErrorAborts(t *testing.T) {
	boom := errors.New("lease store down")
	ports := &fakeRetirePorts{releaseErr: boom}
	r := NewRetirer(ports)

	res, err := r.Retire(context.Background(), liveRetireState())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	wantCalls := []retireCall{{op: "release", effectKey: "effect-1", workerID: "worker-1"}}
	if !reflect.DeepEqual(ports.calls, wantCalls) {
		t.Fatalf("calls = %+v, want only release", ports.calls)
	}
	if res.State.Retired || res.State.Status == RetireStatusRetired {
		t.Fatalf("state = %+v, want un-retired after a failed lease release", res.State)
	}
}

func TestRetireEmptyState(t *testing.T) {
	ports := &fakeRetirePorts{}
	r := NewRetirer(ports)

	res, err := r.Retire(context.Background(), RetireState{})
	if err != nil {
		t.Fatalf("retire on empty state: %v", err)
	}
	if !res.Noop {
		t.Fatal("empty state must be a no-op")
	}
	if len(ports.calls) != 0 {
		t.Fatalf("empty state called ports: %+v", ports.calls)
	}
}

func TestPlanRetireIsPure(t *testing.T) {
	in := liveRetireState()
	want := in

	plan, ok := PlanRetire(in)
	if !ok {
		t.Fatal("a live state must produce a plan")
	}
	if !plan.ReleaseLease || !plan.CloseHeartbeat || !plan.MarkComplete || !plan.PublishEvent {
		t.Fatalf("plan = %+v, want every action for a live worker", plan)
	}
	if in != want {
		t.Fatalf("PlanRetire mutated its input: got %+v, want %+v", in, want)
	}
}

func TestRetireNilPortsOnLiveState(t *testing.T) {
	r := NewRetirer(nil)
	res, err := r.Retire(context.Background(), liveRetireState())
	if !errors.Is(err, ErrNilRetirePorts) {
		t.Fatalf("error = %v, want %v", err, ErrNilRetirePorts)
	}
	if res.State.Retired {
		t.Fatal("state retired without ports")
	}
}
