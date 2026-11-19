package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type testClock struct{}

func (testClock) Now() time.Time { return time.Time{} }

type testStore struct{}

func (testStore) SaveWatch(Watch) error                     { return nil }
func (testStore) Watch(string) (Watch, error)               { return Watch{}, nil }
func (testStore) ActiveWatches() ([]Watch, error)           { return nil, nil }
func (testStore) AllWatches() ([]Watch, error)              { return nil, nil }
func (testStore) SaveObligation(Obligation) error           { return nil }
func (testStore) Obligation(string) (Obligation, error)     { return Obligation{}, nil }
func (testStore) PendingObligations() ([]Obligation, error) { return nil, nil }

type testCI struct{}

func (testCI) PRState(context.Context, string, int) (PRState, error) { return PRState{}, nil }

type testNotifier struct{}

func (testNotifier) Notify(context.Context, Obligation, Pointer) (AttemptOutcome, error) {
	return OutcomePosted, nil
}

var (
	_ Clock      = testClock{}
	_ Store      = testStore{}
	_ CIProvider = testCI{}
	_ Notifier   = testNotifier{}
	_ error      = (*AppError)(nil)
)

func TestTypes(t *testing.T) {
	t.Run("zero values", func(t *testing.T) {
		var w Watch
		if w.ID != "" || w.Repo != "" || w.PR != 0 || w.HeadSHA != "" || w.BaseRef != "" ||
			w.Revision != 0 || w.Status != "" || w.Verdict != "" || w.Checks != nil ||
			w.Subscribers != nil || !w.DeadlineAt.IsZero() || !w.CreatedAt.IsZero() {
			t.Fatalf("Watch zero value is not empty: %+v", w)
		}

		var o Obligation
		if o.ID != "" || o.WatchID != "" || o.Revision != 0 || o.SubscriberID != "" ||
			o.Attempts != nil || !o.StagedAt.IsZero() || o.EscalationLevel != 0 {
			t.Fatalf("Obligation zero value is not empty: %+v", o)
		}

		var a AppError
		if a.Code != "" || a.Message != "" || a.Status != 0 || a.RetryAfterMs != nil {
			t.Fatalf("AppError zero value is not empty: %+v", a)
		}
	})

	t.Run("watch json", func(t *testing.T) {
		want := Watch{
			ID:          "w1",
			Repo:        "org/repo",
			PR:          7,
			HeadSHA:     "abc123",
			BaseRef:     "main",
			Status:      StatusWatching,
			Verdict:     VerdictReady,
			Revision:    3,
			Checks:      map[string]Check{"ci/test": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true, DetailsURL: "https://example.test/c"}},
			Merge:       MergeInfo{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
			Flaky:       FlakyConfig{Names: []string{"ci/flaky"}, MaxRetries: 2, Used: map[string]int{"ci/flaky": 1}},
			Policy:      Policy{RequireUpToDate: true},
			Subscribers: []Subscriber{{ID: "s1", Role: RoleExecutor, Target: "t1"}},
			CreatedAt:   time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
		}

		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal Watch: %v", err)
		}
		assertJSONKeys(t, b, "id", "repo", "pr", "headSHA", "baseRef", "issue", "status", "verdict",
			"revision", "checks", "merge", "flaky", "policy", "subscribers", "deadlineAt",
			"noChecksSince", "createdAt", "closedAt", "closeReason")

		var got Watch
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal Watch: %v", err)
		}
		if got.Revision != 3 || got.Status != StatusWatching || got.Verdict != VerdictReady ||
			got.Checks["ci/test"].Conclusion != "SUCCESS" || got.Subscribers[0].Role != RoleExecutor {
			t.Fatalf("Watch round trip mismatch: %+v", got)
		}
	})

	t.Run("obligation json", func(t *testing.T) {
		want := Obligation{
			ID:              "w1:3:s1",
			WatchID:         "w1",
			Revision:        3,
			SubscriberID:    "s1",
			VerdictPointer:  VerdictReady,
			StagedAt:        time.Date(2026, 11, 19, 10, 1, 0, 0, time.UTC),
			Attempts:        []Attempt{{N: 1, At: time.Date(2026, 11, 19, 10, 2, 0, 0, time.UTC), Channel: "s1", Outcome: OutcomePosted}},
			EscalationLevel: 1,
		}

		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal Obligation: %v", err)
		}
		assertJSONKeys(t, b, "id", "watchID", "revision", "subscriberID", "verdictPointer", "stagedAt",
			"attempts", "deliveredAt", "claimedAt", "readAt", "escalatedAt", "escalationLevel")

		var got Obligation
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal Obligation: %v", err)
		}
		if got.ID != want.ID || got.Revision != 3 || len(got.Attempts) != 1 ||
			got.Attempts[0].Outcome != OutcomePosted || got.EscalationLevel != 1 {
			t.Fatalf("Obligation round trip mismatch: %+v", got)
		}
	})

	t.Run("app error", func(t *testing.T) {
		wantStatus := map[ErrorCode]int{
			CodeInvalidInput:   400,
			CodeWatchNotFound:  404,
			CodeHeadSHAChanged: 409,
			CodeRateLimited:    429,
			CodeLimitExceeded:  429,
			CodeCancelled:      410,
			CodeGone:           410,
			CodePersistFailed:  503,
		}
		for code, status := range wantStatus {
			e := NewAppError(code, "msg")
			if e.Status != status {
				t.Errorf("status for %s: got %d, want %d", code, e.Status, status)
			}
		}

		e := NewAppError(CodeHeadSHAChanged, "head moved")
		var target *AppError
		if !errors.As(fmt.Errorf("mcp: %w", e), &target) || target != e {
			t.Fatal("AppError must work with errors.As")
		}
		if e.Error() == "" {
			t.Fatal("AppError.Error must not be empty")
		}

		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal AppError: %v", err)
		}
		assertJSONKeys(t, b, "code", "message", "status")
	})

	t.Run("ports", func(t *testing.T) {
		var c Clock = testClock{}
		if !c.Now().IsZero() {
			t.Fatal("Clock.Now must return the test value")
		}
		var n Notifier = testNotifier{}
		outcome, err := n.Notify(context.Background(), Obligation{}, Pointer{})
		if err != nil || outcome != OutcomePosted {
			t.Fatalf("Notifier.Notify: outcome=%q err=%v", outcome, err)
		}
	})
}

