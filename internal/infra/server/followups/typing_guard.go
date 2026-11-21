// Package followups holds the pure typing-guard model for automatic wake
// delivery. It decides what to do with an automatic message when a human is
// typing in the target composer, and never probes a terminal, opens a session
// or writes a message. Delivery stays in the adapter (ADR 0082, 0086, 0094).
package followups

import (
	"errors"
	"fmt"
)

// Mode is the typing guard policy for one session. The zero value is ModeOff,
// the default: automatic wake delivers as if no human were typing.
type Mode string

const (
	ModeOff        Mode = "off"
	ModeHold       Mode = "hold"
	ModeDraftAware Mode = "draft-aware"
	ModeQueueOnly  Mode = "queue-only"
)

// ErrUnknownMode reports a mode value that is not one of the four policies.
// Rejecting a typo at the boundary keeps a misspelled guard from silently
// turning itself off.
var ErrUnknownMode = errors.New("followups: unknown typing guard mode")

// ParseMode validates a configured mode string. Empty means the default, off.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", string(ModeOff):
		return ModeOff, nil
	case string(ModeHold):
		return ModeHold, nil
	case string(ModeDraftAware):
		return ModeDraftAware, nil
	case string(ModeQueueOnly):
		return ModeQueueOnly, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownMode, s)
	}
}

// Action is what the deliverer must do with an automatic message at a typing
// event. It is a decision, not an I/O call: the caller performs the action.
type Action string

const (
	// ActionDeliver sends the message through the normal steer path.
	ActionDeliver Action = "deliver"
	// ActionHold keeps the message out of the composer; Enter is not pressed.
	ActionHold Action = "hold"
	// ActionQueue parks the message in the queue without interrupting.
	ActionQueue Action = "queue"
)

// Signal is the observable state of the human composer when an automatic wake
// wants to deliver. The guard only reads these fields.
type Signal struct {
	// Known is false when the typing state could not be observed. Hold and
	// draft-aware fail safe then: an unobserved composer is treated as occupied
	// so the message is held instead of typed into human text.
	Known bool
	// Typing reports whether the human is actively entering text.
	Typing bool
	// Draft is the current composer content. A non-empty draft means human text
	// is present and must not be overwritten or appended to.
	Draft string
}

// ChooseAction decides the delivery action for mode at a typing event. The
// function is pure: a mode and a signal always give the same action, and it
// touches no store, terminal or network.
//
// off always delivers. hold holds whenever the human is typing or the state is
// unknown, which is the legacy boolean fail safe. draft-aware holds only when
// there is real draft content and fails safe to hold when the state is unknown.
// queue-only parks in the queue when the human is typing or the state is
// unknown; a parked message is never lost. An unrecognised mode is treated as
// hold, the safe answer when a policy cannot be interpreted.
func ChooseAction(mode Mode, sig Signal) Action {
	switch mode {
	case ModeOff:
		return ActionDeliver
	case ModeHold:
		if sig.Typing || !sig.Known {
			return ActionHold
		}
		return ActionDeliver
	case ModeDraftAware:
		if !sig.Known || sig.Draft != "" {
			return ActionHold
		}
		return ActionDeliver
	case ModeQueueOnly:
		if sig.Typing || !sig.Known {
			return ActionQueue
		}
		return ActionDeliver
	default:
		return ActionHold
	}
}

// AuditKind names an action recorded in the guard audit log.
type AuditKind string

const (
	AuditSetMode AuditKind = "set-mode"
	AuditHold    AuditKind = "hold"
	AuditRelease AuditKind = "release"
)

// AuditEvent is one immutable entry in the guard audit log.
type AuditEvent struct {
	Kind      AuditKind
	Session   string
	MessageID string
	From      Mode
	To        Mode
}

// HeldMessage is an automatic wake message kept out of a human composer.
// Release returns it so a caller can deliver it after the human stops typing.
type HeldMessage struct {
	ID      string
	Session string
	Text    string
	Mode    Mode
}

// Guard is the in-memory typing guard model: configured modes, held messages
// and an audit log. It is pure state with no file, terminal or network I/O.
type Guard struct {
	modes  map[string]Mode
	held   map[string]HeldMessage
	order  []string
	audit  []AuditEvent
	nextID int
}

// NewGuard returns an empty guard. Sessions without an override use ModeOff.
func NewGuard() *Guard {
	return &Guard{
		modes: map[string]Mode{},
		held:  map[string]HeldMessage{},
	}
}

// SetMode sets the guard policy for a session and records the change. An
// invalid mode returns ErrUnknownMode without changing state.
func (g *Guard) SetMode(session string, mode Mode) error {
	switch mode {
	case ModeOff, ModeHold, ModeDraftAware, ModeQueueOnly:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownMode, mode)
	}
	from := g.Mode(session)
	g.modes[session] = mode
	g.audit = append(g.audit, AuditEvent{Kind: AuditSetMode, Session: session, From: from, To: mode})
	return nil
}

// Mode returns the configured policy, or off for a session with no override.
func (g *Guard) Mode(session string) Mode {
	if m, ok := g.modes[session]; ok {
		return m
	}
	return ModeOff
}

// Plan returns the action for a session at a typing event using its configured
// mode. A session with no override is off by default.
func (g *Guard) Plan(session string, sig Signal) Action {
	return ChooseAction(g.Mode(session), sig)
}

// Hold stores a message out of the way and returns it with a generated id. The
// text is never written to a terminal by this package.
func (g *Guard) Hold(session, text string, mode Mode) HeldMessage {
	g.nextID++
	msg := HeldMessage{
		ID:      fmt.Sprintf("held-%d", g.nextID),
		Session: session,
		Text:    text,
		Mode:    mode,
	}
	g.held[msg.ID] = msg
	g.order = append(g.order, msg.ID)
	g.audit = append(g.audit, AuditEvent{Kind: AuditHold, Session: session, MessageID: msg.ID, To: mode})
	return msg
}

// View lists held messages for a session in hold order.
func (g *Guard) View(session string) []HeldMessage {
	var out []HeldMessage
	for _, id := range g.order {
		if m, ok := g.held[id]; ok && m.Session == session {
			out = append(out, m)
		}
	}
	return out
}

// Release removes a held message and returns it so a caller can deliver it. An
// unknown id leaves the guard unchanged and returns an error.
func (g *Guard) Release(id string) (HeldMessage, error) {
	msg, ok := g.held[id]
	if !ok {
		return HeldMessage{}, fmt.Errorf("followups: held message %q not found", id)
	}
	delete(g.held, id)
	g.audit = append(g.audit, AuditEvent{Kind: AuditRelease, Session: msg.Session, MessageID: id, From: msg.Mode})
	return msg, nil
}

// Audit returns a copy of the audit log so callers cannot mutate guard state.
func (g *Guard) Audit() []AuditEvent {
	out := make([]AuditEvent, len(g.audit))
	copy(out, g.audit)
	return out
}
