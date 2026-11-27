package scm

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// payloadAdapter extracts a PR or MR number from one provider's payload shape.
type payloadAdapter func(body []byte) (int, error)

var payloadAdapters = map[string]payloadAdapter{
	"X-Hub-Signature-256": githubPayload,
	"X-GitHub-Event":      githubPayload,
	"X-Gitlab-Token":      gitlabPayload,
	"X-Gitlab-Event":      gitlabPayload,
}

// providerHeaders are checked in order so selection never depends on map
// iteration order. The first header present in the request wins.
var providerHeaders = []string{
	"X-Hub-Signature-256",
	"X-GitHub-Event",
	"X-Gitlab-Token",
	"X-Gitlab-Event",
}

func githubPayload(body []byte) (int, error) {
	var payload struct {
		Number      int `json:"number"`
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, err
	}
	if payload.PullRequest.Number > 0 {
		return payload.PullRequest.Number, nil
	}
	return payload.Number, nil
}

func gitlabPayload(body []byte) (int, error) {
	var payload struct {
		Number           int `json:"number"`
		ObjectAttributes struct {
			IID    int `json:"iid"`
			Number int `json:"number"`
		} `json:"object_attributes"`
		MergeRequest struct {
			IID int `json:"iid"`
		} `json:"merge_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, err
	}
	if payload.ObjectAttributes.IID > 0 {
		return payload.ObjectAttributes.IID, nil
	}
	if payload.MergeRequest.IID > 0 {
		return payload.MergeRequest.IID, nil
	}
	if payload.ObjectAttributes.Number > 0 {
		return payload.ObjectAttributes.Number, nil
	}
	return payload.Number, nil
}

func selectPayloadAdapter(h http.Header) payloadAdapter {
	for _, header := range providerHeaders {
		if h.Get(header) != "" {
			return payloadAdapters[header]
		}
	}
	return nil
}

const (
	webhookDedupeTTL = 5 * time.Minute
	maxWebhookDedupe = 4096
)

// verdictEvents are the GitHub event types that can change a merge verdict.
// Everything else, including ping, issue_comment and label events, only
// produces reconcile bursts without new signal.
var verdictEvents = map[string]bool{
	"pull_request":        true,
	"pull_request_review": true,
	"check_run":           true,
	"check_suite":         true,
	"status":              true,
	"push":                true,
}

// Webhook pushes PR numbers to a reconciler channel without polling. A
// delivery is claimed before it is sent, so a retried provider event cannot
// start a second reconcile burst while the first is still draining.
type Webhook struct {
	Secret  string
	Changes chan<- int

	mu   sync.Mutex
	seen map[string]time.Time
}

// deliveryKey prefers the provider's delivery ID. GitLab has no equivalent
// header, so the body hash stands in for one.
func deliveryKey(r *http.Request, body []byte) string {
	if id := r.Header.Get("X-GitHub-Delivery"); id != "" {
		return "delivery:" + id
	}
	sum := sha256.Sum256(body)
	return "body:" + hex.EncodeToString(sum[:])
}

func (w *Webhook) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(rw, "bad body", http.StatusBadRequest)
			return
		}
		if !w.valid(r, body) {
			http.Error(rw, "bad signature", http.StatusUnauthorized)
			return
		}
		if event := r.Header.Get("X-GitHub-Event"); event != "" && !verdictEvents[event] {
			rw.WriteHeader(http.StatusAccepted)
			return
		}
		adapter := selectPayloadAdapter(r.Header)
		if adapter == nil {
			http.Error(rw, "unknown provider", http.StatusBadRequest)
			return
		}
		n, err := adapter(body)
		if err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		if n <= 0 || w.Changes == nil {
			rw.WriteHeader(http.StatusAccepted)
			return
		}
		key := deliveryKey(r, body)
		if !w.claim(key) {
			rw.WriteHeader(http.StatusAccepted)
			return
		}
		select {
		case w.Changes <- n:
			rw.WriteHeader(http.StatusAccepted)
		default:
			// A full channel means the reconciler is busy. Tell the provider
			// to retry instead of accepting a delivery we dropped.
			w.release(key)
			rw.Header().Set("Retry-After", "1")
			http.Error(rw, "reconciler busy", http.StatusServiceUnavailable)
		}
	})
}

// claim reserves key for one delivery. It is the only place that writes seen,
// so two concurrent copies of the same webhook cannot both pass.
func (w *Webhook) claim(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked()
	if _, ok := w.seen[key]; ok {
		return false
	}
	if w.seen == nil {
		w.seen = make(map[string]time.Time)
	}
	w.seen[key] = time.Now()
	return true
}

// release drops a claim whose delivery was not accepted, so a provider retry
// can still deliver it once the reconciler has room.
func (w *Webhook) release(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seen, key)
}

func (w *Webhook) pruneLocked() {
	if len(w.seen) == 0 {
		return
	}
	now := time.Now()
	for key, at := range w.seen {
		if now.Sub(at) > webhookDedupeTTL {
			delete(w.seen, key)
		}
	}
	for len(w.seen) >= maxWebhookDedupe {
		var oldest string
		var at time.Time
		first := true
		for key, claimed := range w.seen {
			if first || claimed.Before(at) {
				oldest, at, first = key, claimed, false
			}
		}
		delete(w.seen, oldest)
	}
}

func (w *Webhook) valid(r *http.Request, body []byte) bool {
	if w.Secret == "" {
		return false
	}
	if sig := r.Header.Get("X-Hub-Signature-256"); sig != "" {
		if !strings.HasPrefix(sig, "sha256=") {
			return false
		}
		mac := hmac.New(sha256.New, []byte(w.Secret))
		_, _ = mac.Write(body)
		got, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
		return err == nil && hmac.Equal(mac.Sum(nil), got)
	}
	if token := r.Header.Get("X-Gitlab-Token"); token != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(w.Secret)) == 1
	}
	return false
}
