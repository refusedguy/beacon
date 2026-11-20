// Package wake delivers a wake prompt to a parked opencode session through the
// HTTP API, with no plugin and no second protocol (ADR 0075). It owns the narrow
// POST /api/session/{sessionID}/prompt seam so callers only name a session, a
// text and an idempotency key. The package keeps no agent names and starts no
// server (ADR 0024, ADR 0029).
package wake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	authUser = "opencode"

	// maxBodyBytes bounds the response body we are willing to read (ADR 0060 п6).
	maxBodyBytes = 1 << 20
)

// Delivery is the admission mode of one prompt. steer schedules the agent loop
// right away; queue only stores the input (ADR 0077). A wake defaults to steer
// because a wake must start execution, not park the session again.
type Delivery string

const (
	DeliverySteer Delivery = "steer"
	DeliveryQueue Delivery = "queue"
)

// Request is one wake delivery.
type Request struct {
	// SessionID is the watched session, used in the URL path.
	SessionID string
	// Text carries the new fact and the obligation id. It is sent verbatim.
	Text string
	// Delivery is steer or queue. An empty value means steer.
	Delivery Delivery
	// DeliveryID is the stable id of this wake. A repeat with the same id
	// returns the original admission and creates no second task. An empty id is
	// derived from SessionID and Text so identical calls collapse as well.
	DeliveryID string
}

// Result is the honest outcome of one admitted prompt.
type Result struct {
	// ID is data.id of the admission, the inbox id of the queued input.
	ID string
	// Delivery is the mode the session admitted, echoed from data.delivery.
	Delivery Delivery
	// Duplicate is true when the same DeliveryID was already admitted. The
	// first Result is returned unchanged, so no second task is created.
	Duplicate bool
}

// Doer is the single HTTP seam. *http.Client implements it, and tests pass a
// fake so no real server is needed.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Deliverer posts wake prompts to one opencode server and keeps a small
// in-memory journal of admissions by delivery id. The journal makes a repeated
// wake with the same id return the first result without a second POST.
type Deliverer struct {
	baseURL  string
	password string
	doer     Doer

	mu       sync.Mutex
	admitted map[string]Result
}

// New builds a Deliverer for the opencode server at baseURL. A nil doer means a
// real *http.Client. The address is validated like the thin client: only http
// and https with a host are accepted.
func New(baseURL, password string, doer Doer) (*Deliverer, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return nil, fmt.Errorf("wake: пустой url сервиса opencode")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("wake: неверный url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("wake: неверная схема url %q", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("wake: url без хоста %q", raw)
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &Deliverer{
		baseURL:  strings.TrimRight(raw, "/"),
		password: password,
		doer:     doer,
		admitted: make(map[string]Result),
	}, nil
}

// normalizeDelivery gives an absent mode its wake meaning.
func normalizeDelivery(d Delivery) Delivery {
	if d == "" {
		return DeliverySteer
	}
	return d
}

// validDelivery reports whether d is one of the two defined modes.
func validDelivery(d Delivery) bool {
	return d == DeliverySteer || d == DeliveryQueue
}

// idempotencyKey returns the caller's id or derives a stable one from the
// session and the text. The derivation keeps repeats without an explicit id
// from creating a second task.
func idempotencyKey(req Request) string {
	if req.DeliveryID != "" {
		return req.DeliveryID
	}
	sum := sha256.Sum256([]byte(req.SessionID + "\x00" + req.Text))
	return hex.EncodeToString(sum[:])
}

// Deliver admits one wake prompt and returns its inbox id. A repeat with the
// same idempotency key returns the first admission with Duplicate=true and
// sends no second request, so the session starts at most one task.
func (d *Deliverer) Deliver(ctx context.Context, req Request) (Result, error) {
	mode := normalizeDelivery(req.Delivery)
	if !validDelivery(mode) {
		return Result{}, fmt.Errorf("wake: неизвестный delivery %q", string(req.Delivery))
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return Result{}, fmt.Errorf("wake: пустой sessionID")
	}
	if req.Text == "" {
		return Result{}, fmt.Errorf("wake: пустой text")
	}
	if d == nil || d.doer == nil {
		return Result{}, fmt.Errorf("wake: не настроен HTTP-клиент")
	}
	key := idempotencyKey(req)

	d.mu.Lock()
	prior, seen := d.admitted[key]
	d.mu.Unlock()
	if seen {
		prior.Duplicate = true
		return prior, nil
	}

	payload := struct {
		Text     string   `json:"text"`
		Delivery Delivery `json:"delivery"`
	}{Text: req.Text, Delivery: mode}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("wake: кодирование запроса: %v", err)
	}
	path := "/api/session/" + url.PathEscape(req.SessionID) + "/prompt"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", key)
	httpReq.SetBasicAuth(authUser, d.password)

	resp, err := d.doer.Do(httpReq)
	if err != nil {
		return Result{}, err
	}
	if resp == nil {
		return Result{}, fmt.Errorf("wake: пустой ответ сервера")
	}

	result, duplicate, err := readAdmission(resp, mode)
	if err != nil {
		return Result{}, err
	}
	if duplicate {
		result.Duplicate = true
	}
	d.mu.Lock()
	d.admitted[key] = result
	d.mu.Unlock()
	return result, nil
}

// readAdmission parses one prompt response. It returns duplicate=true for a
// 409 duplicate id, taking the original admission from the body when present.
func readAdmission(resp *http.Response, mode Delivery) (Result, bool, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Result{}, false, fmt.Errorf("wake: чтение ответа: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusConflict {
			// ADR 0077: 409 is a duplicate message id, not a busy session.
			original := parseAdmission(raw)
			if original.Delivery == "" {
				original.Delivery = mode
			}
			return original, true, nil
		}
		return Result{}, false, fmt.Errorf("wake POST: %s: %s", resp.Status, msg)
	}

	admission := parseAdmission(raw)
	if admission.ID == "" {
		return Result{}, false, fmt.Errorf("wake POST: в ответе нет data.id")
	}
	if admission.Delivery != mode {
		return Result{}, false, fmt.Errorf("wake POST: data.delivery = %q, ожидалось %q", string(admission.Delivery), string(mode))
	}
	return admission, false, nil
}

// parseAdmission reads {"data":{"id":...,"delivery":...}} and also accepts a
// bare object for handlers that omit the envelope.
func parseAdmission(raw []byte) Result {
	var envelope struct {
		Data struct {
			ID       string   `json:"id"`
			Delivery Delivery `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Data.ID != "" {
		return Result{ID: envelope.Data.ID, Delivery: envelope.Data.Delivery}
	}
	var bare struct {
		ID       string   `json:"id"`
		Delivery Delivery `json:"delivery"`
	}
	if err := json.Unmarshal(raw, &bare); err == nil {
		return Result{ID: bare.ID, Delivery: bare.Delivery}
	}
	return Result{}
}
