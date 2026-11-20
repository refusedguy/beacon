package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const testSecret = "local-webhook-secret"

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

type fakeStore struct {
	mu          sync.Mutex
	watches     map[string]domain.Watch
	obligations map[string]domain.Obligation
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		watches:     make(map[string]domain.Watch),
		obligations: make(map[string]domain.Obligation),
	}
}

func (f *fakeStore) SaveWatch(w domain.Watch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watches[w.ID] = w
	return nil
}

func (f *fakeStore) Watch(id string) (domain.Watch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.watches[id]
	if !ok {
		return domain.Watch{}, fmt.Errorf("no watch %s", id)
	}
	return w, nil
}

func (f *fakeStore) ActiveWatches() ([]domain.Watch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Watch, 0, len(f.watches))
	for _, w := range f.watches {
		if w.Status != domain.StatusClosed {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeStore) AllWatches() ([]domain.Watch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Watch, 0, len(f.watches))
	for _, w := range f.watches {
		out = append(out, w)
	}
	return out, nil
}

func (f *fakeStore) SaveObligation(o domain.Obligation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obligations[o.ID] = o
	return nil
}

func (f *fakeStore) Obligation(id string) (domain.Obligation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.obligations[id]
	if !ok {
		return domain.Obligation{}, fmt.Errorf("no obligation %s", id)
	}
	return o, nil
}

func (f *fakeStore) PendingObligations() ([]domain.Obligation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Obligation, 0, len(f.obligations))
	for _, o := range f.obligations {
		out = append(out, o)
	}
	return out, nil
}

func checkRunBody(repo string, pr int, sha, conclusion string) []byte {
	body := fmt.Sprintf(
		`{"repository":{"full_name":%q},"check_run":{"head_sha":%q,"conclusion":%q,"pull_requests":[{"number":%d}]}}`,
		repo, sha, conclusion, pr)
	return []byte(body)
}

func postEvent(t *testing.T, r *Receiver, delivery, event string, body []byte, secret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(body))
	req.Header.Set(EventHeader, event)
	req.Header.Set(DeliveryHeader, delivery)
	req.Header.Set(SignatureHeader, Sign(body, secret))
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) Result {
	t.Helper()
	var res Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return res
}

func TestReceiverPublishesCheckRunVerdict(t *testing.T) {
	store := newFakeStore()
	r, err := New(Config{
		Secret: testSecret, Store: store,
		Clock: fixedClock{at: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}

	body := checkRunBody("refusedguy/beacon", 340, "deadbeef", "success")
	rec := postEvent(t, r, "delivery-1", "check_run", body, testSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	res := decodeResult(t, rec)
	if res.Status != "ok" || res.Verdict != string(domain.VerdictReady) {
		t.Fatalf("result = %+v", res)
	}
	if res.Repo != "refusedguy/beacon" || res.PR != 340 || res.HeadSHA != "deadbeef" {
		t.Fatalf("event fields = %+v", res)
	}
	if res.Revision != 1 || res.WatchID == "" {
		t.Fatalf("watch id/revision = %q/%d", res.WatchID, res.Revision)
	}

	w, err := store.Watch(res.WatchID)
	if err != nil {
		t.Fatalf("read watch: %v", err)
	}
	if w.Verdict != domain.VerdictReady || w.Revision != 1 || w.Status != domain.StatusNotifying {
		t.Fatalf("stored watch = %+v", w)
	}
	for _, sub := range w.Subscribers {
		id := domain.ObligationKey(w.ID, w.Revision, sub.ID)
		o, err := store.Obligation(id)
		if err != nil {
			t.Fatalf("obligation %s: %v", id, err)
		}
		if o.VerdictPointer != domain.VerdictReady {
			t.Fatalf("obligation verdict = %s", o.VerdictPointer)
		}
	}
}

func TestReceiverRejectsInvalidSignature(t *testing.T) {
	store := newFakeStore()
	r, err := New(Config{Secret: testSecret, Store: store})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}

	body := checkRunBody("refusedguy/beacon", 340, "deadbeef", "success")
	rec := postEvent(t, r, "delivery-2", "check_run", body, "wrong-secret")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("invalid signature created a watch: %+v", active)
	}
}

func TestReceiverSkipsDuplicateDelivery(t *testing.T) {
	store := newFakeStore()
	r, err := New(Config{Secret: testSecret, Store: store})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}

	first := postEvent(t, r, "delivery-3", "check_run",
		checkRunBody("refusedguy/beacon", 340, "deadbeef", "success"), testSecret)
	if first.Code != http.StatusOK {
		t.Fatalf("first status %d", first.Code)
	}
	before := decodeResult(t, first)

	// Same delivery id, different conclusion and body: the second event must
	// not change the verdict even though its signature is valid.
	second := postEvent(t, r, "delivery-3", "check_run",
		checkRunBody("refusedguy/beacon", 340, "deadbeef", "failure"), testSecret)
	if second.Code != http.StatusOK {
		t.Fatalf("second status %d, body %q", second.Code, second.Body.String())
	}
	dup := decodeResult(t, second)
	if dup.Status != "duplicate" {
		t.Fatalf("second status field = %q", dup.Status)
	}

	w, err := store.Watch(before.WatchID)
	if err != nil {
		t.Fatalf("read watch: %v", err)
	}
	if w.Verdict != domain.VerdictReady || w.Revision != before.Revision {
		t.Fatalf("duplicate moved the watch: %+v", w)
	}
}

