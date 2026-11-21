package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// filterRecorder mirrors the cmd/beacon adapter: a fast in-memory filter gate
// followed by a journal that classifies the delivery by its body.
type filterRecorder struct {
	filter  *Filter
	journal *MemoryDeliveryJournal
	results chan RecordResult
}

func newFilterRecorder(f *Filter, j *MemoryDeliveryJournal) *filterRecorder {
	return &filterRecorder{filter: f, journal: j, results: make(chan RecordResult, 4)}
}

func (r *filterRecorder) Record(ctx context.Context, d Delivery) (RecordResult, error) {
	var result RecordResult
	if !r.filter.ClaimDelivery(d.ID) {
		result = RecordResult{Duplicate: true}
	} else {
		var err error
		result, err = r.journal.Record(ctx, DeliveryKey(d.ID), d.Payload, d.At)
		if err != nil {
			return RecordResult{}, err
		}
	}
	r.results <- result
	return result, nil
}

type eventCapture struct {
	events chan string
}

func (s *eventCapture) Log(_ observe.Level, event string, _ observe.Fields) {
	select {
	case s.events <- event:
	default:
	}
}

func (s *eventCapture) Inc(string, string) {}

func waitEvent(t *testing.T, s *eventCapture, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-s.events:
			if event == want {
				return
			}
		case <-deadline:
			t.Fatalf("event %q was not logged", want)
		}
	}
}

// TestRestartWithChangedBodyReportsConflict joins the two merged changes. PR
// 872 restores the dedupe index from the journal at startup. PR 874 separates
// Duplicate from Conflict by the body. The gap was the fast gate: an ID
// restored from the journal made the adapter return Duplicate before the
// journal could inspect a changed body. A restart followed by a changed body
// must still produce one effect and a separate conflict log entry.
func TestRestartWithChangedBodyReportsConflict(t *testing.T) {
	prevSink := observe.Default()
	capture := &eventCapture{events: make(chan string, 16)}
	observe.SetDefault(capture)
	t.Cleanup(func() { observe.SetDefault(prevSink) })

	journal := NewMemoryDeliveryJournal()
	bodyA := `{"action":"completed","head_sha":"sha-1"}`
	bodyB := `{"action":"requested","head_sha":"sha-1"}`

	send := func(h *Receiver, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-restart", EventWorkflowRun, body))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", w.Code)
		}
	}
	waitResult := func(r *filterRecorder) RecordResult {
		t.Helper()
		select {
		case result := <-r.results:
			return result
		case <-time.After(2 * time.Second):
			t.Fatal("recorder was not called")
			return RecordResult{}
		}
	}

	firstRecorder := newFilterRecorder(NewFilter(FilterConfig{}), journal)
	first, err := NewReceiver([]byte(testSecret), firstRecorder, nil)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	send(first, bodyA)
	if result := waitResult(firstRecorder); !result.Created || result.Duplicate || result.Conflict {
		t.Fatalf("first result = %+v, want Created only", result)
	}

	// A new process rebuilds its filter from the journal. The delivery ID and
	// the head SHA are both restored, as restoreWebhookFilter in cmd/beacon
	// does. The body is not part of that index.
	restartedFilter := NewFilter(FilterConfig{})
	restartedFilter.Restore("delivery-restart", "sha-1")
	secondRecorder := newFilterRecorder(restartedFilter, journal)
	second, err := NewReceiver([]byte(testSecret), secondRecorder, nil)
	if err != nil {
		t.Fatalf("NewReceiver after restart: %v", err)
	}

	send(second, bodyB)
	if result := waitResult(secondRecorder); !result.Duplicate || !result.Conflict || result.Created {
		t.Fatalf("changed redelivery = %+v, want Duplicate and Conflict", result)
	}
	waitEvent(t, capture, "webhook-conflict")

	send(second, bodyA)
	if result := waitResult(secondRecorder); !result.Duplicate || result.Conflict || result.Created {
		t.Fatalf("identical redelivery = %+v, want Duplicate only", result)
	}
	waitEvent(t, capture, "webhook-duplicate")
}
