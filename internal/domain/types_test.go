package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
