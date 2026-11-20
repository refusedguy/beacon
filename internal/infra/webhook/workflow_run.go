package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// GitHub headers and the workflow_run event this handler accepts. Header
// lookup is case-insensitive in net/http; these are go-github's spellings.
const (
	EventHeader     = "X-Github-Event"
	DeliveryHeader  = "X-Github-Delivery"
	SignatureHeader = "X-Hub-Signature-256"

	EventWorkflowRun = "workflow_run"

	ActionRequested  = "requested"
	ActionInProgress = "in_progress"
	ActionCompleted  = "completed"
)

// githubDeliveryNamespace keeps a delivery id distinct from the clientRequest
// and stream-cursor namespaces built by domain.IdempotencyKey (ADR 0183).
const githubDeliveryNamespace = "github-delivery"

// WorkflowRunEventPort is the domain-shaped port a CI bridge adapter
// implements. A hint carries identifiers and the action only: the payload's
// status and conclusion are deliberately absent, so a webhook can never become
// a verdict source. The signature uses just stdlib values, so cibridge can
// satisfy the port without importing this infra package (ADR 0005).
type WorkflowRunEventPort interface {
	WorkflowRunHint(ctx context.Context, repo string, pr int, headSHA, action string, at time.Time) error
}

// DeliveryJournal is the idempotency port for incoming deliveries. A durable
// adapter backs it with UNIQUE(delivery_id) (ADR 0183); the in-memory form
// below is enough for a single process and for tests.
type DeliveryJournal interface {
	// ClaimDelivery records one namespaced delivery key. The first caller
	// gets claimed=true; every repeat gets false without a second publish.
	ClaimDelivery(ctx context.Context, key string, at time.Time) (claimed bool, err error)
}

// MemoryDeliveryJournal remembers delivery keys in one process. It converges
// under concurrent redelivery: exactly one caller can claim a key.
type MemoryDeliveryJournal struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewMemoryDeliveryJournal builds an empty delivery journal.
func NewMemoryDeliveryJournal() *MemoryDeliveryJournal {
	return &MemoryDeliveryJournal{seen: make(map[string]time.Time)}
}

// ClaimDelivery implements DeliveryJournal.
func (j *MemoryDeliveryJournal) ClaimDelivery(_ context.Context, key string, at time.Time) (bool, error) {
	if j == nil {
		return false, errors.New("webhook: nil delivery journal")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.seen == nil {
		j.seen = make(map[string]time.Time)
	}
	if _, ok := j.seen[key]; ok {
		return false, nil
	}
	j.seen[key] = at
	return true, nil
}

var (
	_ DeliveryJournal = (*MemoryDeliveryJournal)(nil)
	_ http.Handler    = (*WorkflowHook)(nil)
)

type workflowRunPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	WorkflowRun struct {
		ID           int64  `json:"id"`
		RunAttempt   int    `json:"run_attempt"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	} `json:"workflow_run"`
}

// WorkflowHook validates a signed workflow_run delivery and forwards a hint
// to the bridge. It never calls GitHub and never changes a Watch or its verdict.
type WorkflowHook struct {
	secret  []byte
	journal DeliveryJournal
	sink    WorkflowRunEventPort
	clock   domain.Clock
	maxBody int64
}

// NewWorkflowHook builds a workflow_run hook. An empty secret or a missing port
// is refused: a hook that cannot verify or cannot publish must not start.
func NewWorkflowHook(secret []byte, journal DeliveryJournal, sink WorkflowRunEventPort, clock domain.Clock) (*WorkflowHook, error) {
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	if journal == nil || sink == nil {
		return nil, errors.New("webhook: delivery journal and bridge sink are required")
	}
	if clock == nil {
		clock = wallClock{}
	}
	return &WorkflowHook{
		secret:  append([]byte(nil), secret...),
		journal: journal,
		sink:    sink,
		clock:   clock,
		maxBody: MaxBodyBytes,
	}, nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// ServeHTTP verifies the signature on the raw body, claims the delivery once,
// and publishes one hint per PR. A missing PR list publishes a single hint
// with PR 0 so the adapter can match a fork push by head SHA (T3.377).
func (h *WorkflowHook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "webhook: POST required")
		return
	}
	body, err := ReadBody(r.Body, h.maxBody)
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, "body_too_large", err.Error())
			return
		}
		writeJSON(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	if !VerifySignature(h.secret, body, r.Header.Get(SignatureHeader)) {
		writeJSON(w, http.StatusUnauthorized, "invalid_signature", "webhook: signature mismatch")
		return
	}
	if r.Header.Get(EventHeader) != EventWorkflowRun {
		writeJSON(w, http.StatusAccepted, "ignored", "not a workflow_run delivery")
		return
	}
	deliveryID := strings.TrimSpace(r.Header.Get(DeliveryHeader))
	if deliveryID == "" {
		writeJSON(w, http.StatusBadRequest, "missing_delivery_id", "webhook: X-GitHub-Delivery required")
		return
	}
	var payload workflowRunPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if !supportedAction(payload.Action) {
		writeJSON(w, http.StatusAccepted, "ignored", "unsupported workflow_run action")
		return
	}
	run := payload.WorkflowRun
	if run.RunAttempt > 1 {
		// A re-run repeats the same workflow_run id with a higher attempt.
		// The first attempt already asked for a re-read, so a second hint
		// would be duplicate work. Polling closes any gap (ADR 0183).
		writeJSON(w, http.StatusAccepted, "ignored", "workflow re-run attempt")
		return
	}
	repo := payload.Repository.FullName
	if repo == "" {
		repo = run.Repository.FullName
	}
	at := h.clock.Now()
	claimed, err := h.journal.ClaimDelivery(r.Context(), domain.IdempotencyKey(githubDeliveryNamespace, deliveryID), at)
	if err != nil {
		observe.ActorError("webhook-delivery", deliveryID, err)
		writeJSON(w, http.StatusInternalServerError, "journal_failed", "webhook: delivery journal failed")
		return
	}
	if !claimed {
		writeJSON(w, http.StatusAccepted, "duplicate", "delivery already handled")
		return
	}
	prs := make([]int, 0, len(run.PullRequests))
	for _, pr := range run.PullRequests {
		if pr.Number > 0 {
			prs = append(prs, pr.Number)
		}
	}
	if len(prs) == 0 {
		prs = append(prs, 0)
	}
	for _, pr := range prs {
		if err := h.sink.WorkflowRunHint(r.Context(), repo, pr, run.HeadSHA, payload.Action, at); err != nil {
			observe.ActorError("webhook-sink", deliveryID, err)
			writeJSON(w, http.StatusInternalServerError, "hint_failed", "webhook: bridge hint failed")
			return
		}
	}
	observe.Event(observe.LevelInfo, "workflow_run", observe.Fields{
		"action":     payload.Action,
		"delivery":   deliveryID,
		"headSHA":    run.HeadSHA,
		"prs":        prs,
		"repo":       repo,
		"runAttempt": run.RunAttempt,
	})
	writeJSON(w, http.StatusAccepted, "accepted", "")
}

func supportedAction(action string) bool {
	switch action {
	case ActionRequested, ActionInProgress, ActionCompleted:
		return true
	default:
		return false
	}
}

type responseBody struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(responseBody{Status: status, Message: message})
}
