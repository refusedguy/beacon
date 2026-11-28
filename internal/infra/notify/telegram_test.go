package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// telegramNotifyKey is the canonical advisory key. The registry owns the
// limiter text and the placement; this package owns the row the benchmark
// prints under that key.
const telegramNotifyKey = "telegram-notify"

const telegramPerfPrefix = "beacon-perf: "

// telegramPerfRow is the fixed payload of the canonical row. Every advisory
// harness publishes exactly these eight fields, so adding one here would make
// the comment parser reject the whole pass.
type telegramPerfRow struct {
	Key      string  `json:"key"`
	MedianMS float64 `json:"median_ms"`
	MinMS    float64 `json:"min_ms"`
	MaxMS    float64 `json:"max_ms"`
	Work     int64   `json:"work"`
	Errs     int64   `json:"errs"`
	Limiter  string  `json:"limiter"`
	Cache    string  `json:"cache"`
}

// telegramCache mirrors the advisory cache policy stamp. CI exports
// BEACON_BENCH_CACHE=warm for every measured pass, so two rows from different
// cache policies can never be read as the same sample.
func telegramCache() string {
	if value := os.Getenv("BEACON_BENCH_CACHE"); value != "" {
		return value
	}
	return "unset"
}

// printTelegramPerfRow writes one canonical row for a whole benchmark run.
// The durations slice is the per-iteration timing the benchmark already
// collects, so the row reports the same work the human-readable lines report.
func printTelegramPerfRow(w io.Writer, durations []time.Duration, work, errs int64) {
	if len(durations) == 0 {
		return
	}
	min, max := durations[0], durations[0]
	for _, d := range durations[1:] {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	median := medianDuration(durations)
	data, err := json.Marshal(telegramPerfRow{
		Key:      telegramNotifyKey,
		MedianMS: float64(median) / float64(time.Millisecond),
		MinMS:    float64(min) / float64(time.Millisecond),
		MaxMS:    float64(max) / float64(time.Millisecond),
		Work:     work,
		Errs:     errs,
		Limiter:  "redact regexp chain + json.Marshal + loopback POST",
		Cache:    telegramCache(),
	})
	if err != nil {
		return
	}
	fmt.Fprintln(w, telegramPerfPrefix+string(data))
}

func TestTelegramCanonicalRow(t *testing.T) {
	t.Setenv("BEACON_BENCH_CACHE", "warm")
	durations := []time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond}
	var out bytes.Buffer
	printTelegramPerfRow(&out, durations, 7, 0)

	line := strings.TrimSuffix(out.String(), "\n")
	if !strings.HasPrefix(line, telegramPerfPrefix) {
		t.Fatalf("row has no canonical prefix: %q", line)
	}
	data := strings.TrimPrefix(line, telegramPerfPrefix)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	want := []string{"key", "median_ms", "min_ms", "max_ms", "work", "errs", "limiter", "cache"}
	if len(fields) != len(want) {
		t.Fatalf("row has %d fields, want %d: %s", len(fields), len(want), data)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("row is missing %q: %s", name, data)
		}
	}
	var row telegramPerfRow
	if err := json.Unmarshal([]byte(data), &row); err != nil {
		t.Fatalf("row does not decode: %v", err)
	}
	if row.Key != telegramNotifyKey || row.MedianMS != 2 || row.MinMS != 1 || row.MaxMS != 3 ||
		row.Work != 7 || row.Errs != 0 || row.Cache != "warm" || row.Limiter == "" {
		t.Fatalf("row = %+v, want %s 2/1/3 7/0 warm with a limiter", row, telegramNotifyKey)
	}
}

