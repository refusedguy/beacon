package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/refusedguy/beacon/internal/domain"
)

// recordingStamper is an in-memory ObligationStamper used by the tool tests.
// It applies the same "write only when the fact is still zero" rule as the SQL
// stamper and records how many times each fact was requested.
type recordingStamper struct {
	mu           sync.Mutex
	obligations  map[string]domain.Obligation
	readCalls    int
	claimedCalls int
}

func newRecordingStamper() *recordingStamper {
	return &recordingStamper{obligations: map[string]domain.Obligation{}}
}

func (s *recordingStamper) add(o domain.Obligation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obligations[o.ID] = o
}

func (s *recordingStamper) snapshot(id string) (domain.Obligation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.obligations[id]
	return o, ok
}

func (s *recordingStamper) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readCalls, s.claimedCalls
}

func (s *recordingStamper) StampRead(_ context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readCalls++
	o, ok := s.obligations[id]
	if !ok {
		return domain.Obligation{}, false, domain.NewAppError(domain.CodeWatchNotFound, "obligation not found")
	}
	if !o.ReadAt.IsZero() {
		return o, false, nil
	}
	o.ReadAt = at
	s.obligations[id] = o
	return o, true, nil
}

func (s *recordingStamper) StampClaimed(_ context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimedCalls++
	o, ok := s.obligations[id]
	if !ok {
		return domain.Obligation{}, false, domain.NewAppError(domain.CodeWatchNotFound, "obligation not found")
	}
	if !o.ClaimedAt.IsZero() {
		return o, false, nil
	}
	o.ClaimedAt = at
	s.obligations[id] = o
	return o, true, nil
}

// resultFixture builds a Watch with one non-flaky failure, one flaky failure,
// one success and one skipped check. The flaky pattern is a glob so the test
// also covers name/glob matching for flakyMatched.
func resultFixture(verdict domain.Verdict) domain.Watch {
	return domain.Watch{
		ID:       "w-1",
		Repo:     "o/r",
		PR:       1,
		HeadSHA:  "sha",
		Status:   domain.StatusNotifying,
		Verdict:  verdict,
		Revision: 7,
		Checks: map[string]domain.Check{
			"ci/failure": {State: "FAILURE", Conclusion: "FAILURE", Required: true, DetailsURL: "https://ci/1"},
			"ci/flaky":   {State: "FAILURE", Conclusion: "FAILURE", Required: false, DetailsURL: "https://ci/2"},
			"ci/ok":      {State: "SUCCESS", Conclusion: "SUCCESS", Required: true, DetailsURL: "https://ci/3"},
			"ci/skipped": {State: "SKIPPED", Conclusion: "SKIPPED", Required: false, DetailsURL: "https://ci/4"},
		},
		Merge: domain.MergeInfo{Mergeable: "MERGEABLE", MergeStateStatus: "DIRTY", ReviewDecision: "APPROVED"},
		Flaky: domain.FlakyConfig{
			Names:      []string{"ci/fl*"},
			MaxRetries: 2,
			Used:       map[string]int{"ci/flaky": 1},
		},
	}
}

func resultObligation(w domain.Watch, subscriber string) domain.Obligation {
	return domain.Obligation{
		ID:             domain.ObligationKey(w.ID, w.Revision, subscriber),
		WatchID:        w.ID,
		Revision:       w.Revision,
		SubscriberID:   subscriber,
		VerdictPointer: w.Verdict,
		StagedAt:       time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
	}
}

func sortedFailedChecks(in []FailedCheck) []FailedCheck {
	out := append([]FailedCheck(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TestGetResultProjectsContractAndStampsRead covers both verdicts named in the
// card. It checks the projected fields, the failed-check shape without logs,
// the retry budget and the idempotent readAt stamp.
func TestGetResultProjectsContractAndStampsRead(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictReady, domain.VerdictKeepWorking} {
		t.Run(string(verdict), func(t *testing.T) {
			clock := &stepClock{t: time.Date(2026, 11, 19, 10, 5, 0, 0, time.UTC)}
			w := resultFixture(verdict)
			o := resultObligation(w, "session-G")
			store := newMemStore()
			if err := store.SaveWatch(w); err != nil {
				t.Fatalf("SaveWatch: %v", err)
			}
			if err := store.SaveObligation(o); err != nil {
				t.Fatalf("SaveObligation: %v", err)
			}
			stamper := newRecordingStamper()
			stamper.add(o)
			h := NewGetResultHandler(store, stamper, clock)

			out, err := h.Result(context.Background(), sessionReq("session-G"), GetResultInput{WatchID: w.ID})
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			if out.Status != string(domain.StatusNotifying) || out.Verdict != string(verdict) {
				t.Errorf("status/verdict = %q/%q", out.Status, out.Verdict)
			}
			if out.Revision != w.Revision {
				t.Errorf("revision = %d, want %d", out.Revision, w.Revision)
			}
			if out.Merge.Mergeable != "MERGEABLE" || out.Merge.MergeStateStatus != "DIRTY" {
				t.Errorf("merge = %+v", out.Merge)
			}
			if out.ObligationID != o.ID {
				t.Errorf("obligationID = %q, want %q", out.ObligationID, o.ID)
			}

			want := []FailedCheck{
				{Name: "ci/failure", Conclusion: "FAILURE", DetailsURL: "https://ci/1", Required: true, FlakyMatched: false},
				{Name: "ci/flaky", Conclusion: "FAILURE", DetailsURL: "https://ci/2", Required: false, FlakyMatched: true},
			}
			got := sortedFailedChecks(out.FailedChecks)
			if len(got) != len(want) {
				t.Fatalf("failedChecks = %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("failedChecks[%d] = %+v, want %+v", i, got[i], want[i])
				}
			}
			if out.RetryBudget != 1 {
				t.Errorf("retryBudget = %d, want 1", out.RetryBudget)
			}

			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal output: %v", err)
			}
			if lowered := strings.ToLower(string(raw)); strings.Contains(lowered, "log") || strings.Contains(lowered, "output") {
				t.Errorf("response carries check logs or output: %s", raw)
			}

			gotObligation, ok := stamper.snapshot(o.ID)
			if !ok || !gotObligation.ReadAt.Equal(clock.t) {
				t.Fatalf("readAt = %v, want %v", gotObligation.ReadAt, clock.t)
			}

			clock.Advance(time.Minute)
			if _, err := h.Result(context.Background(), sessionReq("session-G"), GetResultInput{WatchID: w.ID}); err != nil {
				t.Fatalf("repeat Result: %v", err)
			}
			repeat, _ := stamper.snapshot(o.ID)
			if !repeat.ReadAt.Equal(clock.t.Add(-time.Minute)) {
				t.Errorf("readAt was overwritten on repeat: %v", repeat.ReadAt)
			}
			reads, claims := stamper.counts()
			if reads != 2 || claims != 0 {
				t.Errorf("stamp calls = read %d claim %d, want 2 and 0", reads, claims)
			}
		})
	}
}

// TestGetResultRetryBudgetWithoutFlakyFailure checks that a watch with no
// flaky-matched failure reports no retry budget.
func TestGetResultRetryBudgetWithoutFlakyFailure(t *testing.T) {
	w := resultFixture(domain.VerdictKeepWorking)
	w.Flaky = domain.FlakyConfig{}
	o := resultObligation(w, "session-B")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

	out, err := h.Result(context.Background(), sessionReq("session-B"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if out.RetryBudget != 0 {
		t.Errorf("retryBudget = %d, want 0", out.RetryBudget)
	}
	for _, fc := range out.FailedChecks {
		if fc.FlakyMatched {
			t.Errorf("check %q reported flakyMatched without a flaky config", fc.Name)
		}
	}
}

// TestGetResultValidation covers the input and not-found paths before a stamp.
func TestGetResultValidation(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	o := resultObligation(w, "session-V")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

	_, err := h.Result(context.Background(), sessionReq("session-V"), GetResultInput{})
	assertCode(t, err, domain.CodeInvalidInput)

	_, err = h.Result(context.Background(), nil, GetResultInput{WatchID: w.ID})
	assertCode(t, err, domain.CodeInvalidInput)

	_, err = h.Result(context.Background(), sessionReq("session-V"), GetResultInput{WatchID: "missing"})
	assertCode(t, err, domain.CodeWatchNotFound)

	_, err = h.Result(context.Background(), sessionReq("other-session"), GetResultInput{WatchID: w.ID})
	assertCode(t, err, domain.CodeWatchNotFound)

	reads, claims := stamper.counts()
	if reads != 0 || claims != 0 {
		t.Errorf("a rejected call stamped the obligation: read %d claim %d", reads, claims)
	}
}

// dbTxWriter is a minimal single-writer port over a real in-memory SQLite DB.
type dbTxWriter struct{ db *sql.DB }

func (w dbTxWriter) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func newStampTestDB(t *testing.T, o domain.Obligation) (resultTxWriter, func() domain.Obligation) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE obligations (
			seq INTEGER PRIMARY KEY,
			id TEXT NOT NULL UNIQUE,
			watch_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			subscriber_id TEXT NOT NULL,
			verdict_pointer TEXT NOT NULL,
			delivered_at INTEGER NOT NULL DEFAULT 0,
			payload BLOB NOT NULL
		)`)
	if err != nil {
		t.Fatalf("create obligations: %v", err)
	}
	payload, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal obligation: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO obligations (id, watch_id, revision, subscriber_id, verdict_pointer, payload)
		VALUES (?, ?, ?, ?, ?, ?)`,
		o.ID, o.WatchID, o.Revision, o.SubscriberID, string(o.VerdictPointer), payload); err != nil {
		t.Fatalf("insert obligation: %v", err)
	}

	load := func() domain.Obligation {
		t.Helper()
		var raw []byte
		if err := db.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, o.ID).Scan(&raw); err != nil {
			t.Fatalf("read obligation payload: %v", err)
		}
		var got domain.Obligation
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal obligation: %v", err)
		}
		return got
	}
	return dbTxWriter{db: db}, load
}

