package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeRerunPort is the RerunPort used by these tests. It counts provider calls
// and spends so a repeat can be proven not to reach GitHub or the budget store.
type fakeRerunPort struct {
	w          domain.Watch
	recorded   map[string]bool
	spends     map[string][]string
	rerunCalls int
	repo       string
	runID      int64
	watchErr   error
	rerunErr   error
	spendErr   error
	recordErr  error
}

var _ RerunPort = (*fakeRerunPort)(nil)

func newFakeRerunPort() *fakeRerunPort {
	return &fakeRerunPort{
		w: domain.Watch{
			ID:       "watch-1",
			Repo:     "refusedguy/beacon",
			Revision: 7,
			Flaky: domain.FlakyConfig{
				Names:      []string{"ci/test"},
				MaxRetries: 2,
				Used:       map[string]int{},
			},
			Checks: map[string]domain.Check{
				"ci/test": {
					Conclusion: "FAILURE",
					Required:   true,
					DetailsURL: "https://github.com/refusedguy/beacon/actions/runs/555/job/999",
				},
			},
		},
		recorded: make(map[string]bool),
		spends:   make(map[string][]string),
	}
}

func (f *fakeRerunPort) Watch(_ string) (domain.Watch, error) { return f.w, f.watchErr }

func (f *fakeRerunPort) RerunFailed(_ context.Context, repo string, runID int64) error {
	f.rerunCalls++
	f.repo = repo
	f.runID = runID
	return f.rerunErr
}

func (f *fakeRerunPort) RerunRecorded(_ context.Context, key string) (bool, error) {
	return f.recorded[key], f.recordErr
}

func (f *fakeRerunPort) SpendRerunBudget(_ context.Context, key string, checkNames []string) error {
	if f.spendErr != nil {
		return f.spendErr
	}
	f.spends[key] = append([]string(nil), checkNames...)
	f.recorded[key] = true
	if f.w.Flaky.Used == nil {
		f.w.Flaky.Used = map[string]int{}
	}
	for _, name := range checkNames {
		f.w.Flaky.Used[name]++
	}
	return nil
}

// TestRerunFailedZeroBudgetSkipsProvider pins the first acceptance case: a
// spent budget returns CodeLimitExceeded and no provider or spend call happens.
func TestRerunFailedZeroBudgetSkipsProvider(t *testing.T) {
	port := newFakeRerunPort()
	port.w.Flaky.MaxRetries = 0
	h := NewRerunFailedHandler(port)

	_, err := h.Rerun(context.Background(), RerunFailedInput{WatchID: "watch-1"})
	assertCode(t, err, domain.CodeLimitExceeded)
	if port.rerunCalls != 0 {
		t.Errorf("provider calls = %d, want 0", port.rerunCalls)
	}
	if len(port.spends) != 0 {
		t.Errorf("spends = %v, want none", port.spends)
	}
}

// TestRerunFailedSpendsBudgetOnce pins the second acceptance case: a Watch with
// budget 2 and one flaky check produces exactly one provider call and one spend.
// The runID is resolved from the check details URL when the caller omits it.
func TestRerunFailedSpendsBudgetOnce(t *testing.T) {
	port := newFakeRerunPort()
	h := NewRerunFailedHandler(port)

	out, err := h.Rerun(context.Background(), RerunFailedInput{WatchID: "watch-1"})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if !out.Rerun {
		t.Error("rerun = false on the first call, want true")
	}
	if out.RunID != 555 {
		t.Errorf("runID = %d, want 555 from the details URL", out.RunID)
	}
	if out.Revision != 7 || out.WatchID != "watch-1" {
		t.Errorf("output watch identity = %s/%d, want watch-1/7", out.WatchID, out.Revision)
	}
	if out.RetryBudget != 1 {
		t.Errorf("retryBudget = %d, want 1 after one spend", out.RetryBudget)
	}
	wantKey := rerunIdempotencyKey("watch-1", 7, 555)
	if out.ObligationID != wantKey {
		t.Errorf("obligationID = %q, want %q", out.ObligationID, wantKey)
	}
	if port.rerunCalls != 1 {
		t.Errorf("provider calls = %d, want 1", port.rerunCalls)
	}
	if port.repo != "refusedguy/beacon" || port.runID != 555 {
		t.Errorf("provider args = %s/%d, want refusedguy/beacon/555", port.repo, port.runID)
	}
	if len(port.spends) != 1 || len(port.spends[wantKey]) != 1 || port.spends[wantKey][0] != "ci/test" {
		t.Errorf("spends = %v, want one spend of ci/test under %q", port.spends, wantKey)
	}
	if got := port.w.Flaky.Used["ci/test"]; got != 1 {
		t.Errorf("Flaky.Used[ci/test] = %d, want 1", got)
	}
}

