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
		var payload struct {
			Number      int `json:"number"`
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
			ObjectAttributes struct {
				IID    int `json:"iid"`
				Number int `json:"number"`
			} `json:"object_attributes"`
			MergeRequest struct {
				IID int `json:"iid"`
			} `json:"merge_request"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(rw, "bad json", http.StatusBadRequest)
			return
		}
		n := payload.PullRequest.Number
		if n == 0 {
			n = payload.ObjectAttributes.IID
		}
		if n == 0 {
			n = payload.MergeRequest.IID
		}
		if n == 0 {
			n = payload.ObjectAttributes.Number
		}
		if n == 0 {
			n = payload.Number
		}
		if n > 0 && w.Changes != nil {
			select {
			case w.Changes <- n:
			case <-time.After(2 * time.Second):
			}
		}
		rw.WriteHeader(http.StatusAccepted)
	})
}

func (w *Webhook) valid(r *http.Request, body []byte) bool {
	if w.Secret == "" {
		return true
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
