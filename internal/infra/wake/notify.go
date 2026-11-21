package wake

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

// NotificationKind names the three events that need a human's attention. The
// spellings match the reason and event names emitted by the orchestrator, the
// bridge and the blocker ack path, so an operator grep finds the same word in
// the Telegram message and in the structured log.
type NotificationKind string

const (
	NotifyEscalateHuman    NotificationKind = "escalate_human"
	NotifyExecutorOrphaned NotificationKind = "executor_orphaned"
	NotifyBlockerOpened    NotificationKind = "blocker_opened"
)

// Notification is one human-attention event. WatchID, PR and Revision are
// identifiers only; Detail is the human-readable context and is redacted before
// it leaves the process.
type Notification struct {
	Kind     NotificationKind
	WatchID  string
	PR       int
	Revision uint64
	Detail   string
}

// NotificationSink is the port the attention events are delivered to. A
// Telegram sink implements it; a task's fake can record the events instead.
type NotificationSink interface {
	Notify(ctx context.Context, n Notification) error
}

// MultiNotificationSink fans one notification out to every sink. Nil sinks are
// dropped at construction so an optional transport needs no guard at each call
// site. Every sink is tried even when one fails, and the first error is returned
// so the observe decorator can log it.
type MultiNotificationSink struct {
	Sinks []NotificationSink
}

// NewMultiNotificationSink builds a fan-out sink from the given sinks.
func NewMultiNotificationSink(sinks ...NotificationSink) *MultiNotificationSink {
	all := make([]NotificationSink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			all = append(all, s)
		}
	}
	return &MultiNotificationSink{Sinks: all}
}

// Notify delivers n to each sink in order.
func (m *MultiNotificationSink) Notify(ctx context.Context, n Notification) error {
	if m == nil {
		return nil
	}
	var firstErr error
	for _, sink := range m.Sinks {
		if sink == nil {
			continue
		}
		if err := sink.Notify(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// FormatNotification renders one notification as plain text. It is a pure
// function: redaction and transport belong to the adapter, not to the shape of
// the message.
func FormatNotification(n Notification) string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(string(n.Kind))
	b.WriteString("]")
	if n.WatchID != "" {
		b.WriteString(" watch=")
		b.WriteString(n.WatchID)
	}
	if n.PR != 0 {
		b.WriteString(" pr=")
		b.WriteString(strconv.Itoa(n.PR))
	}
	if n.Revision != 0 {
		b.WriteString(" revision=")
		b.WriteString(strconv.FormatUint(n.Revision, 10))
	}
	if detail := strings.TrimSpace(n.Detail); detail != "" {
		b.WriteString("\n")
		b.WriteString(detail)
	}
	return b.String()
}

// TriageClass is the executor's conclusion after it compares failedChecks with
// its own diff (red-verdict-triage skill).
type TriageClass string

const (
	TriageMainBroken TriageClass = "main_broken"
	TriageMyFault    TriageClass = "my_fault"
	TriageUnclear    TriageClass = "unclear"
)

// The three reasons the red-verdict-triage skill tells the executor to send.
const (
	ReasonMainBroken = "братан иди чини main"
	ReasonMyFault    = "моя вина, чиню"
	ReasonUnclear    = "проверил, изменений моих нет"
)

// ClassifyReason trims raw whitespace and maps the reason text to a triage
// class. An empty or unrecognised reason is unclear, never an error.
func ClassifyReason(raw string) TriageClass {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return TriageUnclear
	}
	switch {
	case strings.Contains(s, "чини main"),
		strings.Contains(s, "broken main"),
		strings.Contains(s, "флейк"),
		strings.Contains(s, "flake"),
		strings.Contains(s, "сломан"):
		return TriageMainBroken
	case strings.Contains(s, "моя вина"),
		strings.Contains(s, "чиню"),
		strings.Contains(s, "my fault"):
		return TriageMyFault
	default:
		return TriageUnclear
	}
}

// ReasonFor returns the canonical message for c. Unknown classes are unclear.
func ReasonFor(c TriageClass) string {
	switch c {
	case TriageMainBroken:
		return ReasonMainBroken
	case TriageMyFault:
		return ReasonMyFault
	default:
		return ReasonUnclear
	}
}

// WatchReader is the slice of domain.WatchStore the notifier needs: read one
// Watch by id. A full WatchStore satisfies it.
type WatchReader interface {
	Watch(id string) (domain.Watch, error)
}

// ConfigOrchestrator is the project-level fallback session id for a Watch that
// names no orchestrator of its own. The config file belongs to the app layer,
// so wake receives the value through this port instead of importing it.
type ConfigOrchestrator func() string

// OrchestratorNotifier holds the ports NotifyOrchestrator uses. The deliverer
// steers a host session; the reader resolves the Watch and its subscribers.
// ConfigOrchestrator is consulted only when the Watch itself names no
// orchestrator: neither a subscriber with role orchestrator nor the persisted
// OrchestratorID field.
type OrchestratorNotifier struct {
	Deliverer          domain.WakeDeliverer
	Watches            WatchReader
	ConfigOrchestrator ConfigOrchestrator
}

// WithConfigOrchestrator binds the project-level fallback. A nil argument
// leaves the notifier without one, so a Watch that names no orchestrator is
// refused instead of steering an arbitrary session.
func (n *OrchestratorNotifier) WithConfigOrchestrator(fn ConfigOrchestrator) *OrchestratorNotifier {
	if n != nil {
		n.ConfigOrchestrator = fn
	}
	return n
}

type notifierKey struct{}

// WithOrchestratorNotifier puts n into ctx so a plain NotifyOrchestrator call
// can find its ports. The same pattern carries TaskDescriptor in domain.
func WithOrchestratorNotifier(ctx context.Context, n *OrchestratorNotifier) context.Context {
	return context.WithValue(ctx, notifierKey{}, n)
}

// OrchestratorSession returns the session id of the orchestrator on w. It
// prefers the subscriber with role orchestrator and then the persisted
// OrchestratorID field. Subscriber.ID and OrchestratorID are opaque host
// session ids, the same value a WakeDeliverer accepts. An empty result means w
// names no orchestrator; a caller with a config fallback may then use it. An
// executor subscriber never qualifies, even when it is the only subscriber.
func OrchestratorSession(w domain.Watch) string {
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator && strings.TrimSpace(sub.ID) != "" {
			return strings.TrimSpace(sub.ID)
		}
	}
	return strings.TrimSpace(w.OrchestratorID)
}

