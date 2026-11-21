package webhook

import (
	"sync"
	"sync/atomic"
	"testing"
)

func newTestFilter() *Filter {
	return NewFilter(FilterConfig{
		Events:   []string{EventWorkflowRun},
		Branches: []string{"main", "release"},
		Authors:  []string{"refusedguy", "beacon-bot"},
	})
}

func TestFilterAcceptsRelevantDelivery(t *testing.T) {
	f := newTestFilter()
	decision, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-1", "sha-1")
	if decision != DecisionAccept {
		t.Fatalf("decision = %q, want %q (reason %q)", decision, DecisionAccept, reason)
	}
	if !decision.Publish() {
		t.Fatal("accepted delivery must be allowed to publish an outbox row")
	}
	if reason == "" {
		t.Fatal("accepted delivery must return an audit reason")
	}

	// A different delivery with a different head SHA is also relevant.
	decision, reason = f.Filter(EventWorkflowRun, "release", "beacon-bot", "delivery-2", "sha-2")
	if decision != DecisionAccept {
		t.Fatalf("second decision = %q, want %q (reason %q)", decision, DecisionAccept, reason)
	}
}

func TestFilterRejectsEventType(t *testing.T) {
	f := newTestFilter()
	decision, reason := f.Filter("push", "main", "refusedguy", "delivery-1", "sha-1")
	if decision != DecisionRejectEvent {
		t.Fatalf("decision = %q, want %q", decision, DecisionRejectEvent)
	}
	if reason == "" {
		t.Fatal("rejected delivery must return an audit reason")
	}
}

func TestFilterRejectsBranch(t *testing.T) {
	f := newTestFilter()
	decision, reason := f.Filter(EventWorkflowRun, "feature/side", "refusedguy", "delivery-1", "sha-1")
	if decision != DecisionRejectBranch {
		t.Fatalf("decision = %q, want %q", decision, DecisionRejectBranch)
	}
	if reason == "" {
		t.Fatal("rejected delivery must return an audit reason")
	}
}

func TestFilterRejectsAuthor(t *testing.T) {
	f := newTestFilter()
	decision, reason := f.Filter(EventWorkflowRun, "main", "stranger", "delivery-1", "sha-1")
	if decision != DecisionRejectAuthor {
		t.Fatalf("decision = %q, want %q", decision, DecisionRejectAuthor)
	}
	if reason == "" {
		t.Fatal("rejected delivery must return an audit reason")
	}
}

func TestFilterDeduplicatesDelivery(t *testing.T) {
	f := newTestFilter()
	first, _ := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-1", "sha-1")
	if first != DecisionAccept {
		t.Fatalf("first decision = %q, want %q", first, DecisionAccept)
	}

	// The same delivery id with a different head SHA is still a repeat.
	second, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-1", "sha-2")
	if second != DecisionDuplicateDelivery {
		t.Fatalf("second decision = %q, want %q (reason %q)", second, DecisionDuplicateDelivery, reason)
	}
	if reason == "" {
		t.Fatal("duplicate delivery must return an audit reason")
	}
}

func TestFilterDeduplicatesHeadSHA(t *testing.T) {
	f := newTestFilter()
	first, _ := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-1", "sha-1")
	if first != DecisionAccept {
		t.Fatalf("first decision = %q, want %q", first, DecisionAccept)
	}

	// A new delivery id carrying the same head SHA does not queue new work.
	second, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-2", "sha-1")
	if second != DecisionDuplicateHeadSHA {
		t.Fatalf("second decision = %q, want %q (reason %q)", second, DecisionDuplicateHeadSHA, reason)
	}
	if reason == "" {
		t.Fatal("duplicate head SHA must return an audit reason")
	}
}