// TestSQLObligationStamperIdempotentRead proves the production stamper writes
// readAt once through one transaction and leaves it untouched on a repeat.
func TestSQLObligationStamperIdempotentRead(t *testing.T) {
	o := resultObligation(resultFixture(domain.VerdictReady), "session-S")
	writer, load := newStampTestDB(t, o)
	stamper := NewSQLObligationStamper(writer)
	first := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	got, applied, err := stamper.StampRead(context.Background(), o.ID, first)
	if err != nil || !applied || !got.ReadAt.Equal(first) {
		t.Fatalf("first StampRead = %+v applied %v err %v", got, applied, err)
	}
	stored := load()
	if !stored.ReadAt.Equal(first) {
		t.Errorf("persisted readAt = %v, want %v", stored.ReadAt, first)
	}

	_, applied, err = stamper.StampRead(context.Background(), o.ID, first.Add(time.Hour))
	if err != nil || applied {
		t.Fatalf("repeat StampRead applied %v err %v, want no write", applied, err)
	}
	if stored := load(); !stored.ReadAt.Equal(first) {
		t.Errorf("readAt overwritten on repeat: %v", stored.ReadAt)
	}
}

// TestSQLObligationStamperMissingObligation maps a missing row to the contract
// error instead of a raw SQL error.
func TestSQLObligationStamperMissingObligation(t *testing.T) {
	writer, _ := newStampTestDB(t, resultObligation(resultFixture(domain.VerdictReady), "session-X"))
	stamper := NewSQLObligationStamper(writer)
	_, _, err := stamper.StampRead(context.Background(), "no-such-obligation", time.Now())
	assertCode(t, err, domain.CodeWatchNotFound)
}

// greenWatch is a Watch where every rubric criterion passes. All checks are
// green, the merge is clean and the verdict is ready.
func greenWatch() domain.Watch {
	return domain.Watch{
		ID:       "w-green",
		Status:   domain.StatusClosed,
		Verdict:  domain.VerdictReady,
		Revision: 1,
		Checks: map[string]domain.Check{
			"ci/test": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
		},
		Merge:    domain.MergeInfo{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
		ClosedAt: time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC),
	}
}

func criterionByID(t *testing.T, ev RubricEvaluation, id CriterionID) CriterionEvaluation {
	t.Helper()
	for _, c := range ev.Criteria {
		if c.Criterion == id {
			return c
		}
	}
	t.Fatalf("criterion %q missing from evaluation %+v", id, ev)
	return CriterionEvaluation{}
}

// TestGradeResultFinishedRunIsNotDone is the headline case of ADR 0034 §1:
// the run reached a terminal state, yet the work failed, so the verdict is
// failed and no ready verdict is stamped on the task.
func TestGradeResultFinishedRunIsNotDone(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	w.Status = domain.StatusClosed
	w.ClosedAt = time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)

	ev := gradeResult(w, DefaultResultRubric(), 0, false)

	if ev.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q", ev.Outcome, OutcomeFailed)
	}
	if ev.ReadyVerdict != "" {
		t.Errorf("readyVerdict = %q, want empty for a failed result", ev.ReadyVerdict)
	}
	if !criterionByID(t, ev, CriterionRunFinished).Passed {
		t.Error("run_finished should pass: the run did reach a terminal state")
	}
	checks := criterionByID(t, ev, CriterionRequiredChecksOK)
	if checks.Passed || !strings.Contains(checks.Explanation, "ci/failure") {
		t.Errorf("required_checks_ok = %+v, want failed naming ci/failure", checks)
	}
	if merge := criterionByID(t, ev, CriterionMergeReady); merge.Passed {
		t.Errorf("merge_ready should fail on a DIRTY merge: %+v", merge)
	}
}

// TestGradeResultSatisfiedByRubric proves the ready verdict is produced only
// when every verifiable criterion passes.
func TestGradeResultSatisfiedByRubric(t *testing.T) {
	ev := gradeResult(greenWatch(), DefaultResultRubric(), 0, false)

	if ev.Outcome != OutcomeSatisfied {
		t.Fatalf("outcome = %q, want %q", ev.Outcome, OutcomeSatisfied)
	}
	if ev.ReadyVerdict != domain.VerdictReady {
		t.Errorf("readyVerdict = %q, want %q", ev.ReadyVerdict, domain.VerdictReady)
	}
	for _, c := range ev.Criteria {
		if !c.Passed {
			t.Errorf("criterion %q failed: %s", c.Criterion, c.Explanation)
		}
	}
}

