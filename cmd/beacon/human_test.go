package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
)

type w8bDecider struct {
	req   httpapi.ClientDecision
	calls int
	resp  httpapi.ClientDecisionReceipt
	err   error
}

func (d *w8bDecider) Decide(_ context.Context, req httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error) {
	d.calls++
	d.req = req
	return d.resp, d.err
}

type w8bStore struct {
	req   httpapi.ClientDecision
	calls int
	err   error
}

func (s *w8bStore) Apply(_ context.Context, req httpapi.ClientDecision) error {
	s.calls++
	s.req = req
	return s.err
}

// w8bHumanHarness replaces the three seams of the command and restores them
// after the test. It writes a connection.json only when asked to.
func w8bHumanHarness(t *testing.T, withConnection bool) (*CLI, *w8bDecider, *w8bStore) {
	t.Helper()
	dir := t.TempDir()
	if withConnection {
		data := []byte(`{"host":"127.0.0.1","port":8787,"token":"t"}`)
		if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, out, errOut, _ := w69CLI(t, nil)
	decider := &w8bDecider{resp: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}
	store := &w8bStore{}
	prevDir, prevDecider, prevStore := humanStateDir, humanDeciderFor, humanStoreFor
	humanStateDir = func() (string, error) { return dir, nil }
	humanDeciderFor = func(httpapi.ClientConn) (humanDecider, error) { return decider, nil }
	humanStoreFor = func(string) humanDecisionStore { return store }
	t.Cleanup(func() {
		humanStateDir, humanDeciderFor, humanStoreFor = prevDir, prevDecider, prevStore
	})
	_ = errOut
	_ = out
	return c, decider, store
}

func TestW8BHumanRejectsUnknownVerb(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"ban", "w-1"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 0 || store.calls != 0 {
		t.Fatalf("unknown verb reached a write path: http=%d sqlite=%d", decider.calls, store.calls)
	}
}

func TestW8BHumanRequiresVerbAndTarget(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"approve"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 0 || store.calls != 0 {
		t.Fatalf("missing target reached a write path")
	}
}

func TestW8BHumanPostsToServer(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"--note", "ok by me", "approve", "w-1"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 1 || store.calls != 0 {
		t.Fatalf("http=%d sqlite=%d, want 1/0", decider.calls, store.calls)
	}
	if decider.req.Verb != "approve" || decider.req.Target != "w-1" || decider.req.Note != "ok by me" {
		t.Fatalf("request = %+v", decider.req)
	}
}

func TestW8BHumanFallsBackToSQLiteWithoutServer(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, false)
	if code := runHuman(c, []string{"unblock", "blocker-7"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if store.calls != 1 || store.req.Verb != "unblock" || store.req.Target != "blocker-7" {
		t.Fatalf("sqlite request = %+v, calls %d", store.req, store.calls)
	}
	if decider.calls != 0 {
		t.Fatalf("no connection.json must not dial: %d", decider.calls)
	}
}

func TestW8BHumanFallsBackOnTransportError(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	decider.err = httpapi.ErrNoServer
	if code := runHuman(c, []string{"reject", "w-2"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 1 || store.calls != 1 {
		t.Fatalf("http=%d sqlite=%d, want 1/1", decider.calls, store.calls)
	}
}

func TestW8BHumanDoesNotFallbackOnServerRefusal(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	decider.err = errors.New("not parked")
	if code := runHuman(c, []string{"reject", "w-2"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if store.calls != 0 {
		t.Fatalf("server refusal must not fall back: %d", store.calls)
	}
}

func TestW8BHumanReportsSQLiteFailure(t *testing.T) {
	c, _, store := w8bHumanHarness(t, false)
	store.err = errors.New("db locked")
	if code := runHuman(c, []string{"pause", "w-3"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestW8BHumanRegisteredInCatalog(t *testing.T) {
	for _, entry := range catalog {
		if entry.Name == "human" {
			if !strings.Contains(entry.Usage, "<watchID|blockerID>") {
				t.Fatalf("usage = %q", entry.Usage)
			}
			return
		}
	}
	t.Fatal("human missing from catalog")
}

// RS-CMP-06: the human answer is parsed into a typed schema before any write
// path runs. These tests pin the schema, its errors, and the callback boundary.

func TestRSCMP06ParsesValidResponse(t *testing.T) {
	resp, err := parseHumanResponse("approve", "w-1", "ok by me")
	if err != nil {
		t.Fatalf("parse valid answer: %v", err)
	}
	if resp.Outcome != humanApprove || resp.Target != "w-1" || resp.Note != "ok by me" {
		t.Fatalf("response = %+v", resp)
	}
	if !resp.Outcome.valid() {
		t.Fatalf("outcome %q must be declared", resp.Outcome)
	}
}

func TestRSCMP06RejectsInvalidOutcomeType(t *testing.T) {
	_, err := parseHumanResponse("ban", "w-1", "")
	if err == nil {
		t.Fatal("invalid outcome type must fail")
	}
	if !strings.Contains(err.Error(), "неизвестный тип ответа") {
		t.Fatalf("error = %q, want a clear type error", err)
	}
	if !strings.Contains(err.Error(), "approve") {
		t.Fatalf("error must list declared outcomes: %q", err)
	}
}

func TestRSCMP06RejectsMissingTarget(t *testing.T) {
	_, err := parseHumanResponse("approve", "   ", "")
	if err == nil {
		t.Fatal("missing target must fail")
	}
	if !strings.Contains(err.Error(), "пропущено поле") {
		t.Fatalf("error = %q, want a missing field error", err)
	}
}

func TestRSCMP06InvalidAnswerNeverReachesCallback(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"ban", "w-1"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 0 || store.calls != 0 {
		t.Fatalf("invalid answer reached a write path: http=%d sqlite=%d", decider.calls, store.calls)
	}
}

func TestRSCMP06ValidAnswerPostsTypedOutcome(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"--note", "looks good", "approve", "w-9"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 1 || store.calls != 0 {
		t.Fatalf("http=%d sqlite=%d, want 1/0", decider.calls, store.calls)
	}
	if decider.req.Verb != "approve" || decider.req.Target != "w-9" || decider.req.Note != "looks good" {
		t.Fatalf("request = %+v", decider.req)
	}
}

// humanJSONHarness is the w8b harness with the output buffers kept so a test
// can decode the structured result of a decision.
func humanJSONHarness(t *testing.T, withConnection bool) (*CLI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	if withConnection {
		data := []byte(`{"host":"127.0.0.1","port":8787,"token":"t"}`)
		if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, out, errOut, _ := w69CLI(t, nil)
	prevDir, prevDecider, prevStore := humanStateDir, humanDeciderFor, humanStoreFor
	humanStateDir = func() (string, error) { return dir, nil }
	humanDeciderFor = func(httpapi.ClientConn) (humanDecider, error) {
		return &w8bDecider{resp: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}, nil
	}
	humanStoreFor = func(string) humanDecisionStore { return &w8bStore{} }
	t.Cleanup(func() {
		humanStateDir, humanDeciderFor, humanStoreFor = prevDir, prevDecider, prevStore
	})
	return c, out, errOut
}

func TestW8BHumanJSONHTTP(t *testing.T) {
	c, out, errOut := humanJSONHarness(t, true)
	if code := runHuman(c, []string{"--format", "json", "approve", "w-json"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	var res humanResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode human JSON: %v; output: %q", err, out.String())
	}
	if res.Verb != "approve" || res.Target != "w-json" || res.State != "ok" || res.Transport != "http" {
		t.Fatalf("result = %+v, want approve/w-json/ok/http", res)
	}
}

func TestW8BHumanJSONSQLite(t *testing.T) {
	c, out, errOut := humanJSONHarness(t, false)
	if code := runHuman(c, []string{"--format", "json", "unblock", "b-json"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	var res humanResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode human JSON: %v; output: %q", err, out.String())
	}
	if res.Verb != "unblock" || res.Target != "b-json" || res.State != "applied via sqlite" || res.Transport != "sqlite" {
		t.Fatalf("result = %+v, want unblock/b-json/applied via sqlite/sqlite", res)
	}
}

// BCX-57 / needs_input_continue_of_same_watch (ADR 0061 §4).
//
// A Watch parked in needs_input receives a human approve. The same Watch must
// resume at the next revision, no successor Watch may be created, and the
// obligation must keep its recorded intention. The two eval specs
// w21-human-question-revision and e05-wait-pending-question describe the same
// flow at the live-wait boundary.
func TestNeedsInputContinueOfSameWatch(t *testing.T) {
	dir := t.TempDir()
	seed, err := openProcessStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	watch := domain.Watch{
		ID:       "w-same-watch",
		Repo:     "o/r",
		PR:       7,
		HeadSHA:  "sha",
		Status:   domain.StatusNeedsInput,
		Verdict:  domain.VerdictNeedsInput,
		Revision: 7,
	}
	if err := seed.SaveWatch(watch); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	obligation := domain.Obligation{
		ID:             domain.ObligationKey(watch.ID, watch.Revision, "session-answer"),
		WatchID:        watch.ID,
		Revision:       watch.Revision,
		SubscriberID:   "session-answer",
		VerdictPointer: watch.Verdict,
		StagedAt:       time.Now(),
	}
	if err := seed.SaveObligation(obligation); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	applier := sqliteHumanStore{dir: dir}
	if err := applier.Apply(context.Background(), httpapi.ClientDecision{
		Verb: "approve", Target: watch.ID, Note: "human-answer-1",
	}); err != nil {
		t.Fatalf("human approve: %v", err)
	}

	check, err := openProcessStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()

	got, err := check.Watch(watch.ID)
	if err != nil {
		t.Fatalf("read Watch after answer: %v", err)
	}
	if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictWatching {
		t.Fatalf("answer did not resume the same Watch: status %q verdict %q", got.Status, got.Verdict)
	}
	if got.Revision != watch.Revision+1 {
		t.Fatalf("revision = %d, want %d", got.Revision, watch.Revision+1)
	}
	watches, err := check.AllWatches()
	if err != nil {
		t.Fatalf("AllWatches: %v", err)
	}
	if len(watches) != 1 || watches[0].ID != watch.ID {
		t.Fatalf("answer created a successor Watch: %+v", watches)
	}
	obligations, err := check.WatchObligations(watch.ID)
	if err != nil {
		t.Fatalf("WatchObligations: %v", err)
	}
	if len(obligations) != 1 || obligations[0].ClaimedAt.IsZero() {
		t.Fatalf("answer lost the obligation intention: %+v", obligations)
	}
}

// Negative control for the same scenario. When the question changes, the
// server refuses the stale answer. There is no question_changed code in main,
// so the fake returns a plain refusal. The command must report failure without
// falling back to a store write, so no second Watch is created and no
// obligation is stamped. See w21-human-question-revision.md.
func TestNeedsInputContinueOfSameWatchChangedQuestion(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	decider.err = errors.New("question changed")
	if code := runHuman(c, []string{"approve", "w-stale"}); code == 0 {
		t.Fatal("stale answer to a changed question was accepted")
	}
	if decider.calls != 1 {
		t.Fatalf("http calls = %d, want 1", decider.calls)
	}
	if store.calls != 0 {
		t.Fatalf("stale answer reached the store: %d", store.calls)
	}
}
