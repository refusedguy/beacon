package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const testSecret = "webhook-test-secret"

const completedBody = `{
  "action": "completed",
  "repository": {"full_name": "refusedguy/beacon"},
  "workflow_run": {
    "id": 991,
    "run_attempt": 1,
    "head_sha": "headsha-378",
    "status": "completed",
    "conclusion": "failure",
    "pull_requests": [{"number": 378}]
  }
}`

const rerunBody = `{
  "action": "completed",
  "repository": {"full_name": "refusedguy/beacon"},
  "workflow_run": {"id": 991, "run_attempt": 2, "head_sha": "headsha-378", "pull_requests": [{"number": 378}]}
}`

const forkBody = `{
  "action": "completed",
  "repository": {"full_name": "refusedguy/beacon"},
  "workflow_run": {"id": 992, "run_attempt": 1, "head_sha": "fork-sha", "pull_requests": []}
}`

type hint struct {
	repo    string
	pr      int
	headSHA string
	action  string
	at      time.Time
}

type recordingSink struct {
	mu    sync.Mutex
	hints []hint
	err   error
}

func (s *recordingSink) WorkflowRunHint(_ context.Context, repo string, pr int, headSHA, action string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.hints = append(s.hints, hint{repo: repo, pr: pr, headSHA: headSHA, action: action, at: at})
	return nil
}

func (s *recordingSink) calls() []hint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hint(nil), s.hints...)
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

var _ domain.Clock = fixedClock{}

func sign(secret []byte, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newTestRequest(t *testing.T, secret []byte, deliveryID, event, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set(EventHeader, event)
	if deliveryID != "" {
		req.Header.Set(DeliveryHeader, deliveryID)
	}
	req.Header.Set(SignatureHeader, sign(secret, []byte(body)))
	return req
}

func newTestHandler(t *testing.T, journal DeliveryJournal, sink WorkflowRunEventPort) *WorkflowHook {
	t.Helper()
	h, err := NewWorkflowHook([]byte(testSecret), journal, sink, fixedClock{at: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("NewWorkflowHook: %v", err)
	}
	return h
}

func TestVerifySignatureGitHubControlVector(t *testing.T) {
	secret := []byte("It's a Secret to Everybody")
	body := []byte("Hello, World!")
	want := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if !VerifySignature(secret, body, want) {
		t.Fatal("valid GitHub control vector must verify")
	}
	if VerifySignature(secret, []byte("Hello, world!"), want) {
		t.Fatal("a changed body must not verify")
	}
	if VerifySignature(secret, body, "sha256=zz") {
		t.Fatal("a malformed digest must not verify")
	}
	if VerifySignature(nil, body, want) {
		t.Fatal("an empty secret must not verify")
	}
}

func TestWorkflowRunCompletedPublishesHintWithoutVerdict(t *testing.T) {
	journal := NewMemoryDeliveryJournal()
	sink := &recordingSink{}
	h := newTestHandler(t, journal, sink)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-1", EventWorkflowRun, completedBody))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	calls := sink.calls()
	if len(calls) != 1 {
		t.Fatalf("bridge hints = %d, want 1", len(calls))
	}
	want := hint{repo: "refusedguy/beacon", pr: 378, headSHA: "headsha-378", action: ActionCompleted}
	if calls[0].repo != want.repo || calls[0].pr != want.pr || calls[0].headSHA != want.headSHA || calls[0].action != want.action {
		t.Fatalf("hint = %+v, want %+v", calls[0], want)
	}
	if calls[0].at.IsZero() {
		t.Fatal("hint must carry the delivery time")
	}
	// The completed body says conclusion=failure. That word is not parsed,
	// so the only published signal is a re-read hint, never a retry verdict.

	claimed, err := journal.ClaimDelivery(context.Background(), domain.IdempotencyKey(githubDeliveryNamespace, "delivery-1"), time.Time{})
	if err != nil || claimed {
		t.Fatalf("delivery key must already be claimed: claimed=%v err=%v", claimed, err)
	}
}

func TestWorkflowRunRejectsBadSignature(t *testing.T) {
	tests := []struct {
		name      string
		signature string
	}{
		{name: "missing header", signature: ""},
		{name: "wrong digest", signature: "sha256=" + strings.Repeat("0", 64)},
		{name: "malformed digest", signature: "sha256=nothex"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			journal := NewMemoryDeliveryJournal()
			sink := &recordingSink{}
			h := newTestHandler(t, journal, sink)

			req := newTestRequest(t, []byte(testSecret), "delivery-bad", EventWorkflowRun, completedBody)
			req.Header.Set(SignatureHeader, tc.signature)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if len(sink.calls()) != 0 {
				t.Fatalf("bridge hints = %d, want 0", len(sink.calls()))
			}
			claimed, err := journal.ClaimDelivery(context.Background(), domain.IdempotencyKey(githubDeliveryNamespace, "delivery-bad"), time.Time{})
			if err != nil || !claimed {
				t.Fatalf("a rejected delivery must not be journaled: claimed=%v err=%v", claimed, err)
			}
		})
	}
}

func TestWorkflowRunRedeliveryIsIdempotent(t *testing.T) {
	journal := NewMemoryDeliveryJournal()
	sink := &recordingSink{}
	h := newTestHandler(t, journal, sink)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-same", EventWorkflowRun, completedBody))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("delivery %d: status = %d, want 202", i+1, rec.Code)
		}
	}
	if got := len(sink.calls()); got != 1 {
		t.Fatalf("bridge hints = %d, want 1 after redelivery", got)
	}
}