func TestTelegramPayloadByteIdentical(t *testing.T) {
	for _, text := range []string{"", "plain", `{"quoted":"value"}`, "line\nbreak"} {
		want, err := json.Marshal(map[string]string{"chat_id": benchChatID, "text": text})
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(telegramPayload{ChatID: benchChatID, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("struct payload = %s, map payload = %s", got, want)
		}
	}
}

// wantRedactedText is the literal redaction of benchNotification. The test
// compares the wire body with this fixed string instead of calling Redact to
// build the expectation, so a leak in the redact chain fails the assertion.
const wantRedactedText = "beacon [run-telegram-bench] notify: token [REDACTED] clone https://ci:[REDACTED]@example.com/beacon.git"

func TestTelegramNotifyPayload(t *testing.T) {
	expected, err := json.Marshal(map[string]string{
		"chat_id": benchChatID,
		"text":    wantRedactedText,
	})
	if err != nil {
		t.Fatal(err)
	}

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n := NewTelegram(benchToken, benchChatID)
	n.HTTP = srv.Client()
	n.HTTP.Transport = rewriteTransport{base: n.HTTP.Transport, target: target}
	if err := n.Notify(context.Background(), benchNotification); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !bytes.Equal(body, expected) {
		t.Fatalf("payload = %s, want %s", body, expected)
	}
}

func TestTelegramDisabled(t *testing.T) {
	for _, tc := range []struct{ token, chat string }{
		{"", ""},
		{benchToken, ""},
		{"", benchChatID},
	} {
		n := NewTelegram(tc.token, tc.chat)
		if n.Enabled() {
			t.Errorf("Enabled(%q, %q) = true, want false", tc.token, tc.chat)
		}
		if err := n.Notify(context.Background(), core.Notification{}); err != nil {
			t.Errorf("disabled Notify(%q, %q) = %v, want nil", tc.token, tc.chat, err)
		}
	}
}

func TestTelegramEndpointBuiltOnce(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n := NewTelegram(benchToken, benchChatID)
	n.HTTP = srv.Client()
	n.HTTP.Transport = rewriteTransport{base: n.HTTP.Transport, target: target}
	// Mutating the token after construction must not rebuild the endpoint.
	n.Token = "another-token"
	if err := n.Notify(context.Background(), benchNotification); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	want := "/bot" + url.PathEscape(benchToken) + "/sendMessage"
	if path != want {
		t.Fatalf("request path = %q, want %q", path, want)
	}
}

// failingTransport makes client.Do return a url.Error that repeats the
// endpoint, which carries the bot token in its path.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func TestTelegramRedactsTransportError(t *testing.T) {
	n := NewTelegram(benchToken, benchChatID)
	n.HTTP = &http.Client{Transport: failingTransport{err: errors.New("dial failed")}}
	err := n.Notify(context.Background(), benchNotification)
	if err == nil {
		t.Fatal("Notify with a failing transport returned nil, want an error")
	}
	if strings.Contains(err.Error(), benchToken) {
		t.Fatalf("bot token leaked into error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error is not redacted: %q", err.Error())
	}
}

func TestTelegramStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer srv.Close()

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n := NewTelegram(benchToken, benchChatID)
	n.HTTP = srv.Client()
	n.HTTP.Transport = rewriteTransport{base: n.HTTP.Transport, target: target}
	if err := n.Notify(context.Background(), benchNotification); err == nil {
		t.Fatal("Notify on a 400 status returned nil, want an error")
	}
}

// telegramRequest is what the fixture server observes. A test reads these
// observations instead of asking Notify what it did.
type telegramRequest struct {
	method string
	path   string
	body   []byte
}

// TestTelegramNotify drives Notify through an httptest server. The expected
// body is a literal, so a change to the redact rules fails this test instead
// of moving the expected value with the implementation.
func TestTelegramNotify(t *testing.T) {
	wantBody, err := json.Marshal(map[string]string{"chat_id": benchChatID, "text": wantRedactedText})
	if err != nil {
		t.Fatal(err)
	}
	const escapedToken = "tok/en:special"
	escapedPath := "/bot" + url.PathEscape(escapedToken) + "/sendMessage"
	benchPath := "/bot" + url.PathEscape(benchToken) + "/sendMessage"

	cases := []struct {
		name      string
		token     string
		chat      string
		status    int
		cancel    bool
		wantPath  string
		wantBody  []byte
		wantCount int
		wantErr   bool
	}{
		{name: "disabled sends nothing", token: "", chat: benchChatID, status: http.StatusOK},
		{name: "escaped token path", token: escapedToken, chat: benchChatID,
			status: http.StatusOK, wantPath: escapedPath, wantBody: wantBody, wantCount: 1},
		{name: "redacted body", token: benchToken, chat: benchChatID,
			status: http.StatusOK, wantPath: benchPath, wantBody: wantBody, wantCount: 1},
		{name: "server error", token: benchToken, chat: benchChatID,
			status: http.StatusInternalServerError, wantPath: benchPath, wantBody: wantBody,
			wantCount: 1, wantErr: true},
		{name: "cancelled context", token: benchToken, chat: benchChatID,
			status: http.StatusOK, cancel: true, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan telegramRequest, 4)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- telegramRequest{method: r.Method, path: r.URL.EscapedPath(), body: body}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			target, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			n := NewTelegram(tc.token, tc.chat)
			n.HTTP = srv.Client()
			n.HTTP.Transport = rewriteTransport{base: n.HTTP.Transport, target: target}
			ctx := context.Background()
			if tc.cancel {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cctx
			}

			err = n.Notify(ctx, benchNotification)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Notify error = %v, wantErr = %v", err, tc.wantErr)
			}
			if got := len(requests); got != tc.wantCount {
				t.Fatalf("requests = %d, want %d", got, tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			got := <-requests
			if got.method != http.MethodPost {
				t.Fatalf("method = %q, want POST", got.method)
			}
			if got.path != tc.wantPath {
				t.Fatalf("path = %q, want %q", got.path, tc.wantPath)
			}
			if strings.Count(got.path, "/") != 2 {
				t.Fatalf("token slash was not escaped in path %q", got.path)
			}
			if !bytes.Equal(got.body, tc.wantBody) {
				t.Fatalf("body = %s, want %s", got.body, tc.wantBody)
			}
		})
	}
}