// TestGradeResultNeedsRevisionCarriesExplanations checks that an unfinished
// run feeds every criterion explanation back to the next iteration.
func TestGradeResultNeedsRevisionCarriesExplanations(t *testing.T) {
	ev := gradeResult(resultFixture(domain.VerdictKeepWorking), DefaultResultRubric(), 0, false)

	if ev.Outcome != OutcomeNeedsRevision {
		t.Fatalf("outcome = %q, want %q", ev.Outcome, OutcomeNeedsRevision)
	}
	if ev.ReadyVerdict != "" {
		t.Errorf("readyVerdict = %q, want empty", ev.ReadyVerdict)
	}
	if len(ev.Criteria) != len(DefaultResultRubric().Criteria) {
		t.Fatalf("criteria count = %d, want %d", len(ev.Criteria), len(DefaultResultRubric().Criteria))
	}
	for _, c := range ev.Criteria {
		if c.Explanation == "" {
			t.Errorf("criterion %q has no explanation", c.Criterion)
		}
	}
}

// TestGradeResultIterationCap covers the default budget, a custom budget and
// the twenty-iteration ceiling from ADR 0034 §1.
func TestGradeResultIterationCap(t *testing.T) {
	w := resultFixture(domain.VerdictKeepWorking)

	ev := gradeResult(w, DefaultResultRubric(), defaultOutcomeIterations, false)
	if ev.Outcome != OutcomeMaxIterationsReached || ev.MaxIterations != defaultOutcomeIterations {
		t.Fatalf("default cap = %q/%d, want %q/%d", ev.Outcome, ev.MaxIterations, OutcomeMaxIterationsReached, defaultOutcomeIterations)
	}

	custom := ResultRubric{Criteria: []CriterionID{CriterionRequiredChecksOK}, MaxIterations: 5}
	ev = gradeResult(w, custom, 5, false)
	if ev.Outcome != OutcomeMaxIterationsReached || ev.MaxIterations != 5 {
		t.Fatalf("custom cap = %q/%d, want %q/5", ev.Outcome, ev.MaxIterations, OutcomeMaxIterationsReached)
	}

	over := ResultRubric{Criteria: []CriterionID{CriterionRequiredChecksOK}, MaxIterations: maxOutcomeIterations + 5}
	ev = gradeResult(w, over, maxOutcomeIterations+5, false)
	if ev.MaxIterations != maxOutcomeIterations || ev.Outcome != OutcomeMaxIterationsReached {
		t.Fatalf("ceiling = %q/%d, want %q/%d", ev.Outcome, ev.MaxIterations, OutcomeMaxIterationsReached, maxOutcomeIterations)
	}

	zero := ResultRubric{Criteria: []CriterionID{CriterionRequiredChecksOK}}
	ev = gradeResult(w, zero, 0, false)
	if ev.MaxIterations != defaultOutcomeIterations || ev.Outcome != OutcomeNeedsRevision {
		t.Fatalf("zero budget = %q/%d, want %q/%d", ev.Outcome, ev.MaxIterations, OutcomeNeedsRevision, defaultOutcomeIterations)
	}
}

// TestGradeResultInterruptedWins proves a caller-driven interruption is never
// reported as a satisfied result, even when all criteria pass.
func TestGradeResultInterruptedWins(t *testing.T) {
	ev := gradeResult(greenWatch(), DefaultResultRubric(), 0, true)

	if ev.Outcome != OutcomeInterrupted {
		t.Fatalf("outcome = %q, want %q", ev.Outcome, OutcomeInterrupted)
	}
	if ev.ReadyVerdict != "" {
		t.Errorf("readyVerdict = %q, want empty when interrupted", ev.ReadyVerdict)
	}
}

// TestGradeResultUnknownCriterion makes an undefined criterion a failing one
// with an explanation instead of a silent pass.
func TestGradeResultUnknownCriterion(t *testing.T) {
	rubric := ResultRubric{Criteria: []CriterionID{"not_defined"}, MaxIterations: 1}
	ev := gradeResult(greenWatch(), rubric, 1, false)

	c := criterionByID(t, ev, "not_defined")
	if c.Passed || c.Explanation != "unknown criterion" {
		t.Errorf("unknown criterion = %+v", c)
	}
	if ev.Outcome != OutcomeMaxIterationsReached {
		t.Errorf("outcome = %q, want %q", ev.Outcome, OutcomeMaxIterationsReached)
	}
}

// TestGradeResultIsSeparateFromDelivery checks that the grader re-reads Watch
// facts from the store and leaves the delivery stamps untouched.
func TestGradeResultIsSeparateFromDelivery(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	w.Status = domain.StatusClosed
	w.ClosedAt = time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)
	o := resultObligation(w, "session-G")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

	ev, err := h.GradeResult(context.Background(), w.ID, DefaultResultRubric(), 0, false)
	if err != nil {
		t.Fatalf("GradeResult: %v", err)
	}
	if ev.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q", ev.Outcome, OutcomeFailed)
	}
	reads, claims := stamper.counts()
	if reads != 0 || claims != 0 {
		t.Errorf("grading stamped the obligation: read %d claim %d", reads, claims)
	}

	_, err = h.GradeResult(context.Background(), "", DefaultResultRubric(), 0, false)
	assertCode(t, err, domain.CodeInvalidInput)
	_, err = h.GradeResult(context.Background(), "missing", DefaultResultRubric(), 0, false)
	assertCode(t, err, domain.CodeWatchNotFound)
}

// TestGradeExternalActionLostResponseIsIndeterminate is the ADR 0039 §5
// acceptance: a performed external action whose answer was lost is
// indeterminate even when every rubric criterion would otherwise pass. The
// outcome carries the durable lease key and never a ready verdict.
func TestGradeExternalActionLostResponseIsIndeterminate(t *testing.T) {
	w := greenWatch()
	lease := domain.ObligationKey(w.ID, w.Revision, "session-L")

	lost := gradeExternalAction(w, DefaultResultRubric(), 0, true, true, lease)
	if lost.Outcome != OutcomeIndeterminate {
		t.Fatalf("outcome = %q, want %q", lost.Outcome, OutcomeIndeterminate)
	}
	if lost.ReadyVerdict != "" {
		t.Errorf("readyVerdict = %q, want empty for an unknown effect", lost.ReadyVerdict)
	}
	if lost.LeaseKey != lease {
		t.Errorf("leaseKey = %q, want %q", lost.LeaseKey, lease)
	}
	if len(lost.Criteria) != 1 || lost.Criteria[0].Criterion != CriterionExternalActionResponse || lost.Criteria[0].Passed {
		t.Fatalf("criteria = %+v, want one failing external_action_response", lost.Criteria)
	}

	// The same watch with the answer received is still honestly satisfied and
	// ready. The two outcomes must not collapse into each other.
	answered := gradeExternalAction(w, DefaultResultRubric(), 0, true, false, lease)
	if answered.Outcome != OutcomeSatisfied || answered.ReadyVerdict != domain.VerdictReady {
		t.Fatalf("answered outcome = %q verdict = %q, want satisfied/ready", answered.Outcome, answered.ReadyVerdict)
	}
	if answered.LeaseKey != "" {
		t.Errorf("answered leaseKey = %q, want empty", answered.LeaseKey)
	}
}