// isExecutorSession reports whether id belongs to an executor subscriber of w.
// It guards against a stale or wrong OrchestratorID that would otherwise steer
// the blocked notice back to the executor that reported the failed checks.
func isExecutorSession(w domain.Watch, id string) bool {
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleExecutor && sub.ID == id {
			return true
		}
	}
	return false
}

// ResolveOrchestratorSession returns the one session to steer for w. The order
// is the orchestrator subscriber, the Watch's OrchestratorID, then the
// configured fallback. A candidate that also names an executor is skipped.
func (n *OrchestratorNotifier) ResolveOrchestratorSession(w domain.Watch) string {
	if id := OrchestratorSession(w); id != "" && !isExecutorSession(w, id) {
		return id
	}
	if n != nil && n.ConfigOrchestrator != nil {
		if id := strings.TrimSpace(n.ConfigOrchestrator()); id != "" && !isExecutorSession(w, id) {
			return id
		}
	}
	return ""
}

// NotifyOrchestrator sends reason to the orchestrator of the watch named in p.
// The reason is trimmed and carried in the verdict field of the identifier-only
// payload, so the host sees why the executor stopped without any check body.
// The notifier and both ports must be present in ctx. The target is resolved
// from the orchestrator subscriber, Watch.OrchestratorID or the configured
// fallback, in that order.
func NotifyOrchestrator(ctx context.Context, reason string, p domain.WakePayload) error {
	n, ok := ctx.Value(notifierKey{}).(*OrchestratorNotifier)
	if !ok || n == nil {
		return fmt.Errorf("wake: NotifyOrchestrator: notifier not configured in context")
	}
	if n.Deliverer == nil || n.Watches == nil {
		return fmt.Errorf("wake: NotifyOrchestrator: ports not configured")
	}
	text := strings.TrimSpace(reason)
	if text == "" {
		return fmt.Errorf("wake: NotifyOrchestrator: empty reason")
	}
	w, err := n.Watches.Watch(p.WatchID)
	if err != nil {
		return fmt.Errorf("wake: NotifyOrchestrator: watch %s: %w", p.WatchID, err)
	}
	sessionID := n.ResolveOrchestratorSession(w)
	if sessionID == "" {
		return fmt.Errorf("wake: NotifyOrchestrator: no orchestrator subscriber, Watch.OrchestratorID or configured orchestrator on watch %s", p.WatchID)
	}
	msg := p
	msg.Verdict = text
	return n.Deliverer.Deliver(ctx, sessionID, msg)
}
