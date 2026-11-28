package scm

import (
	"net/http"
	"testing"
)

// TestWebhookGitLabFallbackShapes covers every field gitlabPayload reads. The
// request goes through the handler so the assertion is on the number the
// provider would see queued for reconciliation.
func TestWebhookGitLabFallbackShapes(t *testing.T) {
	const primary = `{"object_attributes":{"iid":77,"number":99},"merge_request":{"iid":88},"number":11}`
	cases := []struct {
		name string
		body string
		want int
	}{
		{"object attributes iid", `{"object_attributes":{"iid":77}}`, 77},
		{"merge request iid", `{"merge_request":{"iid":88}}`, 88},
		{"object attributes number", `{"object_attributes":{"number":99}}`, 99},
		{"top level number", `{"number":11}`, 11},
		{"iid wins over every fallback", primary, 77},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan int, 1)
			w := &Webhook{Secret: webhookSecret, Changes: ch}
			rw := serve(w, http.MethodPost, tc.body, map[string]string{
				"X-Gitlab-Token": webhookSecret,
			})
			if rw.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202: %s", rw.Code, rw.Body.String())
			}
			if len(ch) != 1 || <-ch != tc.want {
				t.Fatalf("want MR %d emitted once, channel len %d", tc.want, len(ch))
			}
		})
	}
}

// TestWebhookFullChannelReleasesClaim proves a delivery the reconciler cannot
// accept is not consumed. The provider sees a retry signal, and a retry after
// the channel drains is delivered once.
func TestWebhookFullChannelReleasesClaim(t *testing.T) {
	const body = `{"object_attributes":{"iid":43}}`
	headers := map[string]string{"X-Gitlab-Token": webhookSecret}
	ch := make(chan int, 1)
	ch <- 42
	w := &Webhook{Secret: webhookSecret, Changes: ch}

	rw := serve(w, http.MethodPost, body, headers)
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rw.Code, rw.Body.String())
	}
	if got := rw.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if len(ch) != 1 || <-ch != 42 {
		t.Fatalf("full channel lost its queued delivery, len %d", len(ch))
	}

	rw = serve(w, http.MethodPost, body, headers)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202: %s", rw.Code, rw.Body.String())
	}
	if len(ch) != 1 || <-ch != 43 {
		t.Fatalf("retry did not deliver MR 43, channel len %d", len(ch))
	}
}
