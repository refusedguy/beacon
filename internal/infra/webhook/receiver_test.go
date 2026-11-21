package webhook

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
)

const (
	controlSecret = "It's a Secret to Everybody"
	controlBody   = "Hello, World!"
	controlSig    = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	workflowRunEventBody = `{"action":"completed","repository":{"full_name":"refusedguy/beacon"},"workflow_run":{"id":991,"run_attempt":1,"head_sha":"headsha-378","pull_requests":[{"number":378}]}}`
)

type captureRecorder struct {
	mu      sync.Mutex
	records []Delivery
	signal  chan Delivery
}

func newCaptureRecorder() *captureRecorder {
	return &captureRecorder{signal: make(chan Delivery, 8)}
}

func (r *captureRecorder) Record(_ context.Context, d Delivery) (RecordResult, error) {
	r.mu.Lock()
	r.records = append(r.records, d)
	r.mu.Unlock()
	r.signal <- d
	return RecordResult{Created: true}, nil
}

func (r *captureRecorder) wait(t *testing.T) Delivery {
	t.Helper()
	select {
	case d := <-r.signal:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a delivery record")
		return Delivery{}
	}
}

func (r *captureRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

type blockingRecorder struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingRecorder() *blockingRecorder {
	return &blockingRecorder{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingRecorder) Record(_ context.Context, _ Delivery) (RecordResult, error) {
	close(r.started)
	<-r.release
	return RecordResult{Created: true}, nil
}

type claimingRecorder struct {
	journal *MemoryDeliveryJournal
	results chan RecordResult
}

func (r *claimingRecorder) Record(ctx context.Context, d Delivery) (RecordResult, error) {
	result, err := r.journal.Record(ctx, DeliveryKey(d.ID), d.Payload, d.At)
	if err == nil {
		r.results <- result
	}
	return result, err
}

type repeatByte byte

func (r repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func newReceiverHandler(t *testing.T, secret string, recorder DeliveryRecorder) *Receiver {
	t.Helper()
	h, err := NewReceiver([]byte(secret), recorder, fixedClock{at: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return h
}

func newReceiverRequest(t *testing.T, secret []byte, deliveryID, event, body, signature string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set(gh.EventTypeHeader, event)
	}
	if deliveryID != "" {
		req.Header.Set(gh.DeliveryIDHeader, deliveryID)
	}
	if signature != "" {
		req.Header.Set(gh.SHA256SignatureHeader, signature)
	}
	return req
}

func signedRequest(t *testing.T, secret []byte, deliveryID, event, body string) *http.Request {
	t.Helper()
	return newReceiverRequest(t, secret, deliveryID, event, body, sign(secret, []byte(body)))
}

func assertNoRecord(t *testing.T, rec *captureRecorder) {
	t.Helper()
	select {
	case d := <-rec.signal:
		t.Fatalf("unexpected delivery record: %+v", d)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReceiverGitHubControlVector(t *testing.T) {
	if got := sign([]byte(controlSecret), []byte(controlBody)); got != controlSig {
		t.Fatalf("HMAC = %s, want GitHub control vector %s", got, controlSig)
	}
	rec := newCaptureRecorder()
	h := newReceiverHandler(t, controlSecret, rec)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReceiverRequest(t, []byte(controlSecret), "", "", controlBody, controlSig))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	// A valid signature with no event type is acknowledged without a record.
	assertNoRecord(t, rec)
}

func TestReceiverRejectsUnsignedDeliveries(t *testing.T) {
	tests := []struct {
		name      string
		signature string
	}{
		{name: "missing header"},
		{name: "wrong digest", signature: "sha256=" + strings.Repeat("0", 64)},
		{name: "malformed digest", signature: "sha256=nothex"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newCaptureRecorder()
			h := newReceiverHandler(t, testSecret, rec)

			req := newReceiverRequest(t, []byte(testSecret), "delivery-bad", "workflow_run", workflowRunEventBody, tc.signature)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			assertNoRecord(t, rec)
		})
	}
}

func TestReceiverRejectsBodyOverLimit(t *testing.T) {
	rec := newCaptureRecorder()
	h := newReceiverHandler(t, testSecret, rec)

	// A lazy reader supplies exactly one byte over go-github's 25 MB cap.
	req := httptest.NewRequest(http.MethodPost, "/webhook", io.LimitReader(repeatByte('x'), (25<<20)+1))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gh.SHA256SignatureHeader, sign([]byte(testSecret), nil))
	req.Header.Set(gh.EventTypeHeader, "workflow_run")
	req.Header.Set(gh.DeliveryIDHeader, "delivery-big")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	assertNoRecord(t, rec)
}

func TestReceiverRecordsDeliveryWithEventAndAction(t *testing.T) {
	rec := newCaptureRecorder()
	h := newReceiverHandler(t, testSecret, rec)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-1", "workflow_run", workflowRunEventBody))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	d := rec.wait(t)
	if d.ID != "delivery-1" || d.Event != "workflow_run" || d.Action != "completed" {
		t.Fatalf("delivery = %+v, want id=delivery-1 event=workflow_run action=completed", d)
	}
	if d.At.IsZero() {
		t.Fatal("delivery must carry the receipt time")
	}
	// The raw body may include a conclusion or status. Only identifiers are
	// handed on; the receiver never derives a verdict from them.
	if string(d.Payload) != workflowRunEventBody {
		t.Fatal("recorded payload must be the validated wire bytes")
	}
}

func TestReceiverIgnoresPingAndUnknownEvents(t *testing.T) {
	tests := []struct {
		name  string
		event string
		body  string
	}{
		{name: "ping", event: EventPing, body: `{"zen":"Keep it logically awesome."}`},
		{name: "unknown event", event: "never_seen_before", body: workflowRunEventBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newCaptureRecorder()
			h := newReceiverHandler(t, testSecret, rec)

			w := httptest.NewRecorder()
			h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-ignored", tc.event, tc.body))
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", w.Code)
			}
			assertNoRecord(t, rec)
		})
	}
}

func TestReceiverAcknowledgesBeforeRecording(t *testing.T) {
	block := newBlockingRecorder()
	h := newReceiverHandler(t, testSecret, block)

	req := signedRequest(t, []byte(testSecret), "delivery-slow", "workflow_run", workflowRunEventBody)
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		done <- w.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler blocked waiting for the recorder")
	}
	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background recorder was never called")
	}
	close(block.release)
}

