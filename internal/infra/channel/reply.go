// Package channel holds the pure decision logic for deterministic replies to
// an external channel message. Crush delivers a channel event into one
// session. When the model turn ends without a reply of its own, Beacon emits a
// single automatic reply back to the message origin. Routing and duplicate
// suppression here are pure: no I/O and no dependency on the transport.
package channel

// TargetKind is the value of the channel_reply target parameter. Crush marks
// a direct message with TargetUser and a group message with TargetGroup.
type TargetKind string

const (
	TargetUser  TargetKind = "user"
	TargetGroup TargetKind = "group"

	// ReplyTool is the tool a model calls to answer a channel message itself.
	// A deterministic reply is suppressed when this tool was called.
	ReplyTool = "channel_reply"
)

// TurnMessage is the channel message that opened a model turn. Target is the
// user or group identifier and becomes the reply target_param.
type TurnMessage struct {
	Kind   TargetKind
	Target string
}

// Route is the destination of a deterministic reply. It is the target_param
// the channel layer passes to its send call.
type Route struct {
	Kind   TargetKind
	Target string
}

// Reason explains an outcome without inspecting transport state.
type Reason string

const (
	ReasonReply      Reason = "reply"
	ReasonSuppressed Reason = "suppressed"
	ReasonNoTarget   Reason = "no_target"
)

// Config is the deterministic reply policy. SuppressTools names the tools
// that count as the model having answered, so the automatic reply is skipped.
type Config struct {
	SuppressTools []string
}

// DefaultConfig suppresses the channel_reply tool, which is the tool Crush
// exposes for a model-authored answer.
func DefaultConfig() Config {
	return Config{SuppressTools: []string{ReplyTool}}
}

// Turn is the observable state after a model turn for one channel message.
type Turn struct {
	Message     TurnMessage
	ToolsCalled []string
}

// Outcome is the reply decision. Route is set only when Send is true.
type Outcome struct {
	Send   bool
	Route  Route
	Reason Reason
}

// Plan decides whether to emit a deterministic reply and where to send it. It
// is deterministic: the same inputs always produce the same Outcome.
//
// A turn with no valid target cannot produce a reply. Otherwise, if the model
// called a tool listed in Config.SuppressTools, it already answered and the
// automatic reply is suppressed. In every other case the reply returns to the
// message origin.
func Plan(cfg Config, turn Turn) Outcome {
	kind, target := turn.Message.Kind, turn.Message.Target
	if !validKind(kind) || target == "" {
		return Outcome{Reason: ReasonNoTarget}
	}
	if alreadyAnswered(cfg.SuppressTools, turn.ToolsCalled) {
		return Outcome{Reason: ReasonSuppressed}
	}
	return Outcome{
		Send:   true,
		Route:  Route{Kind: kind, Target: target},
		Reason: ReasonReply,
	}
}

func validKind(k TargetKind) bool {
	return k == TargetUser || k == TargetGroup
}

// alreadyAnswered reports whether the model called any tool that suppresses
// the automatic reply. Set membership makes the result independent of call
// order and of duplicate entries.
func alreadyAnswered(suppress, called []string) bool {
	for _, tool := range called {
		for _, s := range suppress {
			if tool == s {
				return true
			}
		}
	}
	return false
}