func TestFilterRejectsEmptyFields(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		branch string
		author string
		id     string
		sha    string
		field  string
	}{
		{name: "event", event: "  ", branch: "main", author: "refusedguy", id: "d", sha: "s", field: "event"},
		{name: "branch", event: EventWorkflowRun, branch: "", author: "refusedguy", id: "d", sha: "s", field: "branch"},
		{name: "author", event: EventWorkflowRun, branch: "main", author: "", id: "d", sha: "s", field: "author"},
		{name: "delivery id", event: EventWorkflowRun, branch: "main", author: "refusedguy", id: "", sha: "s", field: "deliveryID"},
		{name: "head SHA", event: EventWorkflowRun, branch: "main", author: "refusedguy", id: "d", sha: "", field: "headSHA"},
		{name: "all fields", field: "event"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFilter()
			decision, reason := f.Filter(tc.event, tc.branch, tc.author, tc.id, tc.sha)
			if decision != DecisionRejectEmpty {
				t.Fatalf("decision = %q, want %q", decision, DecisionRejectEmpty)
			}
			if want := "webhook: missing " + tc.field; reason != want {
				t.Fatalf("reason = %q, want %q", reason, want)
			}
		})
	}
}

func TestFilteredDecisionDoesNotPublish(t *testing.T) {
	for _, decision := range []Decision{
		DecisionRejectEmpty,
		DecisionRejectEvent,
		DecisionRejectBranch,
		DecisionRejectAuthor,
		DecisionDuplicateDelivery,
		DecisionDuplicateHeadSHA,
	} {
		if decision.Publish() {
			t.Errorf("%q must not publish an outbox row", decision)
		}
	}
	if !DecisionAccept.Publish() {
		t.Error("DecisionAccept must publish an outbox row")
	}
}

func TestFilterDedupeConvergesUnderConcurrency(t *testing.T) {
	f := newTestFilter()
	const calls = 32
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision, _ := f.Filter(EventWorkflowRun, "main", "refusedguy", "same-delivery", "same-sha")
			if decision == DecisionAccept {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted deliveries = %d, want exactly 1", got)
	}
}

// TestFilterDistinctIDsAtSameCreatedDoNotMerge is the negative control from
// RS-WK-06. The filter takes no created or timestamp input, so two events
// emitted at the same instant cannot merge just because their clocks match.
// Dedupe keys are the delivery id and the head SHA, nothing about when the
// event happened.
func TestFilterDistinctIDsAtSameCreatedDoNotMerge(t *testing.T) {
	f := newTestFilter()
	first, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-a", "sha-a")
	if first != DecisionAccept {
		t.Fatalf("first decision = %q, want %q (reason %q)", first, DecisionAccept, reason)
	}

	// Same event, branch and author as an event created in the same second, but
	// a different delivery id and head SHA. It is a second, independent effect.
	second, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-b", "sha-b")
	if second != DecisionAccept {
		t.Fatalf("second decision = %q, want %q (reason %q)", second, DecisionAccept, reason)
	}
}

// TestFilterRepeatedIDCreatesNoSecondEffect is the negative control from
// RS-WK-06. Redelivering one id is dropped, so it cannot queue a second outbox
// row. A new head SHA on the repeat does not change that: delivery is checked
// before head SHA.
func TestFilterRepeatedIDCreatesNoSecondEffect(t *testing.T) {
	f := newTestFilter()
	first, _ := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-a", "sha-a")
	if first != DecisionAccept {
		t.Fatalf("first decision = %q, want %q", first, DecisionAccept)
	}
	if !first.Publish() {
		t.Fatal("first accepted delivery must publish one effect")
	}

	second, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-a", "sha-b")
	if second != DecisionDuplicateDelivery {
		t.Fatalf("second decision = %q, want %q (reason %q)", second, DecisionDuplicateDelivery, reason)
	}
	if second.Publish() {
		t.Fatal("repeated delivery id must not publish a second effect")
	}
}

// TestFilterDeliveryCheckedBeforeHeadSHA pins the order of the dedupe checks.
// When a delivery is both a repeated id and a repeated head SHA, the delivery
// check wins. Time plays no part in either check.
func TestFilterDeliveryCheckedBeforeHeadSHA(t *testing.T) {
	f := newTestFilter()
	first, _ := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-a", "sha-a")
	if first != DecisionAccept {
		t.Fatalf("first decision = %q, want %q", first, DecisionAccept)
	}

	decision, reason := f.Filter(EventWorkflowRun, "main", "refusedguy", "delivery-a", "sha-a")
	if decision != DecisionDuplicateDelivery {
		t.Fatalf("decision = %q, want %q (reason %q)", decision, DecisionDuplicateDelivery, reason)
	}
	if want := "webhook: delivery delivery-a already handled"; reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}
