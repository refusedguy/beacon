// Package domain holds the Beacon entities, status enums and port interfaces.
// It imports only the standard library; adapters live in internal/infra.
package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

// Handoff is the structured transfer passed from one agent to a successor. The
// five fields from ADR 0032 §1 (T3.25) stay mandatory so the successor reads a
// fixed contract instead of the whole chat history. ADR 0046 §5 adds three
// continuation sections: MustNotRedo names actions the successor must not
// repeat, MustPreserve names state it must keep, and WorkingSet bounds the
// files and facts needed to continue. The sections sit on top of the five
// fields and never replace them.
type Handoff struct {
	Intent           string `json:"intent"`
	Evidence         string `json:"evidence"`
	PatchPlan        string `json:"patch_plan"`
	VerificationPlan string `json:"verification_plan"`
	RiskNotes        string `json:"risk_notes"`
	MustNotRedo      string `json:"must_not_redo"`
	MustPreserve     string `json:"must_preserve"`
	WorkingSet       string `json:"working_set"`
}

// Validate reports whether the handoff is complete. The five T3.25 fields are
// always required. The three ADR 0046 §5 sections follow an all-or-nothing
// rule: a legacy five-field packet declares none of them and stays valid, so
// callers built before T3.107 keep working; a continuation packet that fills
// at least one section must fill all three. Validate returns an INVALID_INPUT
// AppError naming the first empty field.
func (h Handoff) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"intent", h.Intent},
		{"evidence", h.Evidence},
		{"patch_plan", h.PatchPlan},
		{"verification_plan", h.VerificationPlan},
		{"risk_notes", h.RiskNotes},
	} {
		if strings.TrimSpace(field.value) == "" {
			return NewAppError(CodeInvalidInput, fmt.Sprintf("handoff: пустое поле %s", field.name))
		}
	}

	sections := []struct{ name, value string }{
		{"must_not_redo", h.MustNotRedo},
		{"must_preserve", h.MustPreserve},
		{"working_set", h.WorkingSet},
	}
	declared := 0
	for _, section := range sections {
		if strings.TrimSpace(section.value) != "" {
			declared++
		}
	}
	if declared > 0 && declared < len(sections) {
		for _, section := range sections {
			if strings.TrimSpace(section.value) == "" {
				return NewAppError(CodeInvalidInput, fmt.Sprintf("handoff: пустое поле %s", section.name))
			}
		}
	}
	return nil
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

// WakePayload is the minimal pointer payload a verdict source hands to a wake
// adapter. It carries identifiers only, never check bodies or PR text.
// The name differs from the WakePointer kind value in verdict.go.
type WakePayload struct {
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID"`
	Revision     uint64 `json:"revision"`
	Verdict      string `json:"verdict"`
}

