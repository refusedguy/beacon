package scm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const webhookSecret = "p$ss{w0rd"

func githubSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func serve(w *Webhook, method, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/webhook", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rw := httptest.NewRecorder()
	w.Handler().ServeHTTP(rw, req)
	return rw
}

func TestWebhookGitHubPullRequest(t *testing.T) {
	body := `{"action":"opened","number":1,"pull_request":{"number":42}}`
	ch := make(chan int, 1)
	w := &Webhook{Secret: webhookSecret, Changes: ch}
	rw := serve(w, http.MethodPost, body, map[string]string{
		"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(body)),
	})
	if rw.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rw.Code, rw.Body.String())
	}
	if len(ch) != 1 || <-ch != 42 {
		t.Fatalf("want PR 42 emitted once, channel len %d", len(ch))
	}
}

func TestWebhookGitHubNumberFallback(t *testing.T) {
	body := `{"number":9}`
	ch := make(chan int, 1)
	w := &Webhook{Secret: webhookSecret, Changes: ch}
	rw := serve(w, http.MethodPost, body, map[string]string{
		"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(body)),
	})
	if rw.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rw.Code)
	}
	if len(ch) != 1 || <-ch != 9 {
		t.Fatalf("want PR 9 emitted once, channel len %d", len(ch))
	}
}

func TestWebhookGitLabMergeRequest(t *testing.T) {
	body := `{"object_kind":"merge_request","object_attributes":{"iid":77,"action":"open"}}`
	ch := make(chan int, 1)
	w := &Webhook{Secret: webhookSecret, Changes: ch}
	rw := serve(w, http.MethodPost, body, map[string]string{
		"X-Gitlab-Token": webhookSecret,
	})
	if rw.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rw.Code, rw.Body.String())
	}
	if len(ch) != 1 || <-ch != 77 {
		t.Fatalf("want MR 77 emitted once, channel len %d", len(ch))
	}
}

func TestWebhookRejectsBadAuth(t *testing.T) {
	body := `{"pull_request":{"number":42}}`
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"github bad signature", map[string]string{"X-Hub-Signature-256": "sha256=deadbeef"}},
		{"github malformed signature", map[string]string{"X-Hub-Signature-256": "not-a-hash"}},
		{"gitlab bad token", map[string]string{"X-Gitlab-Token": "wrong-secret"}},
		{"no credentials", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan int, 1)
			w := &Webhook{Secret: webhookSecret, Changes: ch}
			rw := serve(w, http.MethodPost, body, tc.headers)
			if rw.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rw.Code)
			}
			if len(ch) != 0 {
				t.Fatalf("unexpected emission: %d", <-ch)
			}
		})
	}
}

func TestWebhookRejectsBadRequest(t *testing.T) {
	w := &Webhook{Secret: webhookSecret, Changes: make(chan int, 1)}
	if rw := serve(w, http.MethodGet, "", nil); rw.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rw.Code)
	}
	if rw := serve(w, http.MethodPost, "not json", map[string]string{
		"X-Gitlab-Token": webhookSecret,
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", rw.Code)
	}
}

func TestWebhookWithoutSecretRejectsUnsigned(t *testing.T) {
	ch := make(chan int, 1)
	w := &Webhook{Changes: ch}
	rw := serve(w, http.MethodPost, `{"object_attributes":{"iid":5}}`, map[string]string{
		"X-Gitlab-Event": "Merge Request Hook",
	})
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
	if len(ch) != 0 {
		t.Fatalf("unexpected emission: %d", <-ch)
	}
	if w.valid(httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("{}")), []byte("{}")) {
		t.Fatal("valid accepted a request without a secret")
	}
}

func TestWebhookSignedAndUnsignedPosts(t *testing.T) {
	body := `{"number":42,"pull_request":{"number":42},"object_attributes":{"iid":42}}`
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"github signed", map[string]string{
			"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(body)),
		}, http.StatusAccepted},
		{"github unsigned", nil, http.StatusUnauthorized},
		{"gitlab signed", map[string]string{"X-Gitlab-Token": webhookSecret}, http.StatusAccepted},
		{"gitlab unsigned", map[string]string{"X-Gitlab-Event": "Merge Request Hook"}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan int, 1)
			w := &Webhook{Secret: webhookSecret, Changes: ch}
			rw := serve(w, http.MethodPost, body, tc.headers)
			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rw.Code, tc.want, rw.Body.String())
			}
			if tc.want == http.StatusAccepted {
				if len(ch) != 1 || <-ch != 42 {
					t.Fatalf("want PR 42 emitted once, channel len %d", len(ch))
				}
			} else if len(ch) != 0 {
				t.Fatalf("unexpected emission: %d", <-ch)
			}
		})
	}
}