// TestGradeExternalActionLostResponseNeverRetries proves a lost answer is not
// laundered into a retry or a failure when the iteration budget is spent, and
// that an action which never reached the outside world is not indeterminate.
func TestGradeExternalActionLostResponseNeverRetries(t *testing.T) {
	w := greenWatch()
	rubric := DefaultResultRubric()

	lost := gradeExternalAction(w, rubric, maxOutcomeIterations+5, true, true, "lease-1")
	if lost.Outcome != OutcomeIndeterminate {
		t.Fatalf("outcome = %q, want %q at any iteration", lost.Outcome, OutcomeIndeterminate)
	}
	if lost.Outcome == OutcomeMaxIterationsReached || lost.Outcome == OutcomeFailed || lost.Outcome == OutcomeNeedsRevision {
		t.Errorf("lost answer turned into a retry or failure: %q", lost.Outcome)
	}

	notPerformed := gradeExternalAction(w, rubric, 0, false, true, "lease-1")
	if notPerformed.Outcome == OutcomeIndeterminate {
		t.Errorf("no external action gave indeterminate: %q", notPerformed.Outcome)
	}
}

// TestDeliveryIndeterminateIsNotTaskIndeterminate guards the ADR 0017 boundary.
// An obligation carrying a delivery-ladder indeterminate attempt does not make
// the task result indeterminate; only a lost response after a performed action
// does that.
func TestDeliveryIndeterminateIsNotTaskIndeterminate(t *testing.T) {
	w := greenWatch()
	o := resultObligation(w, "session-D")
	o.Attempts = []domain.Attempt{{N: 1, Outcome: domain.OutcomeIndeterminate, At: time.Now()}}
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	h := NewGetResultHandler(store, newRecordingStamper(), &stepClock{t: time.Now()})

	deliveryOnly, err := h.GradeExternalAction(context.Background(), w.ID, DefaultResultRubric(), 0, true, false, o.ID)
	if err != nil {
		t.Fatalf("GradeExternalAction: %v", err)
	}
	if deliveryOnly.Outcome != OutcomeSatisfied || deliveryOnly.ReadyVerdict != domain.VerdictReady {
		t.Fatalf("delivery indeterminate changed the task outcome: %+v", deliveryOnly)
	}

	lost, err := h.GradeExternalAction(context.Background(), w.ID, DefaultResultRubric(), 0, true, true, o.ID)
	if err != nil {
		t.Fatalf("GradeExternalAction lost: %v", err)
	}
	if lost.Outcome != OutcomeIndeterminate || lost.LeaseKey != o.ID {
		t.Fatalf("lost outcome = %+v, want indeterminate with lease %q", lost, o.ID)
	}

	if _, err := h.GradeExternalAction(context.Background(), "", DefaultResultRubric(), 0, true, true, o.ID); err == nil {
		t.Error("empty watchID was accepted")
	}
	if _, err := h.GradeExternalAction(context.Background(), "missing", DefaultResultRubric(), 0, true, true, o.ID); err == nil {
		t.Error("missing watch was accepted")
	}
}