// WakeDeliverer is the port a thin harness adapter implements to steer a
// session after a terminal verdict. The core never names a concrete harness.
type WakeDeliverer interface {
	Deliver(ctx context.Context, sessionID string, p WakePayload) error
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

// StreamEvent is one chunk of a Beacon event stream. Seq is a monotonic,
// 1-based position: a client restores chunk order by sorting on Seq and sees a
// gap when a position never arrives. WatchID ties the chunk to a Watch and
// Payload is an opaque preview of the result. The authoritative, complete
// result travels separately in StreamFinal, so losing a chunk never costs the
// client the full answer (ADR 0031 §1, Q 1.2 mcp-partials).
type StreamEvent struct {
	Seq     uint64 `json:"seq"`
	WatchID string `json:"watchID"`
	Payload []byte `json:"payload,omitempty"`
}

// StreamFinal is the terminal event of a Beacon stream. Chunks is the number
// of chunks the producer emitted; Missing lists the 1-based positions the
// client never received; Duplicates counts the extra copies of a position the
// client saw. Events are the delivered chunks in ascending Seq order. Result
// is the complete authoritative result, identical whether or not the chunks
// were delivered, and Streamed reports whether at least one chunk arrived
// (ADR 0031 §1).
type StreamFinal struct {
	Chunks     uint64        `json:"chunks"`
	Missing    []uint64      `json:"missing"`
	Duplicates uint64        `json:"duplicates"`
	Streamed   bool          `json:"streamed"`
	Events     []StreamEvent `json:"events,omitempty"`
	Result     []byte        `json:"result,omitempty"`
}

// defaultReorderWindow is how far ahead of a gap the assembler waits before it
// gives that position up as missing. It mirrors mcp-partials' reorderWindow.
const defaultReorderWindow uint64 = 16

// StreamAssembler restores chunk order on the client. It accepts chunks that
// arrive out of order or more than once, emits them in ascending Seq order and
// remembers which positions were never seen. Finish reports honest missing
// and duplicate counts plus the complete result, so a client that lost chunks
// still ends with the full answer.
type StreamAssembler struct {
	window     uint64
	next       uint64
	pending    map[uint64]StreamEvent
	seen       map[uint64]bool
	duplicates uint64
	missing    []uint64
	ordered    []StreamEvent
}

// NewStreamAssembler builds an assembler over the given reorder window. A
// zero window selects the default, so callers cannot build one that waits
// forever for a gap.
func NewStreamAssembler(window uint64) *StreamAssembler {
	if window == 0 {
		window = defaultReorderWindow
	}
	return &StreamAssembler{
		window:  window,
		next:    1,
		pending: make(map[uint64]StreamEvent),
		seen:    make(map[uint64]bool),
	}
}

// Push records a chunk. Seq zero is ignored because positions start at one.
// Repeating a position is counted as a duplicate and never emitted twice.
func (a *StreamAssembler) Push(ev StreamEvent) {
	if ev.Seq == 0 {
		return
	}
	if a.seen[ev.Seq] {
		a.duplicates++
		return
	}
	a.seen[ev.Seq] = true
	a.pending[ev.Seq] = ev
	a.flush()
}

// flush emits contiguous chunks and, once a gap is further ahead than the
// window, gives up on the skipped positions and continues after them.
func (a *StreamAssembler) flush() {
	for {
		if ev, ok := a.pending[a.next]; ok {
			a.ordered = append(a.ordered, ev)
			delete(a.pending, a.next)
			a.next++
			continue
		}
		if len(a.pending) == 0 {
			return
		}
		minSeq := a.minPending()
		if minSeq > a.next && minSeq-a.next > a.window {
			for s := a.next; s < minSeq; s++ {
				a.missing = append(a.missing, s)
			}
			a.next = minSeq
			continue
		}
		return
	}
}

// minPending returns the smallest buffered position. The caller only calls it
// with a non-empty buffer.
func (a *StreamAssembler) minPending() uint64 {
	var min uint64
	first := true
	for seq := range a.pending {
		if first || seq < min {
			min = seq
			first = false
		}
	}
	return min
}

// OrderedChunks returns the chunks emitted so far in ascending Seq order.
func (a *StreamAssembler) OrderedChunks() []StreamEvent {
	out := make([]StreamEvent, len(a.ordered))
	copy(out, a.ordered)
	return out
}

// Finish closes the stream. expected is the number of chunks the producer
// emitted and result is the complete authoritative result. Every position in
// 1..expected that was never seen is reported missing, buffered chunks are
// returned in ascending order, and the result is carried through unchanged.
func (a *StreamAssembler) Finish(expected uint64, result []byte) StreamFinal {
	a.flush()
	// The producer count can never be smaller than the positions already
	// emitted; take the larger value so accounting stays honest.
	if emitted := a.next - 1; expected < emitted {
		expected = emitted
	}

	reported := make(map[uint64]bool, len(a.missing))
	for _, s := range a.missing {
		reported[s] = true
	}
	for s := a.next; s <= expected; s++ {
		if !a.seen[s] && !reported[s] {
			a.missing = append(a.missing, s)
		}
	}
	sort.Slice(a.missing, func(i, j int) bool { return a.missing[i] < a.missing[j] })

	rest := make([]uint64, 0, len(a.pending))
	for seq := range a.pending {
		rest = append(rest, seq)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	ordered := make([]StreamEvent, 0, len(a.ordered)+len(rest))
	ordered = append(ordered, a.ordered...)
	for _, seq := range rest {
		ordered = append(ordered, a.pending[seq])
	}

	return StreamFinal{
		Chunks:     expected,
		Missing:    a.missing,
		Duplicates: a.duplicates,
		Streamed:   len(ordered) > 0,
		Events:     ordered,
		Result:     result,
	}
}

// ---- ADR 0042 п1 / T3.85: дескриптор задачи с дедлайном ----
//
// Дедлайн это сквозной контекст задачи, а не таймаут одного вызова и не
// бюджет ожидания из ADR 0017. Три свойства задают контракт: дескриптор идёт
// во вложенные вызовы, поэтому потомок не живёт дольше родителя; дескриптор
// сохраняется в хранилище, поэтому абсолютный дедлайн переживает рестарт;
// по истечении дедлайна задача завершается с причиной, а не уходит в вечное
// ожидание. Дедлайн хранится абсолютным instant, а не длительностью: иначе
// перезапуск процесса отсчитывал бы бюджет заново.

// TaskDescriptor is the cross-cutting context of one task. DeadlineAt is an
// absolute instant so a restarted process restores the same limit instead of
// restarting a duration. The descriptor is passed by value into every nested
// call, so a child cannot extend or mutate the parent's deadline.
type TaskDescriptor struct {
	ID         string    `json:"id"`
	DeadlineAt time.Time `json:"deadlineAt"`
}

// DeadlineReasonExceeded is the termination reason recorded when a task hits
// its hard deadline. It is not a poll tick and not a wait budget.
const DeadlineReasonExceeded = "deadline_exceeded"

// NewTaskDescriptor builds a descriptor and rejects an empty id or a zero
// deadline. Validate reports the first problem as INVALID_INPUT.
func NewTaskDescriptor(id string, deadlineAt time.Time) (TaskDescriptor, error) {
	d := TaskDescriptor{ID: strings.TrimSpace(id), DeadlineAt: deadlineAt}
	if err := d.Validate(); err != nil {
		return TaskDescriptor{}, err
	}
	return d, nil
}

// Validate reports whether the descriptor is usable: it needs an id and an
// absolute deadline. It returns INVALID_INPUT naming the first empty field.
func (d TaskDescriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return NewAppError(CodeInvalidInput, "task descriptor: пустое поле id")
	}
	if d.DeadlineAt.IsZero() {
		return NewAppError(CodeInvalidInput, "task descriptor: пустое поле deadlineAt")
	}
	return nil
}

// CheckDeadline checks the hard limit at at. When the deadline passed it
// returns expired=true and the termination reason, so the caller ends the
// task with a cause instead of waiting forever. Before the deadline it
// returns expired=false and an empty reason. A zero deadline sets no limit.
func (d TaskDescriptor) CheckDeadline(at time.Time) (bool, string) {
	if !d.DeadlineAt.IsZero() && !at.Before(d.DeadlineAt) {
		return true, DeadlineReasonExceeded
	}
	return false, ""
}

// DeadlineError builds the AppError for a task terminated on deadline expiry.
// It returns nil while the task still has time. CodeGone (410) says the task
// is gone because its hard limit passed; the message carries the reason.
func DeadlineError(d TaskDescriptor, at time.Time) error {
	expired, reason := d.CheckDeadline(at)
	if !expired {
		return nil
	}
	return NewAppError(CodeGone, fmt.Sprintf("task %s: %s", d.ID, reason))
}

// taskDescriptorKey is private so only this package can attach or read a
// descriptor, keeping the propagation contract in one place.
type taskDescriptorKey struct{}

// WithTaskDescriptor returns a context carrying d. Every nested call receives
// the same descriptor, so none of them can outlive the parent's deadline.
func WithTaskDescriptor(ctx context.Context, d TaskDescriptor) context.Context {
	return context.WithValue(ctx, taskDescriptorKey{}, d)
}

// TaskDescriptorFrom returns the descriptor carried by ctx. ok is false when
// no descriptor was attached to the context chain.
func TaskDescriptorFrom(ctx context.Context) (TaskDescriptor, bool) {
	d, ok := ctx.Value(taskDescriptorKey{}).(TaskDescriptor)
	return d, ok
}

// NestedContext derives a context for a nested call and caps its deadline by
// the parent descriptor's deadline, so the child can never outlive the parent.
// A requested deadline later than the parent's (or a zero one) is clamped to
// the parent's; an earlier one is kept. When ctx carries no descriptor the
// requested deadline is used unchanged, and a zero requested deadline yields a
// plain cancellable context. The caller must call the returned cancel func.
func NestedContext(ctx context.Context, requested time.Time) (context.Context, context.CancelFunc) {
	if d, ok := TaskDescriptorFrom(ctx); ok && !d.DeadlineAt.IsZero() {
		if requested.IsZero() || requested.After(d.DeadlineAt) {
			requested = d.DeadlineAt
		}
	}
	if requested.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, requested)
}