func TestReceiverRedeliveryIsIdempotent(t *testing.T) {
	rec := &claimingRecorder{journal: NewMemoryDeliveryJournal(), results: make(chan RecordResult, 4)}
	h := newReceiverHandler(t, testSecret, rec)

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-same", "workflow_run", workflowRunEventBody))
		if w.Code != http.StatusAccepted {
			t.Fatalf("delivery %d: status = %d, want 202", i+1, w.Code)
		}
	}
	first, second := <-rec.results, <-rec.results
	if !first.Created || first.Duplicate || first.Conflict {
		t.Fatalf("first result = %+v, want Created only", first)
	}
	if !second.Duplicate || second.Created || second.Conflict {
		t.Fatalf("repeat result = %+v, want Duplicate only", second)
	}
}

func TestReceiverConflictingBodyIsReported(t *testing.T) {
	rec := &claimingRecorder{journal: NewMemoryDeliveryJournal(), results: make(chan RecordResult, 4)}
	h := newReceiverHandler(t, testSecret, rec)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-change", "workflow_run", workflowRunEventBody))
	if w.Code != http.StatusAccepted {
		t.Fatalf("first delivery: status = %d, want 202", w.Code)
	}

	changedBody := strings.Replace(workflowRunEventBody, `"action":"completed"`, `"action":"requested"`, 1)
	if changedBody == workflowRunEventBody {
		t.Fatal("test body did not change")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-change", "workflow_run", changedBody))
	if w.Code != http.StatusAccepted {
		t.Fatalf("conflicting redelivery: status = %d, want 202", w.Code)
	}

	first, second := <-rec.results, <-rec.results
	if !first.Created {
		t.Fatalf("first result = %+v, want Created", first)
	}
	if !second.Duplicate || !second.Conflict || second.Created {
		t.Fatalf("conflicting result = %+v, want Duplicate and Conflict", second)
	}
}

func TestReceiverChecksSignatureBeforeDedup(t *testing.T) {
	rec := &claimingRecorder{journal: NewMemoryDeliveryJournal(), results: make(chan RecordResult, 4)}
	h := newReceiverHandler(t, testSecret, rec)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(t, []byte(testSecret), "delivery-sig", "workflow_run", workflowRunEventBody))
	if w.Code != http.StatusAccepted {
		t.Fatalf("first delivery: status = %d, want 202", w.Code)
	}
	if first := <-rec.results; !first.Created {
		t.Fatalf("first result = %+v, want Created", first)
	}

	req := newReceiverRequest(t, []byte(testSecret), "delivery-sig", "workflow_run", workflowRunEventBody,
		"sha256="+strings.Repeat("0", 64))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("rejected redelivery: status = %d, want 401", w.Code)
	}
	select {
	case result := <-rec.results:
		t.Fatalf("recorder called before signature check: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReceiverNeedsSecretAndRecorder(t *testing.T) {
	if _, err := NewReceiver(nil, newCaptureRecorder(), nil); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("empty secret error = %v, want ErrEmptySecret", err)
	}
	if _, err := NewReceiver([]byte(testSecret), nil, nil); err == nil {
		t.Fatal("nil recorder must be refused")
	}
}

func TestLoadSecretAndLoopbackBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SecretFileName)
	if err := os.WriteFile(path, []byte(testSecret), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	secret, err := LoadSecret(path)
	if err != nil || string(secret) != testSecret {
		t.Fatalf("LoadSecret = %q, %v; want %q", secret, err, testSecret)
	}

	empty := filepath.Join(dir, "empty.secret")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("write empty secret: %v", err)
	}
	if _, err := LoadSecret(empty); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("empty secret error = %v, want ErrEmptySecret", err)
	}
	loose := filepath.Join(dir, "loose.secret")
	if err := os.WriteFile(loose, []byte(testSecret), 0o644); err != nil {
		t.Fatalf("write loose secret: %v", err)
	}
	if _, err := LoadSecret(loose); err == nil {
		t.Fatal("a world-readable secret must be refused")
	}

	srv, err := Start(Config{Addr: "127.0.0.1:0", SecretPath: path, Recorder: newCaptureRecorder()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil || host != LoopbackHost {
		t.Fatalf("bound host = %q, %v; want %q", host, err, LoopbackHost)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := Start(Config{SecretPath: filepath.Join(dir, "missing.secret"), Recorder: newCaptureRecorder()}); err == nil {
		t.Fatal("Start without a secret file must fail")
	}
	addr, err := loopbackWebhookAddr("0.0.0.0:9999")
	if err != nil || addr != "127.0.0.1:9999" {
		t.Fatalf("loopbackWebhookAddr = %q, %v; want 127.0.0.1:9999", addr, err)
	}
}