func TestStreamEventJSON(t *testing.T) {
	ev := StreamEvent{Seq: 3, WatchID: "w1", Payload: []byte("step 3")}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal StreamEvent: %v", err)
	}
	assertJSONKeys(t, b, "seq", "watchID", "payload")

	var got StreamEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal StreamEvent: %v", err)
	}
	if got.Seq != 3 || got.WatchID != "w1" || string(got.Payload) != "step 3" {
		t.Fatalf("StreamEvent round trip mismatch: %+v", got)
	}
}

func TestStreamAssemblerRestoresOrderDropsDuplicatesAndKeepsResult(t *testing.T) {
	a := NewStreamAssembler(0) // default window

	// Chunks arrive out of order, one is repeated and one has an invalid seq.
	a.Push(StreamEvent{Seq: 3, Payload: []byte("c")})
	a.Push(StreamEvent{Seq: 1, Payload: []byte("a")})
	a.Push(StreamEvent{Seq: 3, Payload: []byte("c-again")})
	a.Push(StreamEvent{Seq: 2, Payload: []byte("b")})
	a.Push(StreamEvent{Seq: 0, Payload: []byte("ignored")})

	ordered := a.OrderedChunks()
	want := []string{"a", "b", "c"}
	if len(ordered) != len(want) {
		t.Fatalf("ordered chunks = %d, want %d: %+v", len(ordered), len(want), ordered)
	}
	for i, ev := range ordered {
		if ev.Seq != uint64(i+1) || string(ev.Payload) != want[i] {
			t.Errorf("chunk %d = (seq %d, %q), want (seq %d, %q)", i, ev.Seq, ev.Payload, i+1, want[i])
		}
	}

	const result = "the complete authoritative result"
	final := a.Finish(3, []byte(result))
	if final.Chunks != 3 || final.Duplicates != 1 || !final.Streamed {
		t.Fatalf("final counters = %+v, want chunks 3, duplicates 1, streamed true", final)
	}
	if len(final.Missing) != 0 {
		t.Errorf("missing = %v, want empty", final.Missing)
	}
	if string(final.Result) != result {
		t.Errorf("result = %q, want %q", final.Result, result)
	}
}

func TestStreamAssemblerReportsMissingAndStillReturnsFullResult(t *testing.T) {
	a := NewStreamAssembler(2)

	// Position 1 is lost. With a window of two the gap is still inside the
	// window, so nothing is emitted until Finish closes the stream.
	a.Push(StreamEvent{Seq: 2, Payload: []byte("b")})
	a.Push(StreamEvent{Seq: 3, Payload: []byte("c")})
	if got := a.OrderedChunks(); len(got) != 0 {
		t.Fatalf("emitted %d chunks before Finish, want 0: %+v", len(got), got)
	}

	const result = "full result despite a lost chunk"
	final := a.Finish(3, []byte(result))
	if !reflect.DeepEqual(final.Missing, []uint64{1}) {
		t.Errorf("missing = %v, want [1]", final.Missing)
	}
	if string(final.Result) != result {
		t.Errorf("result = %q, want %q", final.Result, result)
	}
	if len(final.Events) != 2 || final.Events[0].Seq != 2 || final.Events[1].Seq != 3 {
		t.Errorf("final events = %+v, want seq 2 and 3 in order", final.Events)
	}
}

func TestStreamAssemblerGivesUpGapBeyondWindow(t *testing.T) {
	a := NewStreamAssembler(2)

	// Position 1 and 2 are lost; the gap to seq 3 exceeds the window, so the
	// assembler reports them as missing as soon as it can.
	a.Push(StreamEvent{Seq: 3, Payload: []byte("c")})
	a.Push(StreamEvent{Seq: 4, Payload: []byte("d")})

	final := a.Finish(4, []byte("full"))
	if !reflect.DeepEqual(final.Missing, []uint64{1, 2}) {
		t.Errorf("missing = %v, want [1 2]", final.Missing)
	}
	if len(final.Events) != 2 || final.Events[0].Seq != 3 || final.Events[1].Seq != 4 {
		t.Errorf("final events = %+v, want seq 3 and 4 in order", final.Events)
	}
}

func assertJSONKeys(t *testing.T, b []byte, keys ...string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("decode JSON object: %v", err)
	}
	for _, k := range keys {
		if _, ok := fields[k]; !ok {
			t.Errorf("missing JSON key %q in %s", k, b)
		}
	}
}
