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
	rw := serve(w, http.MethodPost, `{"object_attributes":{"iid":5}}`, map[string]string{
		"X-Gitlab-Event": "Merge Request Hook",
	})
	if rw.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rw.Code)
	}
	if len(ch) != 1 || <-ch != 5 {
		t.Fatalf("want MR 5 emitted once, channel len %d", len(ch))
	}
}

func TestWebhookProviderHeaderSelectsParser(t *testing.T) {
	githubFirst := `{"pull_request":{"number":42},"object_attributes":{"iid":77}}`
	gitlabFirst := `{"object_attributes":{"iid":77},"pull_request":{"number":42}}`
	tests := []struct {
		name    string
		body    string
		secret  string
		headers map[string]string
		want    int
	}{
		{"github with auth header", githubFirst, webhookSecret, map[string]string{
			"X-Hub-Signature-256": githubSignature(webhookSecret, []byte(githubFirst)),
		}, 42},
		{"gitlab with auth header", gitlabFirst, webhookSecret, map[string]string{
			"X-Gitlab-Token": webhookSecret,
		}, 77},
		{"github with event header", gitlabFirst, "", map[string]string{
			"X-GitHub-Event": "pull_request",
		}, 42},
		{"gitlab with event header", githubFirst, "", map[string]string{
			"X-Gitlab-Event": "Merge Request Hook",
		}, 77},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan int, 1)
			w := &Webhook{Secret: tc.secret, Changes: ch}
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

func TestWebhookRejectsUnknownProvider(t *testing.T) {
	ch := make(chan int, 1)
	w := &Webhook{Changes: ch}
	body := `{"pull_request":{"number":42},"object_attributes":{"iid":77}}`
	rw := serve(w, http.MethodPost, body, map[string]string{
		"X-Service": "beacon",
	})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rw.Code, rw.Body.String())
	}
	if len(ch) != 0 {
		t.Fatalf("unexpected emission: %d", <-ch)
	}
}