// TestRerunFailedRepeatDoesNotRerun pins the third acceptance case: the same
// key returns rerun=false and leaves the provider and the spend counter alone.
func TestRerunFailedRepeatDoesNotRerun(t *testing.T) {
	port := newFakeRerunPort()
	h := NewRerunFailedHandler(port)
	in := RerunFailedInput{WatchID: "watch-1", RunID: 555}

	first, err := h.Rerun(context.Background(), in)
	if err != nil {
		t.Fatalf("first Rerun: %v", err)
	}
	if !first.Rerun {
		t.Fatal("first rerun = false, want true")
	}
	second, err := h.Rerun(context.Background(), in)
	if err != nil {
		t.Fatalf("second Rerun: %v", err)
	}
	if second.Rerun {
		t.Error("repeat rerun = true, want false")
	}
	if port.rerunCalls != 1 {
		t.Errorf("provider calls = %d after repeat, want 1", port.rerunCalls)
	}
	if len(port.spends) != 1 {
		t.Errorf("spends = %v after repeat, want the first spend only", port.spends)
	}
	if got := port.w.Flaky.Used["ci/test"]; got != 1 {
		t.Errorf("Flaky.Used[ci/test] = %d after repeat, want 1", got)
	}
}

func TestRerunFailedCheckNameMustBeFlakyMatched(t *testing.T) {
	port := newFakeRerunPort()
	h := NewRerunFailedHandler(port)

	_, err := h.Rerun(context.Background(), RerunFailedInput{WatchID: "watch-1", CheckName: "ci/build", RunID: 555})
	assertCode(t, err, domain.CodeInvalidInput)
	if port.rerunCalls != 0 {
		t.Errorf("provider calls = %d for a non-flaky check, want 0", port.rerunCalls)
	}
}

func TestRerunFailedCheckNameNarrowsTheSpend(t *testing.T) {
	port := newFakeRerunPort()
	port.w.Checks["ci/lint"] = domain.Check{
		Conclusion: "FAILURE",
		Required:   true,
		DetailsURL: "https://github.com/refusedguy/beacon/actions/runs/556/job/1000",
	}
	port.w.Flaky.Names = []string{"ci/test", "ci/lint"}
	h := NewRerunFailedHandler(port)

	out, err := h.Rerun(context.Background(), RerunFailedInput{WatchID: "watch-1", CheckName: "ci/lint", RunID: 556})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if !out.Rerun {
		t.Error("rerun = false, want true")
	}
	key := rerunIdempotencyKey("watch-1", 7, 556)
	spent := port.spends[key]
	if len(spent) != 1 || spent[0] != "ci/lint" {
		t.Errorf("spent checks = %v, want only ci/lint", spent)
	}
	if got := port.w.Flaky.Used["ci/test"]; got != 0 {
		t.Errorf("Flaky.Used[ci/test] = %d, want 0", got)
	}
}

func TestRerunFailedProviderErrorDoesNotSpend(t *testing.T) {
	port := newFakeRerunPort()
	port.rerunErr = errors.New("github unavailable")
	h := NewRerunFailedHandler(port)

	_, err := h.Rerun(context.Background(), RerunFailedInput{WatchID: "watch-1", RunID: 555})
	assertCode(t, err, domain.CodePersistFailed)
	if len(port.spends) != 0 {
		t.Errorf("spends = %v after a provider error, want none", port.spends)
	}
	if got := port.w.Flaky.Used["ci/test"]; got != 0 {
		t.Errorf("Flaky.Used[ci/test] = %d, want 0", got)
	}
}

func TestRerunFailedRequiresWatchID(t *testing.T) {
	h := NewRerunFailedHandler(newFakeRerunPort())
	_, err := h.Rerun(context.Background(), RerunFailedInput{})
	assertCode(t, err, domain.CodeInvalidInput)
}

func TestParseRunID(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want int64
		ok   bool
	}{
		{"actions run", "https://github.com/o/r/actions/runs/12345/job/99", 12345, true},
		{"nested path", "https://github.com/o/r/actions/runs/987", 987, true},
		{"no run segment", "https://github.com/o/r/commit/abc", 0, false},
		{"non numeric run", "https://github.com/o/r/actions/runs/main", 0, false},
		{"empty", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRunID(tt.url)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseRunID(%q) = %d, %t; want %d, %t", tt.url, got, ok, tt.want, tt.ok)
			}
		})
	}
}
