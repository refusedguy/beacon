// Package domain holds the Beacon entities, status enums and port interfaces.
// This file gives attention a typed reason instead of a single boolean flag.
// It is pure: the classifier reads only its argument and performs no I/O.
package domain

import "strings"

// AttentionLevel says whether someone must act on a Watch. A zero level means
// no known trigger was observed. It does not prove the Watch is clear, because
// an unrecognised source has no typed reason yet.
type AttentionLevel string

const (
	AttentionNone   AttentionLevel = ""
	AttentionAction AttentionLevel = "action"
)

// AttentionReason names a trigger that blocks a Watch. The four values mirror
// the verdicts that already require a retry or a rebase. The codes proposed in
// T3.263 (trust_gate, login_required, attention_required) stay out of this set:
// T3.263 is dropped and PR #394 closed that direction.
type AttentionReason string

const (
	AttentionRetry       AttentionReason = "retry"
	AttentionRebase      AttentionReason = "rebase"
	AttentionKeepWorking AttentionReason = "keep_working"
	AttentionTimeout     AttentionReason = "timeout"
)

// Attention is one classified reason a Watch needs attention, together with
// the next action the responder should take. The zero value means no known
// trigger, not proof that nothing is wrong.
type Attention struct {
	Level      AttentionLevel  `json:"level"`
	Reason     AttentionReason `json:"reason"`
	NextAction string          `json:"nextAction,omitempty"`
}

// NeedsAction reports whether the Watch needs an agent to act. The check is on
// the level, so an unknown reason never turns into an actionable item by
// accident.
func (a Attention) NeedsAction() bool {
	return a.Level == AttentionAction
}

// Compact returns a one-character code for the reason. An unclassified
// Attention returns "?", so an unknown source stays visible in compact output
// instead of looking clear.
func (a Attention) Compact() string {
	if !a.NeedsAction() {
		return "?"
	}
	switch a.Reason {
	case AttentionRetry:
		return "r"
	case AttentionRebase:
		return "b"
	case AttentionKeepWorking:
		return "k"
	case AttentionTimeout:
		return "t"
	}
	return "?"
}

// AttentionForVerdict classifies a verdict token. It folds case and
// surrounding space so verdicts from different producers land on the same
// reason. Ready, watching and unknown tokens return the zero value: no known
// trigger, not proof that the Watch is clear.
func AttentionForVerdict(verdict string) Attention {
	switch AttentionReason(strings.ToLower(strings.TrimSpace(verdict))) {
	case AttentionRetry:
		return Attention{Level: AttentionAction, Reason: AttentionRetry, NextAction: "rerun the failed checks"}
	case AttentionRebase:
		return Attention{Level: AttentionAction, Reason: AttentionRebase, NextAction: "rebase onto origin/main"}
	case AttentionKeepWorking:
		return Attention{Level: AttentionAction, Reason: AttentionKeepWorking, NextAction: "resume the failed checks"}
	case AttentionTimeout:
		return Attention{Level: AttentionAction, Reason: AttentionTimeout, NextAction: "inspect the Watch and decide whether to continue"}
	}
	return Attention{}
}
