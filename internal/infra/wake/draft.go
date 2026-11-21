package wake

import (
	"context"
	"fmt"

	"github.com/refusedguy/beacon/internal/domain"
)

// Draft-aware delivery is the Beacon side of OpenRig's DraftAwareDelivery
// (T3.244, ADR 0087): one attempt, an exact refusal reason, and the original
// held ids when the attempt is blocked. The wake is staged into the host
// composer, the composer text is checked before and after staging, and Enter
// is pressed only when the composer holds exactly the staged text. A foreign
// or late human draft blocks Enter instead of receiving the wake.
//
// There is no live pane, composer or OpenRig in this environment, so Composer
// is a port. A host layer with a real composer implements it; the tests use a
// fake. Nothing in this file touches a pane on its own.
//
// TODO(T3.244): the host composer adapter is absent here. A pane/composer
// layer must implement Composer against a real composer and opt in through
// OpenCodeDeliverer.DraftAware before draft-aware delivery can run in prod.
// Until then the default immediate PromptFunc/CLI path is unchanged.

// DraftRefusalReason is the exact, machine-readable reason a draft-aware
// attempt stopped before Enter.
type DraftRefusalReason string

const (
	// DraftReasonForeignDraft: the composer already held text before staging.
	DraftReasonForeignDraft DraftRefusalReason = "foreign_draft"
	// DraftReasonTextChanged: the composer text diverged from the staged text
	// before commit (late or changed human input).
	DraftReasonTextChanged DraftRefusalReason = "text_changed"
	// DraftReasonComposerUnavailable: the composer port could not be read or
	// written.
	DraftReasonComposerUnavailable DraftRefusalReason = "composer_unavailable"
	// DraftReasonCommitFailed: verification passed but Enter failed.
	DraftReasonCommitFailed DraftRefusalReason = "commit_failed"
)

// DraftRefusal is the typed outcome of a blocked draft-aware attempt. HeldIDs
// are the messages that were not sent, so a caller can report or release them
// verbatim. Detail is a human-readable suffix, never a verdict.
type DraftRefusal struct {
	Reason  DraftRefusalReason
	HeldIDs []string
	Detail  string
}

func (e *DraftRefusal) Error() string {
	return fmt.Sprintf("wake: draft-aware refused (%s): held %v", e.Reason, e.HeldIDs)
}

// Composer is the draft/composer port a host adapter implements. Text returns
// the current composer content. Stage inserts text without committing it.
// Commit presses Enter on the staged text. The default immediate path never
// calls this port.
type Composer interface {
	Text(ctx context.Context, sessionID string) (string, error)
	Stage(ctx context.Context, sessionID, text string) error
	Commit(ctx context.Context, sessionID string) error
}

var _ domain.WakeDeliverer = (*DraftAwareDeliverer)(nil)

// DraftAwareDeliverer stages a wake into the composer, verifies it, and only
// then commits it. It makes exactly one attempt: a refusal never restages or
// retries, so a wake can never attach to a foreign draft.
type DraftAwareDeliverer struct {
	Composer Composer
}

// Deliver implements domain.WakeDeliverer. The expected text is Envelope(p),
// the same whitelisted pointer the immediate path would send. It returns a
// *DraftRefusal when Enter must not be pressed; such a failure stays retryable
// because the caller does not mark the obligation delivered.
func (d *DraftAwareDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	held := []string{p.ObligationID}
	refuse := func(reason DraftRefusalReason, detail string) error {
		return &DraftRefusal{Reason: reason, HeldIDs: held, Detail: detail}
	}
	if d == nil || d.Composer == nil {
		return refuse(DraftReasonComposerUnavailable, "no composer port")
	}

	expected := Envelope(p)

	before, err := d.Composer.Text(ctx, sessionID)
	if err != nil {
		return refuse(DraftReasonComposerUnavailable, err.Error())
	}
	if before != "" {
		// A human draft is already present. Never put a wake into it.
		return refuse(DraftReasonForeignDraft, "composer held text before staging")
	}
	if err := d.Composer.Stage(ctx, sessionID, expected); err != nil {
		return refuse(DraftReasonComposerUnavailable, err.Error())
	}
	after, err := d.Composer.Text(ctx, sessionID)
	if err != nil {
		return refuse(DraftReasonComposerUnavailable, err.Error())
	}
	if after != expected {
		// Late or changed human input. Hold the draft, do not press Enter.
		return refuse(DraftReasonTextChanged, "composer text diverged from staged text")
	}
	if err := d.Composer.Commit(ctx, sessionID); err != nil {
		return refuse(DraftReasonCommitFailed, err.Error())
	}
	return nil
}
