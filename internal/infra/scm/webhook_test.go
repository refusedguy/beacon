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

func TestWebhookWithoutSecretAcceptsUnsigned(t *testing.T) {
	ch := make(chan int, 1)
	w := &Webhook{Changes: ch}
	rw := serve(w, http.MethodPost, `{"object_attributes":{"iid":5}}`, nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rw.Code)
	}
	if len(ch) != 1 || <-ch != 5 {
		t.Fatalf("want MR 5 emitted once, channel len %d", len(ch))
	}
}
