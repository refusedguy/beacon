package orchestrator

import (
	"context"
	"errors"
	"testing"
)

// approvalEvent records the order of the approval lifecycle events. The order
// is what proves approval starts before the merge and finishes after it.
type approvalEvent struct {
	kind string // begin, merge, cancel or finish
	err  string
}

type fakeApprovalHandle struct {
	events    *[]approvalEvent
	cancels   int
	finishes  int
	finishErr string
}

func (h *fakeApprovalHandle) Cancel() {
	h.cancels++
	*h.events = append(*h.events, approvalEvent{kind: "cancel"})
}

func (h *fakeApprovalHandle) Finish(result error) {
	h.finishes++
	if result != nil {
		h.finishErr = result.Error()
	}
	*h.events = append(*h.events, approvalEvent{kind: "finish", err: h.finishErr})
}

type fakeNetworkApprover struct {
	events  *[]approvalEvent
	specs   []NetworkSpec
	handles []*fakeApprovalHandle
	err     error
}

func (a *fakeNetworkApprover) Approve(_ context.Context, spec NetworkSpec) (ApprovalHandle, error) {
	*a.events = append(*a.events, approvalEvent{kind: "begin"})
	a.specs = append(a.specs, spec)
	if a.err != nil {
		return nil, a.err
	}
	handle := &fakeApprovalHandle{events: a.events}
	a.handles = append(a.handles, handle)
	return handle, nil
}

type approvalMerger struct {
	events *[]approvalEvent
	err    error
	calls  int
}

func (m *approvalMerger) Merge(_ context.Context, _ string, _ int) error {
	m.calls++
	*m.events = append(*m.events, approvalEvent{kind: "merge"})
	return m.err
}

func eventKinds(events []approvalEvent) []string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.kind)
	}
	return kinds
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The success path starts one approval before the merge, finalizes it with a
// nil result and never cancels the proxy. This is the "1 approval before
// launch" check from the card.
func TestNetworkApprovalBeforeLaunchFinalizeOnSuccess(t *testing.T) {
	events := []approvalEvent{}
	approver := &fakeNetworkApprover{events: &events}
	merger := &approvalMerger{events: &events}
	spec := &NetworkSpec{Method: "POST", Host: "api.github.com", Path: "/repos/refusedguy/beacon/pulls/42/merge"}
	st := newFakeStore(readyWatch("w1", 42, 3))

	result, err := Run(context.Background(), st, merger, nil, Options{
		NetworkApprover: approver,
		NetworkSpec:     spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Merged) != 1 || result.Merged[0] != 42 {
		t.Fatalf("merged = %v, want [42]", result.Merged)
	}
	if len(approver.specs) != 1 || approver.specs[0] != *spec {
		t.Fatalf("specs = %+v, want one copy of %+v", approver.specs, *spec)
	}
	if got, want := eventKinds(events), []string{"begin", "merge", "finish"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if len(approver.handles) != 1 {
		t.Fatalf("handles = %d, want 1", len(approver.handles))
	}
	handle := approver.handles[0]
	if handle.finishes != 1 || handle.finishErr != "" {
		t.Fatalf("finish: count %d err %q, want 1 and empty", handle.finishes, handle.finishErr)
	}
	if handle.cancels != 0 {
		t.Fatalf("cancels = %d, want 0 on success", handle.cancels)
	}
}

// An error in the operation cancels the proxy once and still finalizes the
// approval with the error. This is the "1 proxy cancel on error" check.
func TestNetworkApprovalCancelsProxyOnError(t *testing.T) {
	events := []approvalEvent{}
	approver := &fakeNetworkApprover{events: &events}
	merger := &approvalMerger{events: &events, err: errors.New("network down")}
	st := newFakeStore(readyWatch("w1", 42, 3))

	_, err := Run(context.Background(), st, merger, nil, Options{
		NetworkApprover: approver,
		NetworkSpec:     &NetworkSpec{Method: "POST", Host: "api.github.com", Path: "/merge"},
	})
	if err == nil {
		t.Fatal("Run returned nil error, want merge failure")
	}
	if got, want := eventKinds(events), []string{"begin", "merge", "cancel", "finish"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if len(approver.handles) != 1 {
		t.Fatalf("handles = %d, want 1", len(approver.handles))
	}
	handle := approver.handles[0]
	if handle.cancels != 1 {
		t.Fatalf("cancels = %d, want 1", handle.cancels)
	}
	if handle.finishes != 1 || handle.finishErr != "network down" {
		t.Fatalf("finish: count %d err %q, want 1 and %q", handle.finishes, handle.finishErr, "network down")
	}
}

// An approver with no spec makes no approval. This is the "0 approvals
// without a network spec" check and proves existing Watches are untouched.
func TestNetworkApprovalSkippedWithoutSpec(t *testing.T) {
	events := []approvalEvent{}
	approver := &fakeNetworkApprover{events: &events}
	merger := &approvalMerger{events: &events}
	st := newFakeStore(readyWatch("w1", 42, 3))

	result, err := Run(context.Background(), st, merger, nil, Options{
		NetworkApprover: approver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Merged) != 1 || result.Merged[0] != 42 {
		t.Fatalf("merged = %v, want [42]", result.Merged)
	}
	if len(approver.specs) != 0 || len(approver.handles) != 0 {
		t.Fatalf("specs = %+v handles = %+v, want none", approver.specs, approver.handles)
	}
	if got, want := eventKinds(events), []string{"merge"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}