func TestWorkflowRunBodyOverLimitIsRejected(t *testing.T) {
	journal := NewMemoryDeliveryJournal()
	sink := &recordingSink{}
	h := newTestHandler(t, journal, sink)
	h.maxBody = 64 // exercise the same limit path without allocating 25 MB

	body := completedBody + strings.Repeat(" ", 128)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-big", EventWorkflowRun, body))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if len(sink.calls()) != 0 {
		t.Fatal("an oversized body must not publish a hint")
	}
}

func TestWorkflowRunIgnoresNonHintingDeliveries(t *testing.T) {
	tests := []struct {
		name  string
		event string
		body  string
	}{
		{name: "other event type", event: "push", body: completedBody},
		{name: "unsupported action", event: EventWorkflowRun, body: `{"action":"created","workflow_run":{"id":1,"run_attempt":1,"head_sha":"x","pull_requests":[{"number":1}]}}`},
		{name: "re-run attempt", event: EventWorkflowRun, body: rerunBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			h := newTestHandler(t, NewMemoryDeliveryJournal(), sink)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-ignored", tc.event, tc.body))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", rec.Code)
			}
			if len(sink.calls()) != 0 {
				t.Fatalf("bridge hints = %d, want 0", len(sink.calls()))
			}
		})
	}
}

func TestWorkflowRunForkWithoutPRMatchesHeadSHA(t *testing.T) {
	sink := &recordingSink{}
	h := newTestHandler(t, NewMemoryDeliveryJournal(), sink)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-fork", EventWorkflowRun, forkBody))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	calls := sink.calls()
	if len(calls) != 1 || calls[0].pr != 0 || calls[0].headSHA != "fork-sha" {
		t.Fatalf("fork hints = %+v, want one hint with pr 0 and head SHA", calls)
	}
}

func TestWorkflowRunActionsForwardActionOnly(t *testing.T) {
	for _, action := range []string{ActionRequested, ActionInProgress, ActionCompleted} {
		t.Run(action, func(t *testing.T) {
			sink := &recordingSink{}
			h := newTestHandler(t, NewMemoryDeliveryJournal(), sink)
			body := `{"action":"` + action + `","repository":{"full_name":"o/r"},"workflow_run":{"id":7,"run_attempt":1,"head_sha":"sha","pull_requests":[{"number":9}]}}`
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-"+action, EventWorkflowRun, body))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", rec.Code)
			}
			calls := sink.calls()
			if len(calls) != 1 || calls[0].action != action {
				t.Fatalf("hints = %+v, want one hint with action %q", calls, action)
			}
		})
	}
}

func TestWorkflowRunRejectsMissingDeliveryID(t *testing.T) {
	sink := &recordingSink{}
	h := newTestHandler(t, NewMemoryDeliveryJournal(), sink)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "", EventWorkflowRun, completedBody))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(sink.calls()) != 0 {
		t.Fatal("a delivery without an id must not publish a hint")
	}
}

func TestWorkflowRunSinkFailureReturnsError(t *testing.T) {
	sink := &recordingSink{err: errors.New("bridge down")}
	h := newTestHandler(t, NewMemoryDeliveryJournal(), sink)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(t, []byte(testSecret), "delivery-fail", EventWorkflowRun, completedBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
