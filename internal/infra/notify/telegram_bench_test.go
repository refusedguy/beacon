package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// Notify is the one path in this package that redacts text, marshals a payload
// and posts it, yet no benchmark covers it. This file times that chain against
// a loopback server so the redact regexps, the json.Marshal and the POST all
// stay inside the measured section. The Telegram endpoint is a constant in
// Notify, so a rewrite transport points the request at the fixture server.
//
// CI runs a bounded pass with five counts:
//
//	go test -run '^$' -bench '^BenchmarkTelegramNotify$' -benchtime=100x -count=5 -benchmem ./internal/infra/notify/
const (
	nameTelegramNotify    = "telegram-notify"
	limiterTelegramNotify = "telegram-notify-limiter-redact-marshal-loopback-post"

	benchToken  = "123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	benchChatID = "-100bench"
)

// benchNotification carries two secrets so Redact takes its regexp chain
// instead of returning the string untouched through the HasCandidate gate.
var benchNotification = core.Notification{
	RunID:   core.RunID("run-telegram-bench"),
	Subject: "notify",
	Body:    "token ghp_abcdefghijklmnopqrstuvwxyz123456 clone https://ci:s3cr3t@example.com/beacon.git",
}

// benchExpectedBody is the payload Notify marshals at telegram.go:38-41. The
// fixture compares every received body with these bytes, so a change to the
// redaction or the payload shape turns into a 500 and an errs count.
func benchExpectedBody() []byte {
	text := redact.Redact(fmt.Sprintf("beacon [%s] %s: %s",
		benchNotification.RunID, benchNotification.Subject, benchNotification.Body))
	body, _ := json.Marshal(map[string]string{"chat_id": benchChatID, "text": text})
	return body
}

// rewriteTransport keeps the request that Notify built and only swaps the
// origin. The path, method, headers and body reach the fixture unchanged.
type rewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = ""
	return rt.base.RoundTrip(clone)
}

// telegramNotifyBench holds one notifier pointed at a bounded loopback server.
// The handler answers 200 with a fixed notification only when the POST carries
// the expected redacted payload. Any other request gets a 500, which Notify
// turns into an errs count, so the benchmark cannot pass on a silent mismatch.
type telegramNotifyBench struct {
	notifier *Telegram
	ctx      context.Context
	requests *atomic.Int64
}

func newTelegramNotifyBench(b *testing.B) *telegramNotifyBench {
	b.Helper()
	expected := benchExpectedBody()
	wantPath := "/bot" + url.PathEscape(benchToken) + "/sendMessage"

	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.Path != wantPath || !bytes.Equal(body, expected) {
			http.Error(w, "unexpected telegram request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	b.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL)
	if err != nil {
		b.Fatal(err)
	}
	client := srv.Client()
	client.Transport = rewriteTransport{base: client.Transport, target: target}

	notifier := NewTelegram(benchToken, benchChatID)
	notifier.HTTP = client
	return &telegramNotifyBench{notifier: notifier, ctx: context.Background(), requests: &requests}
}

// runTelegramNotify performs one Notify call. Work counts the send and the
// loopback request it served. Errs counts the Notify error, which covers both
// transport failures and the non-2xx status from the fixture.
func runTelegramNotify(h *telegramNotifyBench) (int64, int64) {
	before := h.requests.Load()
	if err := h.notifier.Notify(h.ctx, benchNotification); err != nil {
		return 0, 1
	}
	return 1 + h.requests.Load() - before, 0
}

func medianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// BenchmarkTelegramNotify times one redact, marshal and loopback POST per
// iteration. Work, errs and the named limiter print on every pass, so a bounded
// smoke run and a full run report the same shape. A non-zero errs count fails
// the pass instead of masquerading as a fast one.
func BenchmarkTelegramNotify(b *testing.B) {
	h := newTelegramNotifyBench(b)
	if err := h.notifier.Notify(h.ctx, benchNotification); err != nil {
		b.Fatalf("warm-up notify: %v", err)
	}

	durations := make([]time.Duration, 0, b.N)
	var work, errs int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		w, e := runTelegramNotify(h)
		durations = append(durations, time.Since(start))
		work += w
		errs += e
		if e != 0 {
			b.Fatalf("telegram notify failed on iteration %d", i)
		}
	}
	b.StopTimer()

	median := medianDuration(durations)
	fmt.Printf("%s: sends=%d limiter=%s\n", nameTelegramNotify, b.N, limiterTelegramNotify)
	fmt.Printf("%s: work=%d errs=%d limiter=%s\n", nameTelegramNotify, work, errs, limiterTelegramNotify)
	b.ReportMetric(float64(median.Nanoseconds()), nameTelegramNotify+"-median-ns")
	b.ReportMetric(float64(work), nameTelegramNotify+"-work")
	b.ReportMetric(float64(errs), nameTelegramNotify+"-errs")
	b.ReportMetric(1, limiterTelegramNotify)
	// One canonical row for the whole run. The advisory comment reads this row
	// under the telegram-notify registry key.
	printTelegramPerfRow(os.Stdout, durations, work, errs)
}
