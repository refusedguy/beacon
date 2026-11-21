package wake

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type ntfyFakeDoer struct {
	calls    int
	req      *http.Request
	status   int
	body     string
	err      error
	response *http.Response
}

func (d *ntfyFakeDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.req = req
	if d.err != nil {
		return nil, d.err
	}
	if d.response != nil {
		return d.response, nil
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	body := d.body
	if body == "" {
		body = "ok"
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func TestNtfyConfigFromEnv(t *testing.T) {
	cfg := NtfyConfigFromEnv(func(key string) string {
		switch key {
		case NtfyURLEnv:
			return "  http://example.test/  "
		case NtfyTopicEnv:
			return "  beacon-alerts  "
		}
		return ""
	})
	if cfg.URL != "http://example.test/" || cfg.Topic != "beacon-alerts" {
		t.Fatalf("config = %+v, want trimmed env values", cfg)
	}
}

func TestNtfyNotifySinkFailOpenWithoutConfig(t *testing.T) {
	doer := &ntfyFakeDoer{}
	sink := NewNtfyNotifySink(NtfyConfig{Doer: doer})
	ctx := context.Background()

	if sink.Configured() {
		t.Fatal("Configured = true without a topic")
	}
	if err := sink.Notify(ctx, Notification{Kind: NotifyBlockerOpened, WatchID: "w1"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := sink.Send(ctx, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if doer.calls != 0 {
		t.Fatalf("transport calls = %d, want 0", doer.calls)
	}
}

func TestNtfyNotifySinkSendsFormattedAndRedactedText(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotMethod, gotContentType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	sink := NewNtfyNotifySink(NtfyConfig{
		URL:   srv.URL + "/",
		Topic: "beacon-alerts",
		Doer:  srv.Client(),
	})
	n := Notification{
		Kind:     NotifyEscalateHuman,
		WatchID:  "watch-1",
		PR:       7,
		Revision: 3,
		Detail:   "ghp_abcdefghijklmnopqrstuvwxyz0123456789 Authorization: Bearer topsecret",
	}
	if err := sink.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/beacon-alerts" {
		t.Fatalf("path = %q, want /beacon-alerts", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", gotContentType)
	}
	body := string(gotBody)
	if !strings.Contains(body, "[escalate_human] watch=watch-1 pr=7 revision=3") {
		t.Fatalf("body = %q, want formatted notification", body)
	}
	if strings.Contains(body, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") || strings.Contains(body, "topsecret") {
		t.Fatalf("body kept a secret: %q", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("body = %q, want redaction marker", body)
	}
}

func TestNtfyNotifySinkUsesDefaultURL(t *testing.T) {
	doer := &ntfyFakeDoer{}
	sink := NewNtfyNotifySink(NtfyConfig{Topic: "beacon-alerts", Doer: doer})
	if err := sink.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if doer.calls != 1 || doer.req == nil {
		t.Fatalf("transport calls = %d, want 1", doer.calls)
	}
	if got := doer.req.URL.String(); got != "https://ntfy.sh/beacon-alerts" {
		t.Fatalf("url = %q, want default ntfy URL", got)
	}
}

func TestNtfyNotifySinkReportsServerError(t *testing.T) {
	doer := &ntfyFakeDoer{status: http.StatusInternalServerError, body: "boom"}
	sink := NewNtfyNotifySink(NtfyConfig{Topic: "beacon-alerts", Doer: doer})
	if err := sink.Notify(context.Background(), Notification{Kind: NotifyBlockerOpened, WatchID: "w1"}); err == nil {
		t.Fatal("Notify returned nil for a 500 response")
	}

	doer = &ntfyFakeDoer{err: errors.New("dial failed")}
	sink = NewNtfyNotifySink(NtfyConfig{Topic: "beacon-alerts", Doer: doer})
	if err := sink.Notify(context.Background(), Notification{Kind: NotifyBlockerOpened, WatchID: "w1"}); err == nil {
		t.Fatal("Notify returned nil for a transport error")
	}
}

func TestMultiNotificationSinkDeliversToEverySink(t *testing.T) {
	first := &recordingSink{}
	second := &recordingSink{}
	broken := &recordingSink{err: errors.New("send failed")}
	multi := NewMultiNotificationSink(first, nil, second, broken)
	n := Notification{Kind: NotifyExecutorOrphaned, WatchID: "w1"}

	if err := multi.Notify(context.Background(), n); err == nil {
		t.Fatal("Notify returned nil when one sink failed")
	}
	for i, sink := range []*recordingSink{first, second, broken} {
		if len(sink.got) != 1 || sink.got[0] != n {
			t.Fatalf("sink %d got %+v, want one notification", i, sink.got)
		}
	}
}

type recordingSink struct {
	got []Notification
	err error
}

func (s *recordingSink) Notify(_ context.Context, n Notification) error {
	s.got = append(s.got, n)
	return s.err
}
