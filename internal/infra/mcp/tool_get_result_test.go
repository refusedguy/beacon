package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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

// ackFixtureSetup adjusts the Watch and obligation before the fixture is
// stored. A setup that changes the Watch identity must rebuild the obligation.
type ackFixtureSetup func(w *domain.Watch, o *domain.Obligation)

// ackFixtureWithWatch replaces the verdict-derived Watch with an already built
// one and rebuilds its obligation.
func ackFixtureWithWatch(w domain.Watch, session string) ackFixtureSetup {
	return func(cur *domain.Watch, o *domain.Obligation) {
		*cur = w
		*o = resultObligation(w, session)
	}
}

// newAckResultFixture builds a stored Watch, its obligation and a stamper that
// already knows the obligation. Callers keep the w, o, store and stamper names
// used by the ack and get_result tests.
func newAckResultFixture(t *testing.T, verdict domain.Verdict, session string, setups ...ackFixtureSetup) (domain.Watch, domain.Obligation, *memStore, *recordingStamper) {
	t.Helper()
	w := resultFixture(verdict)
	o := resultObligation(w, session)
	for _, setup := range setups {
		setup(&w, &o)
	}
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	return w, o, store, stamper
}

func sortedFailedChecks(in []FailedCheck) []FailedCheck {
	out := append([]FailedCheck(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IsNotFound keeps the not-found contract for this double. memStore reports an
// empty selection with a plain sentinel, so the result paths need this
// classifier to tell it apart from a storage failure.
func (m *memStore) IsNotFound(err error) bool {
	return errors.Is(err, errMissing)
}

// failingResultStore reports a storage failure on every read. It deliberately
// does not implement resultStoreNotFound: a disk or SQL failure is not an empty
// selection.
type failingResultStore struct {
	err error
}

func (s *failingResultStore) Watch(string) (domain.Watch, error) {
	return domain.Watch{}, s.err
}

func (s *failingResultStore) Obligation(string) (domain.Obligation, error) {
	return domain.Obligation{}, s.err
}

// failingObligationStore serves a Watch and fails only the obligation read, so
// the obligation call site can be checked on its own.
type failingObligationStore struct {
	watch domain.Watch
	err   error
}

func (s *failingObligationStore) Watch(string) (domain.Watch, error) {
	return s.watch, nil
}

func (s *failingObligationStore) Obligation(string) (domain.Obligation, error) {
	return domain.Obligation{}, s.err
}

// TestGetResultProjectsContractAndStampsRead covers both verdicts named in the
// card. It checks the projected fields, the failed-check shape without logs,
// the retry budget and the idempotent readAt stamp.
func TestGetResultProjectsContractAndStampsRead(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictReady, domain.VerdictKeepWorking} {
		t.Run(string(verdict), func(t *testing.T) {
			clock := &stepClock{t: time.Date(2026, 11, 19, 10, 5, 0, 0, time.UTC)}
			w, o, store, stamper := newAckResultFixture(t, verdict, "session-G")
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

// TestGetResultEchoesWatchConfigID proves get_result carries the Watch's
// configuration identity from ADR 0087 (T5.36). A Watch recorded before the
// field existed reads back as empty and keeps the previous JSON shape, so no
// live configuration probe is needed to verify the contract.
func TestGetResultEchoesWatchConfigID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		configID string
	}{
		{name: "recorded config", configID: "ci=stub,gpt=codex"},
		{name: "old watch", configID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, store, stamper := newAckResultFixture(t, domain.VerdictReady, "session-C", func(w *domain.Watch, o *domain.Obligation) {
				w.ConfigID = tc.configID
			})
			h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

			out, err := h.Result(context.Background(), sessionReq("session-C"), GetResultInput{WatchID: w.ID})
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			if out.ConfigID != tc.configID {
				t.Errorf("configId = %q, want %q", out.ConfigID, tc.configID)
			}

			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal output: %v", err)
			}
			wantKey := strings.Contains(string(raw), `"configId":`)
			if wantKey != (tc.configID != "") {
				t.Errorf("configId key present = %v, want %v in %s", wantKey, tc.configID != "", raw)
			}
		})
	}
}

// TestGetResultRetryBudgetWithoutFlakyFailure checks that a watch with no
// flaky-matched failure reports no retry budget.
func TestGetResultRetryBudgetWithoutFlakyFailure(t *testing.T) {
	w, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-B", func(w *domain.Watch, o *domain.Obligation) {
		w.Flaky = domain.FlakyConfig{}
	})
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

// TestGetResultCancelledCheckIsFailed verifies that a CANCELLED conclusion is
// projected as a failed check (T3.401). The cancelled check has no failure state,
// so the conclusion is what makes it fail.
func TestGetResultCancelledCheckIsFailed(t *testing.T) {
	w := domain.Watch{
		ID:       "w-cancel",
		Repo:     "o/r",
		PR:       2,
		HeadSHA:  "sha",
		Status:   domain.StatusNotifying,
		Verdict:  domain.VerdictKeepWorking,
		Revision: 1,
		Checks: map[string]domain.Check{
			"ci/cancelled": {Conclusion: "CANCELLED", Required: true, DetailsURL: "https://ci/cancel"},
			"ci/ok":        {State: "SUCCESS", Conclusion: "SUCCESS", Required: true, DetailsURL: "https://ci/ok"},
		},
		Merge: domain.MergeInfo{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
	}
	_, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-C", ackFixtureWithWatch(w, "session-C"))
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

	out, err := h.Result(context.Background(), sessionReq("session-C"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	want := []FailedCheck{
		{Name: "ci/cancelled", Conclusion: "CANCELLED", DetailsURL: "https://ci/cancel", Required: true},
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
}

// TestGetResultCancelledStateIsFailed verifies that a check cancelled at the
// state level reaches failedChecks even when no conclusion was recorded
// (T3.402). The verdict side treats both spellings as failures, so get_result
// has to match.
func TestGetResultCancelledStateIsFailed(t *testing.T) {
	for _, state := range []string{"CANCELLED", "CANCELED"} {
		t.Run(state, func(t *testing.T) {
			w := domain.Watch{
				ID:       "w-cancel-state",
				Repo:     "o/r",
				PR:       3,
				HeadSHA:  "sha",
				Status:   domain.StatusNotifying,
				Verdict:  domain.VerdictKeepWorking,
				Revision: 1,
				Checks: map[string]domain.Check{
					"ci/cancelled-state": {State: state, Required: true, DetailsURL: "https://ci/cancel-state"},
					"ci/ok":              {State: "SUCCESS", Conclusion: "SUCCESS", Required: true, DetailsURL: "https://ci/ok"},
				},
				Merge: domain.MergeInfo{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
			}
			_, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-CS", ackFixtureWithWatch(w, "session-CS"))
			h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})

			out, err := h.Result(context.Background(), sessionReq("session-CS"), GetResultInput{WatchID: w.ID})
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			want := []FailedCheck{
				{Name: "ci/cancelled-state", DetailsURL: "https://ci/cancel-state", Required: true},
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
		})
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

// TestResultReadFailureIsPersistFailed covers the three Watch reads on the
// result paths. A storage failure keeps PERSIST_FAILED instead of being
// reported as a missing Watch.
func TestResultReadFailureIsPersistFailed(t *testing.T) {
	cause := "store: disk I/O failure"
	h := NewGetResultHandler(&failingResultStore{err: errors.New(cause)}, newRecordingStamper(), &stepClock{t: time.Now()})
	ctx := context.Background()

	_, err := h.Result(ctx, sessionReq("session-V"), GetResultInput{WatchID: "w-1"})
	assertCode(t, err, domain.CodePersistFailed)
	if !strings.Contains(err.Error(), cause) {
		t.Errorf("error %q does not keep the cause %q", err.Error(), cause)
	}

	_, err = h.GradeResult(ctx, "w-1", DefaultResultRubric(), 0, false)
	assertCode(t, err, domain.CodePersistFailed)

	_, err = h.GradeExternalAction(ctx, "w-1", DefaultResultRubric(), 0, true, false, "lease")
	assertCode(t, err, domain.CodePersistFailed)
}

// TestResultObligationReadFailureIsPersistFailed covers the obligation read in
// get_result. A storage failure there also keeps PERSIST_FAILED.
func TestResultObligationReadFailureIsPersistFailed(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	cause := "store: obligation disk I/O failure"
	h := NewGetResultHandler(&failingObligationStore{watch: w, err: errors.New(cause)}, newRecordingStamper(), &stepClock{t: time.Now()})

	_, err := h.Result(context.Background(), sessionReq("session-V"), GetResultInput{WatchID: w.ID})
	assertCode(t, err, domain.CodePersistFailed)
	if !strings.Contains(err.Error(), cause) {
		t.Errorf("error %q does not keep the cause %q", err.Error(), cause)
	}
}

// TestResultReadMissingIsWatchNotFound covers the empty-selection case on all
// three result paths. The plain not-found sentinel still maps to
// WATCH_NOT_FOUND now that read failures map to PERSIST_FAILED.
func TestResultReadMissingIsWatchNotFound(t *testing.T) {
	h := NewGetResultHandler(newMemStore(), newRecordingStamper(), &stepClock{t: time.Now()})
	ctx := context.Background()

	_, err := h.Result(ctx, sessionReq("session-V"), GetResultInput{WatchID: "missing"})
	assertCode(t, err, domain.CodeWatchNotFound)

	_, err = h.GradeResult(ctx, "missing", DefaultResultRubric(), 0, false)
	assertCode(t, err, domain.CodeWatchNotFound)

	_, err = h.GradeExternalAction(ctx, "missing", DefaultResultRubric(), 0, true, false, "lease")
	assertCode(t, err, domain.CodeWatchNotFound)
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

// TestGradeResultBudgetExhaustionAsksHuman covers CV-09 at the pure grader: an
// exhausted budget asks for a human decision, while a satisfied result does not.
func TestGradeResultBudgetExhaustionAsksHuman(t *testing.T) {
	ev := gradeResult(resultFixture(domain.VerdictKeepWorking), DefaultResultRubric(), defaultOutcomeIterations, false)

	if ev.Outcome != OutcomeMaxIterationsReached {
		t.Fatalf("outcome = %q, want %q", ev.Outcome, OutcomeMaxIterationsReached)
	}
	if !ev.NeedsHumanDecision {
		t.Error("exhausted budget did not ask for a human decision")
	}
	if ev.ReadyVerdict != "" {
		t.Errorf("readyVerdict = %q, want empty: a parked run must not open merge", ev.ReadyVerdict)
	}

	below := gradeResult(resultFixture(domain.VerdictKeepWorking), DefaultResultRubric(), defaultOutcomeIterations-1, false)
	if below.NeedsHumanDecision {
		t.Error("budget below the cap asked for a human decision")
	}

	satisfied := gradeResult(greenWatch(), DefaultResultRubric(), 0, false)
	if satisfied.NeedsHumanDecision {
		t.Error("satisfied result asked for a human decision")
	}
	if satisfied.ReadyVerdict != domain.VerdictReady {
		t.Errorf("readyVerdict = %q, want %q", satisfied.ReadyVerdict, domain.VerdictReady)
	}
}

// TestGradeResultParksWatchWhenBudgetExhausted checks the handler side of
// CV-09: the result carries the question flag and the Watch reaches
// needs_input without creating a successor Watch.
func TestGradeResultParksWatchWhenBudgetExhausted(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	w, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-human")
	h := NewGetResultHandler(store, stamper, clock)

	ev, err := h.GradeResult(context.Background(), w.ID, DefaultResultRubric(), defaultOutcomeIterations, false)
	if err != nil {
		t.Fatalf("GradeResult: %v", err)
	}
	if !ev.NeedsHumanDecision {
		t.Fatal("result did not carry the question flag")
	}
	if ev.ReadyVerdict != "" {
		t.Errorf("readyVerdict = %q, want empty", ev.ReadyVerdict)
	}

	parked, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("read parked Watch: %v", err)
	}
	if parked.Status != domain.StatusNeedsInput || parked.Verdict != domain.VerdictNeedsInput {
		t.Errorf("parked Watch = %q/%q, want %q/%q", parked.Status, parked.Verdict, domain.StatusNeedsInput, domain.VerdictNeedsInput)
	}
	if parked.Revision != w.Revision+1 {
		t.Errorf("revision = %d, want %d", parked.Revision, w.Revision+1)
	}
	if len(store.watches) != 1 {
		t.Errorf("parking created %d Watches, want 1", len(store.watches))
	}
}

// TestMaxIterationsAnswerRaisesBudgetAndUnparks is the CV-09 contract: a human
// answer through ack(continueOf) resumes the same Watch, and a raised budget
// stops asking for a decision.
func TestMaxIterationsAnswerRaisesBudgetAndUnparks(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	w, o, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-answer")
	h := NewGetResultHandler(store, stamper, clock)

	first, err := h.GradeResult(context.Background(), w.ID, DefaultResultRubric(), defaultOutcomeIterations, false)
	if err != nil {
		t.Fatalf("GradeResult: %v", err)
	}
	if !first.NeedsHumanDecision {
		t.Fatal("first grade did not ask for a human decision")
	}
	parked, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("read parked Watch: %v", err)
	}
	if parked.Status != domain.StatusNeedsInput {
		t.Fatalf("status = %q, want %q", parked.Status, domain.StatusNeedsInput)
	}

	ack := NewAckHandler(store, stamper, clock)
	if _, err := ack.AckReceipt(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID, ContinueOf: w.ID,
	}); err != nil {
		t.Fatalf("ack continueOf: %v", err)
	}
	resumed, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("read resumed Watch: %v", err)
	}
	if resumed.Status != domain.StatusWatching || resumed.Verdict != domain.VerdictWatching {
		t.Errorf("answer did not resume the Watch: %q/%q", resumed.Status, resumed.Verdict)
	}
	if len(store.watches) != 1 {
		t.Errorf("answer created %d Watches, want 1", len(store.watches))
	}

	raised := DefaultResultRubric()
	raised.MaxIterations = defaultOutcomeIterations + 2
	second, err := h.GradeResult(context.Background(), w.ID, raised, defaultOutcomeIterations, false)
	if err != nil {
		t.Fatalf("GradeResult with raised budget: %v", err)
	}
	if second.NeedsHumanDecision {
		t.Error("raised budget still asked for a human decision")
	}
	if second.Outcome == OutcomeMaxIterationsReached {
		t.Error("raised budget still reported an exhausted budget")
	}
	final, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("read final Watch: %v", err)
	}
	if final.Status != domain.StatusWatching {
		t.Errorf("grading re-parked the Watch: status %q", final.Status)
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
	w, _, store, stamper := newAckResultFixture(t, domain.VerdictReady, "session-G", func(w *domain.Watch, o *domain.Obligation) {
		w.Status = domain.StatusClosed
		w.ClosedAt = time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)
	})
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
	w, o, store, _ := newAckResultFixture(t, domain.VerdictReady, "session-D", func(w *domain.Watch, o *domain.Obligation) {
		*w = greenWatch()
		*o = resultObligation(*w, "session-D")
		o.Attempts = []domain.Attempt{{N: 1, Outcome: domain.OutcomeIndeterminate, At: time.Now()}}
	})
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
	w, _, store, stamper := newAckResultFixture(t, domain.VerdictReady, "session-K")
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
// verdicts map to their action and command, the non-terminal ones stay in the
// wait family, and the terminal timeout ends the turn so an agent never
// invents a command from prose (BCX-11).
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
		{domain.VerdictTimeout, domain.NextActionDone, ""},
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

// TestTerminalVerdictTimeoutIsNotRetryableWait pins the get_result half of
// BCX-11: a terminal timeout verdict ends the turn and its hint names the
// long-poll it must not be confused with.
func TestTerminalVerdictTimeoutIsNotRetryableWait(t *testing.T) {
	action, command, hint := nextStepFor(domain.VerdictTimeout)
	if action != domain.NextActionDone {
		t.Errorf("nextStepFor(timeout) action = %q, want %q", action, domain.NextActionDone)
	}
	if action == NextActionWait {
		t.Error("terminal timeout verdict still tells the agent to wait")
	}
	if command != "" {
		t.Errorf("nextStepFor(timeout) command = %q, want empty", command)
	}
	if !strings.Contains(hint, "terminal") || !strings.Contains(hint, "long-poll") {
		t.Errorf("nextStepFor(timeout) hint = %q, want terminal and long-poll wording", hint)
	}
}

// --- W11-B: failure excerpts ------------------------------------------------

// fakeExcerptSource returns a canned excerpt and counts calls per check name.
// errByName lets a test make one check's fetch fail while others still succeed.
type fakeExcerptSource struct {
	mu        sync.Mutex
	byName    map[string]string
	errByName map[string]error
	calls     map[string]int
}

func newFakeExcerptSource(answers map[string]string) *fakeExcerptSource {
	return &fakeExcerptSource{
		byName:    answers,
		errByName: map[string]error{},
		calls:     map[string]int{},
	}
}

func (s *fakeExcerptSource) FailureExcerpt(_ context.Context, _, _, checkName string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[checkName]++
	return s.byName[checkName], s.errByName[checkName]
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
	w, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-E")
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

// TestFailureExcerptTruncationIsVisible proves a long annotation sets the flag,
// carries the marker and exposes the full CI log link (ATX-36).
func TestFailureExcerptTruncationIsVisible(t *testing.T) {
	src := newFakeExcerptSource(map[string]string{
		"ci/failure": strings.Repeat("a", 4096) + "€",
	})
	h, w := excerptTestHandler(t, src, nil)
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !out.FailureExcerptTruncated {
		t.Error("aggregate truncation did not set failureExcerptTruncated")
	}
	if !strings.HasSuffix(out.FailureExcerpt, failureExcerptTruncatedMarker) {
		t.Errorf("aggregate excerpt = %q, want suffix %q", out.FailureExcerpt, failureExcerptTruncatedMarker)
	}
	if out.FailureExcerptLogURL != "https://ci/1" {
		t.Errorf("log URL = %q, want %q", out.FailureExcerptLogURL, "https://ci/1")
	}
	for _, fc := range out.FailedChecks {
		if fc.Name == "ci/failure" && !strings.HasSuffix(fc.FailureExcerpt, failureExcerptTruncatedMarker) {
			t.Errorf("ci/failure excerpt = %q, want suffix %q", fc.FailureExcerpt, failureExcerptTruncatedMarker)
		}
	}
}

// TestFailureExcerptManyAnnotationsKeepMarker proves eight short annotations
// keep the truncation flag when the aggregate, not a single excerpt, is cut
// (ATX-36).
func TestFailureExcerptManyAnnotationsKeepMarker(t *testing.T) {
	answers := map[string]string{}
	w := resultFixture(domain.VerdictKeepWorking)
	w.Checks = map[string]domain.Check{}
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("ci/many-%d", i)
		w.Checks[name] = domain.Check{State: "FAILURE", Conclusion: "FAILURE", Required: true, DetailsURL: fmt.Sprintf("https://ci/many/%d", i)}
		answers[name] = strings.Repeat("x", 300)
	}
	src := newFakeExcerptSource(answers)
	_, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-E", ackFixtureWithWatch(w, "session-E"))
	h := NewGetResultHandler(store, stamper, &stepClock{t: time.Now()})
	h.SetFailureExcerptSource(src)

	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !out.FailureExcerptTruncated {
		t.Error("aggregate truncation of eight annotations did not set the flag")
	}
	if !strings.HasSuffix(out.FailureExcerpt, failureExcerptTruncatedMarker) {
		t.Errorf("aggregate excerpt = %q, want suffix %q", out.FailureExcerpt, failureExcerptTruncatedMarker)
	}
	if out.FailureExcerptLogURL == "" {
		t.Error("truncated aggregate has no full CI log link")
	}
	for _, fc := range out.FailedChecks {
		if strings.HasSuffix(fc.FailureExcerpt, failureExcerptTruncatedMarker) {
			t.Errorf("per-check excerpt %q was cut, want aggregate-only cut", fc.FailureExcerpt)
		}
	}
}

// TestFailureExcerptTruncationSurvivesCache proves a cache hit keeps the flag
// and the marker instead of re-fetching the source (ATX-36).
func TestFailureExcerptTruncationSurvivesCache(t *testing.T) {
	src := newFakeExcerptSource(map[string]string{"ci/failure": strings.Repeat("a", 4096)})
	cache := newCountingExcerptCache()
	h, w := excerptTestHandler(t, src, cache)
	if _, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID}); err != nil {
		t.Fatalf("first Result: %v", err)
	}
	src.byName["ci/failure"] = ""
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("second Result: %v", err)
	}
	if !out.FailureExcerptTruncated {
		t.Error("cached truncation lost the flag")
	}
	if !strings.HasSuffix(out.FailureExcerpt, failureExcerptTruncatedMarker) {
		t.Errorf("cached excerpt = %q, want suffix %q", out.FailureExcerpt, failureExcerptTruncatedMarker)
	}
	if got := src.callCount("ci/failure"); got != 1 {
		t.Errorf("source calls = %d, want 1", got)
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

// TestFailureExcerptSourceErrorIsVisible proves a failed fetch is reported to
// the agent instead of looking like a check with no annotations (EH-02). The
// flag appears on the failed check and in the aggregate, and survives a repeat
// read because a failed fetch is never cached as an empty success.
func TestFailureExcerptSourceErrorIsVisible(t *testing.T) {
	src := newFakeExcerptSource(map[string]string{"ci/failure": ""})
	src.errByName["ci/failure"] = errors.New("github excerpt: status 500")
	h, w := excerptTestHandler(t, src, nil)
	req := sessionReq("session-E")
	for i := 0; i < 2; i++ {
		out, err := h.Result(context.Background(), req, GetResultInput{WatchID: w.ID})
		if err != nil {
			t.Fatalf("Result %d: %v", i, err)
		}
		if !out.FailureExcerptUnavailable {
			t.Errorf("read %d: aggregate did not report the failed fetch", i)
		}
		if out.FailureExcerpt != "" {
			t.Errorf("read %d: aggregate excerpt = %q, want empty", i, out.FailureExcerpt)
		}
		for _, fc := range out.FailedChecks {
			switch fc.Name {
			case "ci/failure":
				if !fc.FailureExcerptUnavailable {
					t.Errorf("read %d: ci/failure lost the unavailability flag", i)
				}
			case "ci/flaky":
				if fc.FailureExcerptUnavailable {
					t.Errorf("read %d: check without annotations got the flag", i)
				}
			}
		}
	}
	if got := src.callCount("ci/failure"); got != 2 {
		t.Errorf("source calls = %d, want 2 (a failed fetch is not cached)", got)
	}
}

// TestFailureExcerptUnavailableOptionalKeepsStableSchema pins the new flag as
// optional and absent from a response with no configured source (EH-02).
func TestFailureExcerptUnavailableOptionalKeepsStableSchema(t *testing.T) {
	schema, err := OutputSchema[GetResultOutput]()
	if err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	if _, ok := schema.Properties["failureExcerptUnavailable"]; !ok {
		t.Fatal("get_result schema is missing failureExcerptUnavailable")
	}
	for _, required := range schema.Required {
		if required == "failureExcerptUnavailable" {
			t.Fatal("failureExcerptUnavailable must be optional, not required")
		}
	}
	h, w := excerptTestHandler(t, nil, nil)
	out, err := h.Result(context.Background(), sessionReq("session-E"), GetResultInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if out.FailureExcerptUnavailable {
		t.Error("no-source response set the unavailability flag")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"failureExcerptUnavailable"`) {
		t.Errorf("optional field present without a source: %s", raw)
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

// TestEnvFailureExcerptSourceRejectsEmptyMetadata pins EH-02's change to the
// zero-wiring source: missing fields are errors that name the field, not a
// silent empty excerpt. A missing check run with valid metadata stays empty.
func TestEnvFailureExcerptSourceRejectsEmptyMetadata(t *testing.T) {
	src := &envFailureExcerptSource{client: &http.Client{}}
	cases := []struct {
		name, repo, headSHA, checkName, field string
	}{
		{"repo", "", "sha", "ci/failure", "repo"},
		{"headSHA", "o/r", "", "ci/failure", "headSHA"},
		{"checkName", "o/r", "sha", "", "checkName"},
		{"repo format", "not-a-repo", "sha", "ci/failure", "owner/name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := src.FailureExcerpt(context.Background(), tc.repo, tc.headSHA, tc.checkName)
			if err == nil {
				t.Fatalf("FailureExcerpt = %q, nil; want error naming %q", got, tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error = %q, want it to name %q", err, tc.field)
			}
		})
	}
	var nilSource *envFailureExcerptSource
	if _, err := nilSource.FailureExcerpt(context.Background(), "o/r", "sha", "ci/failure"); err == nil {
		t.Error("nil source returned no error")
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

// limitResultFixture builds a stored watch with exactly n failed checks and
// the obligation the caller needs to read it.
func limitResultFixture(t *testing.T, n int) (domain.Watch, *memStore, *recordingStamper, *stepClock) {
	t.Helper()
	w := resultFixture(domain.VerdictKeepWorking)
	w.Checks = make(map[string]domain.Check, n)
	for i := 0; i < n; i++ {
		w.Checks[fmt.Sprintf("ci/fail-%03d", i)] = domain.Check{
			State: "FAILURE", Conclusion: "FAILURE", Required: true,
		}
	}
	_, _, store, stamper := newAckResultFixture(t, domain.VerdictKeepWorking, "session-limit", ackFixtureWithWatch(w, "session-limit"))
	return w, store, stamper, &stepClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
}

func TestGetResultRecordLimit(t *testing.T) {
	cases := []struct {
		name    string
		checks  int
		wantErr bool
	}{
		{"empty", 0, false},
		{"at limit", getResultMaxRecords, false},
		{"over limit", getResultMaxRecords + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, store, stamper, clock := limitResultFixture(t, tc.checks)
			h := NewGetResultHandler(store, stamper, clock)
			h.SetFailureExcerptSource(nil)
			_, out, err := h.Handle(context.Background(), sessionReq("session-limit"), GetResultInput{WatchID: w.ID})
			if tc.wantErr {
				assertLimitError(t, err, "200-failedCheck", "fewer failedChecks")
				if stamper.readCalls != 0 {
					t.Fatalf("refused result consumed the read stamp: %d stamps", stamper.readCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(out.FailedChecks) != tc.checks {
				t.Fatalf("failedChecks = %d, want %d", len(out.FailedChecks), tc.checks)
			}
			if stamper.readCalls != 1 {
				t.Fatalf("read stamps = %d, want 1", stamper.readCalls)
			}
		})
	}
}

func TestGetResultByteLimit(t *testing.T) {
	w, store, stamper, clock := limitResultFixture(t, 1)
	check := w.Checks["ci/fail-000"]
	check.DetailsURL = strings.Repeat("u", getResultMaxBytes+1)
	w.Checks["ci/fail-000"] = check
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	h := NewGetResultHandler(store, stamper, clock)
	h.SetFailureExcerptSource(nil)
	_, _, err := h.Handle(context.Background(), sessionReq("session-limit"), GetResultInput{WatchID: w.ID})
	assertLimitError(t, err, "262144-byte", "fewer failedChecks")
}

func TestGetResultLimitIsToolError(t *testing.T) {
	_, store, stamper, clock := limitResultFixture(t, getResultMaxRecords+1)
	session := connectTestSession(t, Dependencies{Store: store, Stamper: stamper, Clock: clock})
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_get_result",
		Arguments: GetResultInput{WatchID: "w-1"},
		Meta:      mcpsdk.Meta{sessionIDMetaKey: "session-limit"},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_get_result: %v", err)
	}
	if !result.IsError {
		t.Fatalf("oversized result did not set isError: %+v", result.StructuredContent)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal error content: %v", err)
	}
	if !strings.Contains(string(body), "200-failedCheck") || !strings.Contains(string(body), "fewer failedChecks") {
		t.Fatalf("tool error text = %s, want the limit and the narrowing hint", body)
	}
}
