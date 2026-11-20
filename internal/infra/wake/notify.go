package wake

import (
	"context"
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

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

// WatchReader is the slice of domain.Store the notifier needs: read one Watch
// by id. A full domain.Store satisfies it.
type WatchReader interface {
	Watch(id string) (domain.Watch, error)
}

// OrchestratorNotifier holds the two ports NotifyOrchestrator uses. The
// deliverer steers a host session; the reader resolves the watch and its
// subscribers.
type OrchestratorNotifier struct {
	Deliverer domain.WakeDeliverer
	Watches   WatchReader
}

type notifierKey struct{}

// WithOrchestratorNotifier puts n into ctx so a plain NotifyOrchestrator call
// can find its ports. The same pattern carries TaskDescriptor in domain.
func WithOrchestratorNotifier(ctx context.Context, n *OrchestratorNotifier) context.Context {
	return context.WithValue(ctx, notifierKey{}, n)
}

// OrchestratorSession returns the session id of the orchestrator subscriber on
// w. Subscriber.ID is the opaque host session id, the same value cibridge
// passes to a WakeDeliverer. An empty string means there is no such subscriber.
func OrchestratorSession(w domain.Watch) string {
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator {
			return sub.ID
		}
	}
	return ""
}

// NotifyOrchestrator sends reason to the orchestrator of the watch named in p.
// The reason is trimmed and carried in the verdict field of the identifier-only
// payload, so the host sees why the executor stopped without any check body.
// The notifier and both ports must be present in ctx, and the watch must have
// an orchestrator subscriber.
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
	sessionID := OrchestratorSession(w)
	if sessionID == "" {
		return fmt.Errorf("wake: NotifyOrchestrator: no orchestrator subscriber on watch %s", p.WatchID)
	}
	msg := p
	msg.Verdict = text
	return n.Deliverer.Deliver(ctx, sessionID, msg)
}
