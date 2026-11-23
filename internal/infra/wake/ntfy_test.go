package wake

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	doer := &ntfyFakeDoer{}
	sink := NewNtfyNotifySink(NtfyConfig{
		URL:   "https://ntfy.example.test/",
		Topic: "beacon-alerts",
		Doer:  doer,
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
	if doer.calls != 1 || doer.req == nil {
		t.Fatalf("transport calls = %d, want 1", doer.calls)
	}
	req := doer.req
	if req.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", req.Method)
	}
	if req.URL.String() != "https://ntfy.example.test/beacon-alerts" {
		t.Fatalf("url = %q, want public ntfy endpoint", req.URL.String())
	}
	if got := req.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", got)
	}
	gotBody, _ := io.ReadAll(req.Body)
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

func TestNtfyNotifySinkRejectsUnsafeURL(t *testing.T) {
	bad := []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.test",
		"example.test/topic",
		"//example.test",
		"http://",
		"http:opaque",
		"%zz",
		"http://169.254.169.254/latest/meta-data",
		"http://127.0.0.1",
		"http://10.0.0.5",
		"http://192.168.1.10",
		"http://[::1]",
		"http://localhost",
	}
	for _, raw := range bad {
		t.Run(raw, func(t *testing.T) {
			doer := &ntfyFakeDoer{}
			sink := NewNtfyNotifySink(NtfyConfig{URL: raw, Topic: "alerts", Doer: doer})
			if sink.Configured() {
				t.Fatalf("Configured = true for URL %q", raw)
			}
			if err := sink.Send(context.Background(), "hello"); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if doer.calls != 0 {
				t.Fatalf("transport calls = %d, want 0 for URL %q", doer.calls, raw)
			}
		})
	}
}

func TestNtfyNotifySinkAcceptsValidURL(t *testing.T) {
	for _, raw := range []string{"http://example.test", "https://example.test/base/"} {
		t.Run(raw, func(t *testing.T) {
			doer := &ntfyFakeDoer{}
			sink := NewNtfyNotifySink(NtfyConfig{URL: raw, Topic: "alerts", Doer: doer})
			if !sink.Configured() {
				t.Fatalf("Configured = false for URL %q", raw)
			}
			if err := sink.Send(context.Background(), "hello"); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if doer.calls != 1 || doer.req == nil {
				t.Fatalf("transport calls = %d, want 1", doer.calls)
			}
			want := strings.TrimRight(raw, "/") + "/alerts"
			if got := doer.req.URL.String(); got != want {
				t.Fatalf("url = %q, want %q", got, want)
			}
		})
	}
}

func TestNtfyNotifySinkRejectsUnsafeTopic(t *testing.T) {
	for _, topic := range []string{"bad topic", "a/b", "topic\ninject", "a?b", "topic#frag", ""} {
		t.Run(topic, func(t *testing.T) {
			doer := &ntfyFakeDoer{}
			sink := NewNtfyNotifySink(NtfyConfig{
				URL:   "https://ntfy.example.test",
				Topic: topic,
				Doer:  doer,
			})
			if sink.Configured() {
				t.Fatalf("Configured = true for topic %q", topic)
			}
			if err := sink.Send(context.Background(), "hello"); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if doer.calls != 0 {
				t.Fatalf("transport calls = %d, want 0 for topic %q", doer.calls, topic)
			}
		})
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

func TestNtfyTransportErrorKeepsCauseSentinelsWithoutLeakingSecret(t *testing.T) {
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	cause := &url.Error{
		Op:  "Post",
		URL: "https://ntfy.example.test/" + secret + "/alerts",
		Err: context.DeadlineExceeded,
	}
	sink := NewNtfyNotifySink(NtfyConfig{
		URL:   "https://ntfy.example.test",
		Topic: "alerts",
		Doer:  &ntfyFakeDoer{err: cause},
	})

	err := sink.Send(context.Background(), "hello")
	if err == nil {
		t.Fatal("transport failure must return an error")
	}
	want := "wake: ntfy: transport: " + sink.sanitize(cause.Error())
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("err leaked the secret: %v", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Fatalf("errors.As reached url.Error with URL %q", urlErr.URL)
	}
	for node := errors.Unwrap(err); node != nil; node = errors.Unwrap(node) {
		if strings.Contains(node.Error(), secret) {
			t.Fatalf("unwrapped chain leaked the secret: %v", node)
		}
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
