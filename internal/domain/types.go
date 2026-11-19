// Package domain holds the Beacon entities, status enums and port interfaces.
// It imports only the standard library; adapters live in internal/infra.
package domain

import (
	"context"
	"fmt"
	"time"
)

// Status is the lifecycle state of a Watch.
type Status string

const (
	StatusWatching  Status = "watching"
	StatusNotifying Status = "notifying"
	StatusClosed    Status = "closed"
)

// Verdict is the result computed for one Watch revision.
type Verdict string

const (
	VerdictWatching    Verdict = "watching"
	VerdictReady       Verdict = "ready"
	VerdictRebase      Verdict = "rebase"
	VerdictRetry       Verdict = "retry"
	VerdictKeepWorking Verdict = "keep_working"
	VerdictTimeout     Verdict = "timeout"
	VerdictMerged      Verdict = "merged"
	VerdictClosed      Verdict = "closed"
)

// Role is the subscriber role on a Watch.
type Role string

const (
	RoleExecutor     Role = "executor"
	RoleOrchestrator Role = "orchestrator"
)

// AttemptOutcome is the observed outcome of one delivery attempt.
type AttemptOutcome string

const (
	OutcomePosted          AttemptOutcome = "posted"
	OutcomeTransportFailed AttemptOutcome = "transport-failed"
	OutcomeIndeterminate   AttemptOutcome = "indeterminate"
)

// Check is one status check attached to a Watch.
type Check struct {
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
	Required   bool   `json:"required"`
	DetailsURL string `json:"detailsUrl"`
}

// MergeInfo mirrors the merge fields read from the pull request.
type MergeInfo struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
}

// FlakyConfig holds the flaky check names and their retry budget.
type FlakyConfig struct {
	Names      []string       `json:"names"`
	MaxRetries int            `json:"maxRetries"`
	Used       map[string]int `json:"used"`
}

// Policy holds per-watch verification settings.
type Policy struct {
	RequireUpToDate bool `json:"requireUpToDate"`
	IgnoreReviews   bool `json:"ignoreReviews"`
}

// Subscriber is one party waiting for a Watch result.
type Subscriber struct {
	ID           string    `json:"id"`
	Role         Role      `json:"role"`
	Adapter      string    `json:"adapter"`
	Target       string    `json:"target"`
	AddedAt      time.Time `json:"addedAt"`
	LastActivity time.Time `json:"lastActivity"`
}

// Watch is the unit of work. Optional fields use the Go zero value when absent.
type Watch struct {
	ID            string           `json:"id"`
	Repo          string           `json:"repo"`
	PR            int              `json:"pr"`
	HeadSHA       string           `json:"headSHA"`
	BaseRef       string           `json:"baseRef"`
	Issue         string           `json:"issue"`
	Status        Status           `json:"status"`
	Verdict       Verdict          `json:"verdict"`
	Revision      uint64           `json:"revision"`
	Checks        map[string]Check `json:"checks"`
	Merge         MergeInfo        `json:"merge"`
	Flaky         FlakyConfig      `json:"flaky"`
	Policy        Policy           `json:"policy"`
	Subscribers   []Subscriber     `json:"subscribers"`
	DeadlineAt    time.Time        `json:"deadlineAt"`
	NoChecksSince time.Time        `json:"noChecksSince"`
	CreatedAt     time.Time        `json:"createdAt"`
	ClosedAt      time.Time        `json:"closedAt"`
	CloseReason   string           `json:"closeReason"`
}

// Attempt is one recorded delivery try for an Obligation.
type Attempt struct {
	N       int            `json:"n"`
	At      time.Time      `json:"at"`
	Channel string         `json:"channel"`
	Outcome AttemptOutcome `json:"outcome"`
}

// Obligation is one subscriber's claim on a Watch revision.
// ID is derived from WatchID, Revision and SubscriberID.
type Obligation struct {
	ID              string    `json:"id"`
	WatchID         string    `json:"watchID"`
	Revision        uint64    `json:"revision"`
	SubscriberID    string    `json:"subscriberID"`
	VerdictPointer  Verdict   `json:"verdictPointer"`
	StagedAt        time.Time `json:"stagedAt"`
	Attempts        []Attempt `json:"attempts"`
	DeliveredAt     time.Time `json:"deliveredAt"`
	ClaimedAt       time.Time `json:"claimedAt"`
	ReadAt          time.Time `json:"readAt"`
	EscalatedAt     time.Time `json:"escalatedAt"`
	EscalationLevel int       `json:"escalationLevel"`
}

// Pointer is the payload sent through a notification channel. It carries no
// result content, only identifiers and the revision.
type Pointer struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
}

// PRState is the normalized pull request snapshot returned by a CIProvider.
type PRState struct {
	State            string           `json:"state"`
	IsDraft          bool             `json:"isDraft"`
	HeadRefOid       string           `json:"headRefOid"`
	BaseRefName      string           `json:"baseRefName"`
	Mergeable        string           `json:"mergeable"`
	MergeStateStatus string           `json:"mergeStateStatus"`
	ReviewDecision   string           `json:"reviewDecision"`
	Checks           map[string]Check `json:"checks"`
}

// Clock supplies time to the domain. Adapters implement it in infra.
type Clock interface {
	Now() time.Time
}

// Store persists watches and obligations. The domain only names the port.
type Store interface {
	SaveWatch(w Watch) error
	Watch(id string) (Watch, error)
	ActiveWatches() ([]Watch, error)
	AllWatches() ([]Watch, error)
	SaveObligation(o Obligation) error
	Obligation(id string) (Obligation, error)
	PendingObligations() ([]Obligation, error)
}

// CIProvider reads pull request state from the hosting service.
type CIProvider interface {
	PRState(ctx context.Context, repo string, pr int) (PRState, error)
}

// Notifier delivers a wake pointer to a subscriber.
type Notifier interface {
	Notify(ctx context.Context, o Obligation, p Pointer) (AttemptOutcome, error)
}

// ErrorCode is one of the contract v2 error codes.
type ErrorCode string

const (
	CodeInvalidInput   ErrorCode = "INVALID_INPUT"
	CodeWatchNotFound  ErrorCode = "WATCH_NOT_FOUND"
	CodeHeadSHAChanged ErrorCode = "HEAD_SHA_CHANGED"
	CodeRateLimited    ErrorCode = "RATE_LIMITED"
	CodeLimitExceeded  ErrorCode = "LIMIT_EXCEEDED"
	CodeCancelled      ErrorCode = "CANCELLED"
	CodeGone           ErrorCode = "GONE"
	CodePersistFailed  ErrorCode = "PERSIST_FAILED"
)

// AppError is the shared error shape. No Result[T] type is used.
type AppError struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	Status       int       `json:"status"`
	RetryAfterMs *int64    `json:"retryAfterMs,omitempty"`
	NewWatchID   string    `json:"newWatchID,omitempty"`
}

// NewAppError builds an AppError with the status defined by the contract.
func NewAppError(code ErrorCode, message string) *AppError {
	return &AppError{Code: code, Message: message, Status: statusForCode(code)}
}

func statusForCode(code ErrorCode) int {
	switch code {
	case CodeInvalidInput:
		return 400
	case CodeWatchNotFound:
		return 404
	case CodeHeadSHAChanged:
		return 409
	case CodeRateLimited, CodeLimitExceeded:
		return 429
	case CodeCancelled, CodeGone:
		return 410
	case CodePersistFailed:
		return 503
	default:
		return 0
	}
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