// TestGetResultKeepsLeaseKeyAndFieldOrder proves ADR 0036 §2 is untouched: the
// new outcome value adds no field and reorders none of the get_result response.
func TestGetResultKeepsLeaseKeyAndFieldOrder(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	o := resultObligation(w, "session-K")
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

	out, err := h.Result(context.Background(), sessionReq("session-K"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if out.ObligationID != domain.ObligationKey(w.ID, w.Revision, "session-K") {
		t.Errorf("obligationID = %q, want the durable lease key", out.ObligationID)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	wantOrder := []string{"status", "verdict", "revision", "merge", "failedChecks", "obligationID", "retryBudget"}
	last := -1
	for _, field := range wantOrder {
		at := strings.Index(string(raw), `"`+field+`":`)
		if at <= last {
			t.Fatalf("field %q missing or out of order in %s", field, raw)
		}
		last = at
	}
}

// --- Ship facts wiring, T3.75 ----------------------------------------------

func greenRequiredChecks(names ...string) map[string]domain.Check {
	m := make(map[string]domain.Check, len(names))
	for _, name := range names {
		m[name] = domain.Check{State: "SUCCESS", Conclusion: "SUCCESS", Required: true}
	}
	return m
}

func readyWatchWithChecks(checks map[string]domain.Check) domain.Watch {
	return domain.Watch{
		ID:      "w-ready",
		Repo:    "o/r",
		PR:      2,
		HeadSHA: "sha-ready",
		Checks:  checks,
		Merge: domain.MergeInfo{
			Mergeable:        "MERGEABLE",
			MergeStateStatus: "CLEAN",
			ReviewDecision:   "APPROVED",
		},
		Policy: domain.Policy{RequireUpToDate: true},
	}
}

// readySnapshotBase satisfies every row-7 condition except Ship. The adapter
// under test is the only thing that fills Ship before Evaluate sees the
// snapshot, so this reproduces the production shape of the regression.
func readySnapshotBase() domain.Snapshot {
	return domain.Snapshot{
		State:            "OPEN",
		HeadSHA:          "sha-ready",
		WatchHeadSHA:     "sha-ready",
		Rollup:           "SUCCESS",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		SuitesValid:      true,
	}
}

// TestReadyReachableWithObservedShipFacts proves the ready verdict is reachable
// from observed Watch data. T3.72 added ShipAllowed to Evaluate but left the
// facts unpopulated; here a Snapshot with green required checks passes through
// the adapter and then through Evaluate. The negative rows prove that an
// unobserved fact stays false instead of being silently granted.
func TestReadyReachableWithObservedShipFacts(t *testing.T) {
	cases := []struct {
		name      string
		checks    map[string]domain.Check
		wantFacts domain.ShipFacts
		want      domain.Verdict
	}{
		{
			name:      "green build audit and test make ready reachable",
			checks:    greenRequiredChecks("build", "audit", "test"),
			wantFacts: domain.ShipFacts{Build: true, Audit: true, TDD: true},
			want:      domain.VerdictReady,
		},
		{
			name:      "green build and audit without a test fact stay honest",
			checks:    greenRequiredChecks("build", "audit"),
			wantFacts: domain.ShipFacts{Build: true, Audit: true},
			want:      domain.VerdictWatching,
		},
		{
			name: "a failed required build is not an observed fact",
			checks: map[string]domain.Check{
				"build": {State: "FAILURE", Conclusion: "FAILURE", Required: true},
				"audit": {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
				"test":  {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
			},
			wantFacts: domain.ShipFacts{Audit: true, TDD: true},
			want:      domain.VerdictWatching,
		},
		{
			name: "green non-required checks are not facts",
			checks: map[string]domain.Check{
				"build": {State: "SUCCESS", Conclusion: "SUCCESS", Required: false},
				"audit": {State: "SUCCESS", Conclusion: "SUCCESS", Required: false},
				"test":  {State: "SUCCESS", Conclusion: "SUCCESS", Required: false},
			},
			wantFacts: domain.ShipFacts{},
			want:      domain.VerdictWatching,
		},
		{
			name:      "no observed checks cannot grant ready",
			checks:    map[string]domain.Check{},
			wantFacts: domain.ShipFacts{},
			want:      domain.VerdictWatching,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := readyWatchWithChecks(tc.checks)
			gotFacts := shipFactsFromWatch(w)
			if gotFacts != tc.wantFacts {
				t.Fatalf("shipFactsFromWatch = %+v, want %+v", gotFacts, tc.wantFacts)
			}

			s := withShipFacts(readySnapshotBase(), w)
			got, wake := domain.Evaluate(s, w.Policy)
			if got != tc.want {
				t.Fatalf("Evaluate = %q, want %q (facts %+v)", got, tc.want, gotFacts)
			}
			if tc.want == domain.VerdictReady && len(wake) == 0 {
				t.Fatal("a ready verdict must wake the orchestrator")
			}
		})
	}
}

// --- Background task handle tests (ADR 0048 feature 3, T3.113) -------------

// testTaskClock drives the wait contract without real sleeps. By default its
// After channels stay pending; fireImmediately pre-fills them so a wait takes
// the timeout branch synchronously. afterCalled signals that Wait registered a
// timer, so a concurrency test knows the wait reached its select.
type testTaskClock struct {
	mu              sync.Mutex
	now             time.Time
	fireImmediately bool
	afters          []<-chan time.Time
	afterCalled     chan struct{}
}

func (c *testTaskClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testTaskClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *testTaskClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	if c.fireImmediately {
		ch <- c.Now().Add(d)
	}
	c.mu.Lock()
	c.afters = append(c.afters, ch)
	c.mu.Unlock()
	if c.afterCalled != nil {
		select {
		case c.afterCalled <- struct{}{}:
		default:
		}
	}
	return ch
}

func TestBackgroundTaskOutputByOffset(t *testing.T) {
	clock := &testTaskClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	task := NewBackgroundTask("bg-output", clock)
	if task.Handle() != "bg-output" {
		t.Fatalf("handle = %q, want bg-output", task.Handle())
	}

	empty := task.Output(0)
	if len(empty.Data) != 0 || empty.NextOffset != 0 {
		t.Fatalf("empty output = %+v", empty)
	}

	task.AppendOutput([]byte("hello "))
	task.AppendOutput([]byte("world"))

	full := task.Output(0)
	if string(full.Data) != "hello world" || full.NextOffset != 11 {
		t.Fatalf("full output = %+v", full)
	}

	tail := task.Output(6)
	if string(tail.Data) != "world" || tail.NextOffset != 11 {
		t.Fatalf("offset 6 output = %+v", tail)
	}

	negative := task.Output(-5)
	if string(negative.Data) != "hello world" || negative.NextOffset != 11 {
		t.Fatalf("negative offset output = %+v", negative)
	}

	past := task.Output(99)
	if len(past.Data) != 0 || past.NextOffset != 11 {
		t.Fatalf("past-end output = %+v", past)
	}

	// The returned bytes are a copy: a caller cannot mutate stored output.
	full.Data[0] = 'X'
	if got := string(task.Output(0).Data); got != "hello world" {
		t.Fatalf("stored output was mutated: %q", got)
	}

	// A later append is visible to a read from the previous NextOffset.
	task.AppendOutput([]byte("!"))
	next := task.Output(full.NextOffset)
	if string(next.Data) != "!" || next.NextOffset != 12 {
		t.Fatalf("continuation output = %+v", next)
	}
}

func TestBackgroundTaskWaitTimeout(t *testing.T) {
	clock := &testTaskClock{
		now:             time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		fireImmediately: true,
	}
	task := NewBackgroundTask("bg-timeout", clock)

	res := task.Wait(context.Background(), 250*time.Millisecond)
	if !res.TimedOut || res.Done || res.WaitedMs != 250 {
		t.Fatalf("timeout wait = %+v, want timedOut, not done, waitedMs=250", res)
	}
	if len(clock.afters) != 1 {
		t.Fatalf("timer registrations = %d, want 1", len(clock.afters))
	}

	zeroClock := &testTaskClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), fireImmediately: true}
	zeroTask := NewBackgroundTask("bg-zero", zeroClock)
	zero := zeroTask.Wait(context.Background(), 0)
	if !zero.TimedOut || zero.Done || zero.WaitedMs != 0 {
		t.Fatalf("zero timeout wait = %+v", zero)
	}
	if len(zeroClock.afters) != 0 {
		t.Fatalf("zero timeout registered %d timers, want 0", len(zeroClock.afters))
	}
}

func TestBackgroundTaskWaitDone(t *testing.T) {
	clock := &testTaskClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	task := NewBackgroundTask("bg-done", clock)
	task.Complete("ok")

	res := task.Wait(context.Background(), time.Second)
	if res.TimedOut || !res.Done || res.WaitedMs != 0 {
		t.Fatalf("done wait = %+v, want done, not timed out, waitedMs=0", res)
	}
	if len(clock.afters) != 0 {
		t.Fatalf("already-done wait registered %d timers, want 0", len(clock.afters))
	}
}

func TestBackgroundTaskWaitUnblocksOnComplete(t *testing.T) {
	clock := &testTaskClock{
		now:         time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		afterCalled: make(chan struct{}, 1),
	}
	task := NewBackgroundTask("bg-unblock", clock)
	resCh := make(chan TaskWaitResult, 1)
	go func() { resCh <- task.Wait(context.Background(), 5*time.Second) }()

	select {
	case <-clock.afterCalled:
	case <-time.After(time.Second):
		t.Fatal("Wait did not register its timer")
	}

	clock.Advance(40 * time.Millisecond)
	task.Complete("done")

	select {
	case res := <-resCh:
		if res.TimedOut || !res.Done || res.WaitedMs != 40 {
			t.Fatalf("unblocked wait = %+v, want done, not timed out, waitedMs=40", res)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after completion")
	}
}

func TestBackgroundTaskCompletionNotification(t *testing.T) {
	task := NewBackgroundTask("bg-notify", nil)
	if _, ok := task.Completion(); ok {
		t.Fatal("Completion reported done before Complete")
	}
	select {
	case <-task.Done():
		t.Fatal("Done channel closed before Complete")
	default:
	}

	task.AppendOutput([]byte("tail"))
	task.Complete("terminal-A")
	task.Complete("terminal-B") // duplicate must be a no-op

	select {
	case <-task.Done():
	default:
		t.Fatal("Done channel not closed after Complete")
	}

	comp, ok := task.Completion()
	if !ok || comp.Handle != "bg-notify" || comp.Terminal != "terminal-A" {
		t.Fatalf("completion = %+v, ok=%v", comp, ok)
	}
	again, ok := task.Completion()
	if !ok || again != comp {
		t.Fatalf("second completion = %+v, ok=%v, want same value", again, ok)
	}
	if got := string(task.Output(0).Data); got != "tail" {
		t.Fatalf("output after completion = %q, want tail", got)
	}
}

func TestBackgroundTaskWaitContextCancel(t *testing.T) {
	clock := &testTaskClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	task := NewBackgroundTask("bg-cancel", clock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := task.Wait(ctx, time.Second)
	if res.TimedOut || res.Done || res.WaitedMs != 0 {
		t.Fatalf("cancelled wait = %+v, want not timed out, not done, waitedMs=0", res)
	}
}

func TestTaskRegistry(t *testing.T) {
	reg := NewTaskRegistry(&testTaskClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)})
	if _, err := reg.Start(""); err == nil {
		t.Fatal("Start accepted an empty handle")
	}

	first, err := reg.Start("job-1")
	if err != nil {
		t.Fatalf("Start job-1: %v", err)
	}
	if first.Handle() != "job-1" {
		t.Fatalf("first handle = %q", first.Handle())
	}
	if _, err := reg.Start("job-1"); err == nil {
		t.Fatal("Start accepted a duplicate handle")
	}
	if _, err := reg.Start("job-2"); err != nil {
		t.Fatalf("Start job-2: %v", err)
	}

	got, ok := reg.Get("job-1")
	if !ok || got != first {
		t.Fatalf("Get job-1 = %p, ok=%v, want first task", got, ok)
	}
	if _, ok := reg.Get("missing"); ok {
		t.Fatal("Get returned a task for an unknown handle")
	}
}

// --- MCP protocol tasks/get contract tests (ADR 0056 feature 1) ------------

// TestTasksGetReturnsIDImmediately proves the create call hands back a taskId
// at once: the result is already usable by tasks/get, the task is still
// working (completion has not been waited for), and the clock did not move.
func TestTasksGetReturnsIDImmediately(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := &stepClock{t: base}
	store := NewProtocolTaskStore(clock)

	created, err := store.Start(60_000, 5_000)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created.ResultType != TaskResultType {
		t.Fatalf("create resultType = %q, want %q", created.ResultType, TaskResultType)
	}
	if created.TaskID == "" {
		t.Fatal("create returned an empty taskId")
	}
	if created.Status != TaskStateWorking {
		t.Fatalf("create status = %q, want %q", created.Status, TaskStateWorking)
	}
	if created.TTLMs == nil || *created.TTLMs != 60_000 {
		t.Fatalf("create ttlMs = %v, want 60000", created.TTLMs)
	}
	if created.PollIntervalMs != 5_000 {
		t.Fatalf("pollIntervalMs = %d, want 5000", created.PollIntervalMs)
	}
	if !clock.t.Equal(base) {
		t.Fatalf("Start advanced the clock to %v; it must not wait", clock.t)
	}
	if _, err := time.Parse(time.RFC3339Nano, created.CreatedAt); err != nil {
		t.Fatalf("createdAt is not RFC3339: %q: %v", created.CreatedAt, err)
	}

	got, err := store.Get(created.TaskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ResultType != taskResultTypeComplete || got.TaskID != created.TaskID || got.Status != TaskStateWorking {
		t.Fatalf("get result = %+v, want complete/%s/working", got, created.TaskID)
	}
	if n := len(store.Notifications()); n != 0 {
		t.Fatalf("Start published %d notifications; creation must be immediate", n)
	}
}

// TestTasksGetReturnsResultLater shows the connection is not held for the
// result: tasks/get first reports a working task, and only after a later
// update does it carry the completed result.
func TestTasksGetReturnsResultLater(t *testing.T) {
	store := NewProtocolTaskStore(&stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	created, err := store.Start(0, 1_000)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	first, err := store.Get(created.TaskID)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if first.Status != TaskStateWorking || len(first.Result) != 0 {
		t.Fatalf("first get = %+v, want working with no result", first)
	}

	updated, err := store.Update(created.TaskID, TaskPatch{
		Status:        TaskStateCompleted,
		StatusMessage: "done",
		Result:        json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != TaskStateCompleted || !updated.Status.Terminal() {
		t.Fatalf("updated status = %q, want a terminal completed state", updated.Status)
	}

	last, err := store.Get(created.TaskID)
	if err != nil {
		t.Fatalf("last Get: %v", err)
	}
	if last.ResultType != taskResultTypeComplete || last.Status != TaskStateCompleted {
		t.Fatalf("last get = %+v, want complete/completed", last)
	}
	if string(last.Result) != `{"ok":true}` {
		t.Fatalf("result = %s, want %s", last.Result, `{"ok":true}`)
	}
}

// TestTasksGetTTLExpiry enforces the contract TTL: the task is readable until
// the deadline, then gone. Unknown and empty ids keep their own codes.
func TestTasksGetTTLExpiry(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	store := NewProtocolTaskStore(clock)
	created, err := store.Start(1_000, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := store.Get(created.TaskID); err != nil {
		t.Fatalf("Get at creation: %v", err)
	}
	clock.Advance(999 * time.Millisecond)
	if _, err := store.Get(created.TaskID); err != nil {
		t.Fatalf("Get before TTL: %v", err)
	}
	clock.Advance(time.Millisecond)
	_, err = store.Get(created.TaskID)
	assertCode(t, err, domain.CodeGone)

	_, err = store.Get("never-issued")
	assertCode(t, err, domain.CodeWatchNotFound)
	_, err = store.Get("")
	assertCode(t, err, domain.CodeInvalidInput)
}

// TestTasksGetTTLUnlimited covers the null case: ttlMs is emitted as null and
// an unlimited task survives any clock advance.
func TestTasksGetTTLUnlimited(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	store := NewProtocolTaskStore(clock)
	created, err := store.Start(0, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created.TTLMs != nil {
		t.Fatalf("ttlMs = %d, want null for unlimited", *created.TTLMs)
	}
	raw, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"ttlMs":null`) {
		t.Fatalf("ttlMs not emitted as null: %s", raw)
	}
	clock.Advance(24 * time.Hour)
	if _, err := store.Get(created.TaskID); err != nil {
		t.Fatalf("unlimited task expired: %v", err)
	}
}

// TestTasksGetNotificationsProgressAndResult checks both notifications of the
// contract: a working update is progress, a terminal update is a result, and
// each carries the full task in notifications/tasks order.
func TestTasksGetNotificationsProgressAndResult(t *testing.T) {
	store := NewProtocolTaskStore(&stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	created, err := store.Start(0, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := store.Update(created.TaskID, TaskPatch{Status: TaskStateWorking, StatusMessage: "halfway"}); err != nil {
		t.Fatalf("progress Update: %v", err)
	}
	if _, err := store.Update(created.TaskID, TaskPatch{
		Status: TaskStateCompleted,
		Result: json.RawMessage(`{"v":1}`),
	}); err != nil {
		t.Fatalf("result Update: %v", err)
	}

	notes := store.Notifications()
	if len(notes) != 2 {
		t.Fatalf("notifications = %d, want 2", len(notes))
	}
	for i, n := range notes {
		if n.Method != taskStatusNotificationMethod {
			t.Errorf("notification %d method = %q, want %q", i, n.Method, taskStatusNotificationMethod)
		}
		if n.Params.TaskID != created.TaskID {
			t.Errorf("notification %d taskId = %q, want %q", i, n.Params.TaskID, created.TaskID)
		}
	}
	if kind := notes[0].Params.Status.NotificationKind(); kind != NotificationProgress {
		t.Errorf("first notification kind = %q, want progress", kind)
	}
	if notes[0].Params.StatusMessage != "halfway" || len(notes[0].Params.Result) != 0 {
		t.Errorf("progress notification = %+v, want message and no result", notes[0].Params)
	}
	if kind := notes[1].Params.Status.NotificationKind(); kind != NotificationResult {
		t.Errorf("second notification kind = %q, want result", kind)
	}
	if string(notes[1].Params.Result) != `{"v":1}` {
		t.Errorf("result notification result = %s, want %s", notes[1].Params.Result, `{"v":1}`)
	}

	notes[0].Params.Status = TaskStateFailed
	if again := store.Notifications(); again[0].Params.Status != TaskStateWorking {
		t.Fatal("Notifications returned a mutable slice of the store history")
	}
}

// TestTasksGetTerminalIsFinal keeps a finished result stable and makes cancel
// a result notification that cannot run twice.
func TestTasksGetTerminalIsFinal(t *testing.T) {
	store := NewProtocolTaskStore(&stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	created, err := store.Start(0, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := store.Update(created.TaskID, TaskPatch{
		Status: TaskStateCompleted,
		Result: json.RawMessage(`{"n":1}`),
	}); err != nil {
		t.Fatalf("Update completed: %v", err)
	}
	if _, err := store.Update(created.TaskID, TaskPatch{Status: TaskStateWorking}); err == nil {
		t.Fatal("a terminal task was reopened by a late event")
	}
	got, err := store.Get(created.TaskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != TaskStateCompleted || string(got.Result) != `{"n":1}` {
		t.Fatalf("late event changed the result: %+v", got)
	}

	second, err := store.Start(0, 0)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	cancelled, err := store.Cancel(second.TaskID, "user stopped")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != TaskStateCancelled || cancelled.StatusMessage != "user stopped" {
		t.Fatalf("cancelled task = %+v", cancelled)
	}
	if _, err := store.Cancel(second.TaskID, "again"); err == nil {
		t.Fatal("a cancelled task was cancelled twice")
	}

	results := 0
	for _, n := range store.Notifications() {
		if n.Params.Status.NotificationKind() == NotificationResult {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("result notifications = %d, want 2", results)
	}
}

// TestTasksGetWireShapeMatchesSchema asserts the contract uses the schema's
// field names and status values, with no local variant leaking onto the wire.
func TestTasksGetWireShapeMatchesSchema(t *testing.T) {
	store := NewProtocolTaskStore(&stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	created, err := store.Start(5_000, 2_000)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal create: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	for _, key := range []string{"taskId", "status", "createdAt", "lastUpdatedAt", "ttlMs", "pollIntervalMs", "resultType"} {
		if _, ok := obj[key]; !ok {
			t.Errorf("field %q missing from create result: %s", key, raw)
		}
	}
	if _, ok := obj["taskStatus"]; ok {
		t.Errorf("local variant taskStatus leaked into create result: %s", raw)
	}

	valid := map[TaskState]bool{
		TaskStateWorking: true, TaskStateInputRequired: true, TaskStateCompleted: true,
		TaskStateFailed: true, TaskStateCancelled: true,
	}
	if !valid[created.Status] {
		t.Fatalf("status %q is not a schema value", created.Status)
	}

	if _, err := store.Update(created.TaskID, TaskPatch{
		Status: TaskStateCompleted,
		Result: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := store.Get(created.TaskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal get: %v", err)
	}
	var gotObj map[string]json.RawMessage
	if err := json.Unmarshal(gotRaw, &gotObj); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if value := string(gotObj["resultType"]); value != `"complete"` {
		t.Errorf("tasks/get resultType = %s, want %s", value, `"complete"`)
	}
	if _, ok := gotObj["result"]; !ok {
		t.Errorf("completed tasks/get has no result field: %s", gotRaw)
	}
}

// TestNextStepForVerdicts pins the self-teaching contract: the four wake
// verdicts map to their action and command, every other verdict stays in the
// wait family so an agent never invents a command from prose.
func TestNextStepForVerdicts(t *testing.T) {
	cases := []struct {
		verdict domain.Verdict
		action  string
		command string
	}{
		{domain.VerdictKeepWorking, NextActionKeepWorking, ""},
		{domain.VerdictRebase, NextActionRebase, RebaseCommand},
		{domain.VerdictRetry, NextActionRetry, RetryCommand},
		{domain.VerdictBlocked, NextActionBlocked, ""},
		{domain.VerdictReady, NextActionWait, ""},
		{domain.VerdictWatching, NextActionWait, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.verdict), func(t *testing.T) {
			action, command, hint := nextStepFor(tc.verdict)
			if action != tc.action || command != tc.command {
				t.Errorf("nextStepFor(%s) = %q/%q, want %q/%q", tc.verdict, action, command, tc.action, tc.command)
			}
			if strings.TrimSpace(hint) == "" {
				t.Error("nextStepFor returned an empty hint")
			}
		})
	}
	if !strings.Contains(RebaseCommand, "git rebase origin/main") ||
		!strings.Contains(RebaseCommand, "git push --force-with-lease") {
		t.Errorf("rebase command is incomplete: %q", RebaseCommand)
	}
	if RetryCommand != "gh run rerun --failed" {
		t.Errorf("retry command = %q", RetryCommand)
	}
}

// --- W11-B: failure excerpts ------------------------------------------------

// fakeExcerptSource returns a canned excerpt and counts calls per check name.
type fakeExcerptSource struct {
	mu     sync.Mutex
	byName map[string]string
	calls  map[string]int
}

func newFakeExcerptSource(answers map[string]string) *fakeExcerptSource {
	return &fakeExcerptSource{
		byName: answers,
		calls:  map[string]int{},
	}
}

func (s *fakeExcerptSource) FailureExcerpt(_ context.Context, _, _, checkName string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[checkName]++
	return s.byName[checkName], nil
}

func (s *fakeExcerptSource) callCount(checkName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[checkName]
}

// countingExcerptCache is an in-memory failureExcerptCache that records how
// many Get and Put calls it saw, so a repeat read can prove the source was not
// called again.
type countingExcerptCache struct {
	mu   sync.Mutex
	data map[string]string
	gets int
	puts int
}

func newCountingExcerptCache() *countingExcerptCache {
	return &countingExcerptCache{data: map[string]string{}}
}

func (c *countingExcerptCache) Get(_ context.Context, watchID, headSHA, checkName string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	v, ok := c.data[watchID+"\x00"+headSHA+"\x00"+checkName]
	return v, ok, nil
}

func (c *countingExcerptCache) Put(_ context.Context, watchID, headSHA, checkName, excerpt string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	c.data[watchID+"\x00"+headSHA+"\x00"+checkName] = excerpt
	return nil
}

func (c *countingExcerptCache) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.puts
}

func excerptTestHandler(t *testing.T, src FailureExcerptSource, cache failureExcerptCache) (*GetResultHandler, domain.Watch) {
	t.Helper()
	w := resultFixture(domain.VerdictKeepWorking)
	o := resultObligation(w, "session-E")
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})
	h.SetFailureExcerptSource(src)
	if cache != nil {
		h.SetFailureExcerptCache(cache)
	}
	return h, w
}

// TestFailureExcerptAnnotatedFailure proves an annotation reaches both the
// per-check field and the aggregate, while green and skipped checks stay out.
func TestFailureExcerptAnnotatedFailure(t *testing.T) {
	src := newFakeExcerptSource(map[string]string{
		"ci/failure": "main.go:17: nil deref in handle()",
	})
	h, w := excerptTestHandler(t, src, nil)
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !strings.Contains(out.FailureExcerpt, "main.go:17") {
		t.Errorf("aggregate excerpt = %q", out.FailureExcerpt)
	}
	for _, fc := range out.FailedChecks {
		if fc.Name == "ci/failure" && fc.FailureExcerpt != "main.go:17: nil deref in handle()" {
			t.Errorf("ci/failure excerpt = %q", fc.FailureExcerpt)
		}
	}
	if got := src.callCount("ci/failure"); got != 1 {
		t.Errorf("source calls = %d, want 1", got)
	}
}

// TestFailureExcerptRedaction proves a token in an annotation never reaches
// the response, and that XML/JSON metacharacters are escaped.
func TestFailureExcerptRedaction(t *testing.T) {
	raw := `auth ghp_abcdefghijklmnopqrstuvwxyz0123456789 <script>alert("x")</script> & 'q'`
	src := newFakeExcerptSource(map[string]string{"ci/failure": raw})
	h, w := excerptTestHandler(t, src, nil)
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if strings.Contains(out.FailureExcerpt, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("token leaked into excerpt: %q", out.FailureExcerpt)
	}
	if !strings.Contains(out.FailureExcerpt, "[REDACTED]") {
		t.Errorf("redaction marker missing: %q", out.FailureExcerpt)
	}
	for _, bad := range []string{"<script>", `alert("x")`, " & ", "'q'"} {
		if strings.Contains(out.FailureExcerpt, bad) {
			t.Errorf("unescaped %q in excerpt %q", bad, out.FailureExcerpt)
		}
	}
	if !strings.Contains(out.FailureExcerpt, "&lt;script&gt;") {
		t.Errorf("markup not escaped: %q", out.FailureExcerpt)
	}
}

// TestFailureExcerptTruncation pins the 2 KB ceiling on both the aggregate and
// each per-check excerpt, including a rune boundary.
func TestFailureExcerptTruncation(t *testing.T) {
	src := newFakeExcerptSource(map[string]string{
		"ci/failure": strings.Repeat("a", 4096) + "€",
		"ci/flaky":   strings.Repeat("b", 4096),
	})
	h, w := excerptTestHandler(t, src, nil)
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(out.FailureExcerpt) > failureExcerptLimit {
		t.Errorf("aggregate length = %d, want <= %d", len(out.FailureExcerpt), failureExcerptLimit)
	}
	if !utf8.ValidString(out.FailureExcerpt) {
		t.Error("aggregate excerpt is not valid UTF-8 after truncation")
	}
	for _, fc := range out.FailedChecks {
		if len(fc.FailureExcerpt) > failureExcerptLimit {
			t.Errorf("%s excerpt length = %d, want <= %d", fc.Name, len(fc.FailureExcerpt), failureExcerptLimit)
		}
		if !utf8.ValidString(fc.FailureExcerpt) {
			t.Errorf("%s excerpt is not valid UTF-8 after truncation", fc.Name)
		}
	}
}

// TestFailureExcerptCacheAvoidsSecondFetch proves one fetch per triple: a
// second Result on the same Watch reads the cache instead of calling GitHub.
func TestFailureExcerptCacheAvoidsSecondFetch(t *testing.T) {
	src := newFakeExcerptSource(map[string]string{"ci/failure": "cached reason"})
	cache := newCountingExcerptCache()
	h, w := excerptTestHandler(t, src, cache)
	req := sessionReq("session-E")
	for i := 0; i < 2; i++ {
		if _, err := h.Result(context.Background(), req, GetResultInput{WatchID: w.ID}); err != nil {
			t.Fatalf("Result %d: %v", i, err)
		}
	}
	if got := src.callCount("ci/failure"); got != 1 {
		t.Errorf("source calls after two reads = %d, want 1", got)
	}
	gets, puts := cache.counts()
	if puts != 2 || gets != 4 {
		t.Errorf("cache gets/puts = %d/%d, want 4/2 (two checks per read)", gets, puts)
	}
}

// TestFailureExcerptOptionalKeepsStableSchema pins the W9-C contract: the new
// field is present in the derived schema but not required, and a response from
// a server with no source omits it entirely.
func TestFailureExcerptOptionalKeepsStableSchema(t *testing.T) {
	schema, err := OutputSchema[GetResultOutput]()
	if err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	if _, ok := schema.Properties["failureExcerpt"]; !ok {
		t.Fatal("get_result schema is missing failureExcerpt")
	}
	for _, required := range schema.Required {
		if required == "failureExcerpt" {
			t.Fatal("failureExcerpt must be optional, not required")
		}
	}

	h, w := excerptTestHandler(t, nil, nil)
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if out.FailureExcerpt != "" {
		t.Errorf("no-source excerpt = %q, want empty", out.FailureExcerpt)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"failureExcerpt"`) {
		t.Errorf("optional field present without a source: %s", raw)
	}
}

// TestEscapeUntrusted pins the escaping table used for every excerpt.
func TestEscapeUntrusted(t *testing.T) {
	in := `<a href="x">& 'y'` + "\n\t\x00"
	want := `&lt;a href=&quot;x&quot;&gt;&amp; &#39;y&#39;\u000a\u0009\u0000`
	if got := escapeUntrusted(in); got != want {
		t.Errorf("escapeUntrusted(%q) = %q, want %q", in, got, want)
	}
}

// TestEnvFailureExcerptSource exercises the zero-wiring REST path against a
// local server, including the failed-conclusion filter and the 2 KB boundary.
func TestEnvFailureExcerptSource(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") == "" {
			t.Error("request carried no Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r/commits/sha/check-runs":
			fmt.Fprint(w, `{"check_runs":[{"id":42,"name":"ci/failure","conclusion":"failure"},{"id":43,"name":"ci/ok","conclusion":"success"}]}`)
		case "/repos/o/r/check-runs/42/annotations":
			fmt.Fprint(w, `[{"path":"main.go","start_line":9,"title":"boom","message":"bad东西"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("BEACON_GITHUB_TOKEN", "test-token")
	t.Setenv("BEACON_GITHUB_API", srv.URL)
	src := newEnvFailureExcerptSource()
	if src == nil {
		t.Fatal("env source is nil with a token set")
	}
	got, err := src.FailureExcerpt(context.Background(), "o/r", "sha", "ci/failure")
	if err != nil {
		t.Fatalf("FailureExcerpt: %v", err)
	}
	if !strings.Contains(got, "main.go:9") || !strings.Contains(got, "boom") {
		t.Errorf("excerpt = %q", got)
	}
	if len(paths) != 2 {
		t.Errorf("REST paths = %v, want two calls", paths)
	}
}

// TestSQLFailureExcerptCachePersists exercises the real SQLite cache: a miss
// before Put, a hit after Put, and a stable value on a repeat Put. The key is
// the (watchID, headSHA, checkName) triple, so changing the SHA misses.
func TestSQLFailureExcerptCachePersists(t *testing.T) {
	o := resultObligation(resultFixture(domain.VerdictKeepWorking), "session-C")
	writer, _ := newStampTestDB(t, o)
	cache := newSQLFailureExcerptCache(writer)

	if got, ok, err := cache.Get(context.Background(), "w-1", "sha", "ci/failure"); err != nil || ok || got != "" {
		t.Fatalf("cold Get = %q, %v, %v; want empty miss", got, ok, err)
	}
	if err := cache.Put(context.Background(), "w-1", "sha", "ci/failure", "first"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok, err := cache.Get(context.Background(), "w-1", "sha", "ci/failure"); err != nil || !ok || got != "first" {
		t.Fatalf("warm Get = %q, %v, %v; want first", got, ok, err)
	}
	if err := cache.Put(context.Background(), "w-1", "sha", "ci/failure", "second"); err != nil {
		t.Fatalf("repeat Put: %v", err)
	}
	if got, _, _ := cache.Get(context.Background(), "w-1", "sha", "ci/failure"); got != "first" {
		t.Errorf("repeat Put overwrote cache: %q", got)
	}
	if _, ok, _ := cache.Get(context.Background(), "w-1", "new-sha", "ci/failure"); ok {
		t.Error("a new head SHA must miss the cache")
	}
}