func TestWebhookProviderHeaderSelectsParser(t *testing.T) {
	githubFirst := `{"pull_request":{"number":42},"object_attributes":{"iid":77}}`
	gitlabFirst := `{"object_attributes":{"iid":77},"pull_request":{"number":42}}`
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		want    int
	}{
		{"github with auth header", githubFirst, map[string]string{
			"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(githubFirst)),
		}, 42},
		{"gitlab with auth header", gitlabFirst, map[string]string{
			"X-Gitlab-Token": webhookSecret,
		}, 77},
		{"github event before gitlab token", gitlabFirst, map[string]string{
			"X-GitHub-Event": "pull_request",
			"X-Gitlab-Token": webhookSecret,
		}, 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan int, 1)
			w := &Webhook{Secret: webhookSecret, Changes: ch}
			rw := serve(w, http.MethodPost, tc.body, tc.headers)
			if rw.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202: %s", rw.Code, rw.Body.String())
			}
			if len(ch) != 1 || <-ch != tc.want {
				t.Fatalf("want PR %d emitted once, channel len %d", tc.want, len(ch))
			}
		})
	}
}

func TestSelectPayloadAdapterEventHeaders(t *testing.T) {
	body := []byte(`{"pull_request":{"number":42},"object_attributes":{"iid":77}}`)
	cases := []struct {
		header string
		want   int
	}{
		{"X-GitHub-Event", 42},
		{"X-Gitlab-Event", 77},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			h := http.Header{}
			h.Set(tc.header, "event")
			adapter := selectPayloadAdapter(h)
			if adapter == nil {
				t.Fatal("no adapter selected")
			}
			got, err := adapter(body)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("PR = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSelectPayloadAdapterRejectsUnknownProvider(t *testing.T) {
	h := http.Header{}
	h.Set("X-Service", "beacon")
	if adapter := selectPayloadAdapter(h); adapter != nil {
		t.Fatal("unknown provider selected an adapter")
	}
}

func TestWebhookDuplicateDeliveryProducesOneObserve(t *testing.T) {
	body := `{"action":"synchronize","number":42,"pull_request":{"number":42}}`
	ch := make(chan int, 4)
	w := &Webhook{Secret: webhookSecret, Changes: ch}
	headers := map[string]string{
		"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(body)),
		"X-GitHub-Event":      "pull_request",
		"X-GitHub-Delivery":   "delivery-1",
	}
	for i := 0; i < 3; i++ {
		if rw := serve(w, http.MethodPost, body, headers); rw.Code != http.StatusAccepted {
			t.Fatalf("delivery %d status = %d, want 202", i, rw.Code)
		}
	}
	if len(ch) != 1 {
		t.Fatalf("emissions = %d, want 1 for three copies of one delivery", len(ch))
	}
	if got := <-ch; got != 42 {
		t.Fatalf("emitted %d, want 42", got)
	}
}

func TestWebhookIgnoresEventsThatCannotChangeVerdict(t *testing.T) {
	body := `{"number":42,"pull_request":{"number":42}}`
	ch := make(chan int, 4)
	w := &Webhook{Secret: webhookSecret, Changes: ch}
	for _, event := range []string{"ping", "issue_comment", "label", "star"} {
		rw := serve(w, http.MethodPost, body, map[string]string{
			"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(body)),
			"X-GitHub-Event":      event,
			"X-GitHub-Delivery":   "delivery-" + event,
		})
		if rw.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, want 202", event, rw.Code)
		}
	}
	if len(ch) != 0 {
		t.Fatalf("emissions = %d, want 0 for verdict-irrelevant events", len(ch))
	}

	rw := serve(w, http.MethodPost, body, map[string]string{
		"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(body)),
		"X-GitHub-Event":      "check_run",
		"X-GitHub-Delivery":   "delivery-check-run",
	})
	if rw.Code != http.StatusAccepted {
		t.Fatalf("check_run status = %d, want 202", rw.Code)
	}
	if len(ch) != 1 || <-ch != 42 {
		t.Fatalf("want one emission for check_run, channel len %d", len(ch))
	}
}