func TestReceiverDryRunDoesNotWrite(t *testing.T) {
	store := newFakeStore()
	r, err := New(Config{Secret: testSecret, Store: store, DryRun: true})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}

	body := checkRunBody("refusedguy/beacon", 340, "deadbeef", "success")
	rec := postEvent(t, r, "delivery-4", "check_run", body, testSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	res := decodeResult(t, rec)
	if res.Status != "dry_run" || res.Verdict != string(domain.VerdictReady) {
		t.Fatalf("result = %+v", res)
	}
	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("dry run wrote a watch: %+v", active)
	}
}

func TestReceiverRejectsMissingDeliveryAndWrongMethod(t *testing.T) {
	store := newFakeStore()
	r, err := New(Config{Secret: testSecret, Store: store})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}

	body := checkRunBody("refusedguy/beacon", 340, "deadbeef", "success")
	req := httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(body))
	req.Header.Set(SignatureHeader, Sign(body, testSecret))
	req.Header.Set(EventHeader, "check_run")
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing delivery status %d", rec.Code)
	}

	getRec := httptest.NewRecorder()
	r.Handler().ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, Path, nil))
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", getRec.Code)
	}
}

func TestParseEventTypes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		repo   string
		pr     int
		sha    string
		result string
	}{
		{
			name: "check_run",
			body: `{"repository":{"full_name":"o/r"},"check_run":{"head_sha":"aaa","conclusion":"failure","pull_requests":[{"number":7}]}}`,
			repo: "o/r", pr: 7, sha: "aaa", result: "failure",
		},
		{
			name: "check_suite",
			body: `{"repository":{"full_name":"o/r"},"check_suite":{"head_sha":"bbb","conclusion":"success","pull_requests":[{"number":8}]}}`,
			repo: "o/r", pr: 8, sha: "bbb", result: "success",
		},
		{
			name: "pull_request",
			body: `{"repository":{"full_name":"o/r"},"number":9,"pull_request":{"head":{"sha":"ccc"}}}`,
			repo: "o/r", pr: 9, sha: "ccc", result: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseEvent(tc.name, []byte(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if ev.Repo != tc.repo || ev.PR != tc.pr || ev.HeadSHA != tc.sha || ev.Conclusion != tc.result {
				t.Fatalf("event = %+v", ev)
			}
		})
	}

	if _, err := ParseEvent("issues", []byte(`{}`)); !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("unsupported event error = %v", err)
	}
	if _, err := ParseEvent("check_run", []byte(`{`)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("malformed payload error = %v", err)
	}
}

func TestVerifySignatureVariants(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	if err := VerifySignature(body, Sign(body, testSecret), testSecret); err != nil {
		t.Fatalf("valid signature: %v", err)
	}
	if err := VerifySignature(body, Sign(body, "other"), testSecret); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("wrong secret error = %v", err)
	}
	if err := VerifySignature(body, "sha256=zz", testSecret); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("bad hex error = %v", err)
	}
	if err := VerifySignature(body, "", testSecret); !errors.Is(err, ErrMissingSignature) {
		t.Fatalf("missing header error = %v", err)
	}
	if err := VerifySignature(body, Sign(body, testSecret), ""); err == nil {
		t.Fatal("empty secret accepted")
	}
}
