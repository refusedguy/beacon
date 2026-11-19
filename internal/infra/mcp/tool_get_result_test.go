package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
