package core

// ControlAction is a verb accepted at a control boundary. The CLI, the
// loopback API and the MCP tool parse verbs with this type, so they cannot
// accept different lists.
type ControlAction string

const (
	ControlApprove ControlAction = "approve"
	ControlUnblock ControlAction = "unblock"
	ControlCancel  ControlAction = "cancel"
	ControlRetry   ControlAction = "retry"
	ControlNudge   ControlAction = "nudge"
	ControlWatch   ControlAction = "watch"
)

// ControlActions is the complete vocabulary in wire order. Every adapter that
// parses a verb with ParseControlAction accepts exactly these values.
var ControlActions = [...]ControlAction{
	ControlApprove,
	ControlUnblock,
	ControlCancel,
	ControlRetry,
	ControlNudge,
	ControlWatch,
}

// ParseControlAction converts a wire verb into its typed form. Unknown verbs
// keep their text so a caller can report the value it received.
func ParseControlAction(raw string) ControlAction {
	return ControlAction(raw)
}

// Valid reports whether a belongs to the control vocabulary.
func (a ControlAction) Valid() bool {
	for _, known := range ControlActions {
		if a == known {
			return true
		}
	}
	return false
}

// String returns the wire representation of a.
func (a ControlAction) String() string {
	return string(a)
}

// ControlActionNames returns the vocabulary as plain strings in a fresh slice.
func ControlActionNames() []string {
	names := make([]string, 0, len(ControlActions))
	for _, action := range ControlActions {
		names = append(names, action.String())
	}
	return names
}
