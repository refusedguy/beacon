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

// Webhook pushes PR numbers to a reconciler channel without polling.
type Webhook struct {
	Secret  string
	Changes chan<- int
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
		if n > 0 && w.Changes != nil {
			w.deliver(n)
		}
		rw.WriteHeader(http.StatusAccepted)
	})
}

// deliver hands n to the reconciler, waiting at most two seconds when the
// channel is full. The non-blocking send first keeps the common case free of a
// timer allocation; only a full channel pays for a stoppable timer.
func (w *Webhook) deliver(n int) {
	select {
	case w.Changes <- n:
		return
	default:
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case w.Changes <- n:
	case <-timer.C:
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
