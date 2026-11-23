// Package mcp: beacon_pr_close_dup closes a duplicate pull request and may
// delete its head branch in the same call.
//
// The tool is destructive, so it never acts on the first call. Without confirm
// it returns a dry run and touches no write port. With confirm it refuses when
// a live Watch still owns the PR or when the PR is already merged. A repeat
// after the close returns noop without a second write.
//
// The tool is opt-in, like beacon_ci_failed_log. The advertised tools/list
// response has a byte-pinned contract, so an eleventh name is attached only by
// a composition root that needs duplicate cleanup. The GitHub work stays in
// that root: this package declares ports and imports no sibling infra.
package mcp

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// PRCloseDupToolName is the MCP name of the duplicate cleanup tool.
	PRCloseDupToolName = "beacon_pr_close_dup"

	// PRCloseDupActionDryRun is returned when confirm was not set.
	PRCloseDupActionDryRun = "dry_run"
	// PRCloseDupActionClosed is returned after a confirmed close.
	PRCloseDupActionClosed = "closed"
	// PRCloseDupActionNoop is returned when nothing needed closing.
	PRCloseDupActionNoop = "noop"

	// PRCloseDupReasonActiveWatch refuses a PR a Watch still owns.
	PRCloseDupReasonActiveWatch = "active_watch"
	// PRCloseDupReasonMerged refuses a merged PR.
	PRCloseDupReasonMerged = "merged"
	// PRCloseDupReasonAlreadyClosed is the idempotent repeat result.
	PRCloseDupReasonAlreadyClosed = "already_closed"
	// PRCloseDupReasonConfirmRequired explains a dry run.
	PRCloseDupReasonConfirmRequired = "confirm_required"
	// PRCloseDupReasonBranchDeleteFailed reports a close that landed but whose
	// branch deletion did not.
	PRCloseDupReasonBranchDeleteFailed = "branch_delete_failed"
)

// PRCloseDupInput selects the duplicate PR and the PR that stays open. Confirm
// turns a dry run into a real close. DeleteBranch is off by default because a
// head branch can be shared by more than one PR.
type PRCloseDupInput struct {
	Repo         string `json:"repo" jsonschema:"owner/name; required"`
	PR           int    `json:"pr" jsonschema:"duplicate pull request number; required"`
	DuplicateOf  int    `json:"duplicateOf" jsonschema:"pull request that stays open; required"`
	Confirm      bool   `json:"confirm,omitempty" jsonschema:"false returns a dry run; true performs close and branch delete"`
	DeleteBranch bool   `json:"deleteBranch,omitempty" jsonschema:"delete the head branch after closing; default false"`
}

// PRCloseDupOutput reports what the call did. Reason carries the machine
// readable cause for a dry run, a refusal or a noop. WatchID names the live
// Watch that blocked a close.
type PRCloseDupOutput struct {
	Repo          string `json:"repo"`
	PR            int    `json:"pr"`
	DuplicateOf   int    `json:"duplicateOf"`
	Action        string `json:"action"`
	BranchDeleted bool   `json:"branchDeleted"`
	Reason        string `json:"reason,omitempty"`
	WatchID       string `json:"watchID,omitempty"`
}

// PRCleanup is the destructive port behind beacon_pr_close_dup. The adapter
// lives in the composition root and reuses the existing GitHub client; this
// package never calls os/exec.
//
// HeadBranch resolves the duplicate's head ref. domain.PRState carries only
// the head SHA, so branch deletion needs one more read on the same client. The
// adapter must check that no other open PR uses the branch before deleting it.
type PRCleanup interface {
	ClosePR(ctx context.Context, repo string, pr int, reason string) error
	HeadBranch(ctx context.Context, repo string, pr int) (string, error)
	DeleteBranch(ctx context.Context, repo, branch string) error
}

// PRCloseDupHandler implements beacon_pr_close_dup over a cleanup port, the
// CIProvider and a read-only state snapshot. The seen map is a fast path for a
// repeat inside one server lifetime; the PR state read is authoritative across
// restarts.
type PRCloseDupHandler struct {
	cleanup  PRCleanup
	ci       domain.CIProvider
	snapshot SnapshotReader
	actor    string

	mu   sync.Mutex
	seen map[string]PRCloseDupOutput
}

// NewPRCloseDupHandler builds a handler. The actor is the caller label written
// to observe; a nil or empty value defaults to "agent".
func NewPRCloseDupHandler(cleanup PRCleanup, ci domain.CIProvider, snapshot SnapshotReader, actor string) *PRCloseDupHandler {
	if actor == "" {
		actor = "agent"
	}
	return &PRCloseDupHandler{
		cleanup:  cleanup,
		ci:       ci,
		snapshot: snapshot,
		actor:    actor,
		seen:     make(map[string]PRCloseDupOutput),
	}
}

// Handle adapts Close to the go-sdk typed tool handler signature.
func (h *PRCloseDupHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in PRCloseDupInput) (*mcpsdk.CallToolResult, PRCloseDupOutput, error) {
	out, err := h.Close(ctx, in)
	if err != nil {
		return nil, PRCloseDupOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// prCloseDupKey is the (repo, pr, duplicateOf) idempotency key. NUL separators
// keep a repo containing digits from colliding with the two numbers.
func prCloseDupKey(repo string, pr, duplicateOf int) string {
	return repo + "\x00" + strconv.Itoa(pr) + "\x00" + strconv.Itoa(duplicateOf)
}

func prCloseDupOutput(in PRCloseDupInput, action, reason string) PRCloseDupOutput {
	return PRCloseDupOutput{
		Repo:        in.Repo,
		PR:          in.PR,
		DuplicateOf: in.DuplicateOf,
		Action:      action,
		Reason:      reason,
	}
}

func validatePRCloseDupInput(in PRCloseDupInput) error {
	owner, name, ok := strings.Cut(in.Repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return domain.NewAppError(domain.CodeInvalidInput, "pr_close_dup: repo must be owner/name")
	}
	if in.PR <= 0 || in.DuplicateOf <= 0 {
		return domain.NewAppError(domain.CodeInvalidInput, "pr_close_dup: pr and duplicateOf must be positive")
	}
	if in.PR == in.DuplicateOf {
		return domain.NewAppError(domain.CodeInvalidInput, "pr_close_dup: pr and duplicateOf must differ")
	}
	return nil
}

// liveWatch returns the Watch that still owns this PR. A closed Watch does not
// block cleanup; every other status does. A snapshot read error is returned so
// the caller refuses the close instead of guessing that no Watch owns the PR.
func (h *PRCloseDupHandler) liveWatch(repo string, pr int) (domain.Watch, bool, error) {
	snap, err := h.snapshot.Snapshot()
	if err != nil {
		return domain.Watch{}, false, err
	}
	for _, w := range snap.RadarWatches() {
		if w.Repo == repo && w.PR == pr && w.Status != domain.StatusClosed {
			return w, true, nil
		}
	}
	return domain.Watch{}, false, nil
}

// Close runs the guarded cleanup. It returns a result rather than an error for
// the three policy outcomes (dry run, live Watch, merged or closed PR), so an
// agent can read why no write happened without parsing an error string.
func (h *PRCloseDupHandler) Close(ctx context.Context, in PRCloseDupInput) (PRCloseDupOutput, error) {
	if h == nil {
		return PRCloseDupOutput{}, domain.NewAppError(domain.CodePersistFailed, "pr_close_dup: handler is nil")
	}
	if err := validatePRCloseDupInput(in); err != nil {
		return PRCloseDupOutput{}, err
	}
	if h.cleanup == nil || h.ci == nil || h.snapshot == nil {
		return PRCloseDupOutput{}, domain.NewAppError(domain.CodePersistFailed, "pr_close_dup: ports are not configured")
	}

	key := prCloseDupKey(in.Repo, in.PR, in.DuplicateOf)
	h.mu.Lock()
	prior, seen := h.seen[key]
	h.mu.Unlock()
	if seen {
		out := prCloseDupOutput(in, PRCloseDupActionNoop, PRCloseDupReasonAlreadyClosed)
		out.BranchDeleted = prior.BranchDeleted
		return out, nil
	}

	// Refuse a PR a Watch still owns before any provider write. A failed
	// snapshot also refuses: without a reliable Watch view the guard is blind.
	w, live, err := h.liveWatch(in.Repo, in.PR)
	if err != nil {
		observe.ActorError("pr_close_dup-snapshot", in.Repo, err)
		return PRCloseDupOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "pr_close_dup: snapshot: "+err.Error(), err)
	}
	if live {
		out := prCloseDupOutput(in, PRCloseDupActionNoop, PRCloseDupReasonActiveWatch)
		out.WatchID = w.ID
		return out, nil
	}

	state, err := h.ci.PRState(ctx, in.Repo, in.PR)
	if err != nil {
		observe.ActorError("pr_close_dup-ci", in.Repo, err)
		return PRCloseDupOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "pr_close_dup: ci: "+err.Error(), err)
	}
	switch strings.ToUpper(strings.TrimSpace(state.State)) {
	case "MERGED":
		return prCloseDupOutput(in, PRCloseDupActionNoop, PRCloseDupReasonMerged), nil
	case "CLOSED":
		return prCloseDupOutput(in, PRCloseDupActionNoop, PRCloseDupReasonAlreadyClosed), nil
	}

	if !in.Confirm {
		return prCloseDupOutput(in, PRCloseDupActionDryRun, PRCloseDupReasonConfirmRequired), nil
	}

	// Resolve the head ref before closing. After the close the ref may be gone.
	branch := ""
	if in.DeleteBranch {
		branch, err = h.cleanup.HeadBranch(ctx, in.Repo, in.PR)
		if err != nil {
			observe.ActorError("pr_close_dup-head", in.Repo, err)
			return PRCloseDupOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "pr_close_dup: head branch: "+err.Error(), err)
		}
	}

	reason := "duplicate of #" + strconv.Itoa(in.DuplicateOf)
	if err := h.cleanup.ClosePR(ctx, in.Repo, in.PR, reason); err != nil {
		observe.ActorError("pr_close_dup-close", in.Repo, err)
		return PRCloseDupOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "pr_close_dup: close: "+err.Error(), err)
	}

	out := prCloseDupOutput(in, PRCloseDupActionClosed, "")
	if in.DeleteBranch && branch != "" {
		if err := h.cleanup.DeleteBranch(ctx, in.Repo, branch); err != nil {
			observe.ActorError("pr_close_dup-branch", in.Repo, err)
			out.Reason = PRCloseDupReasonBranchDeleteFailed
		} else {
			out.BranchDeleted = true
		}
	}

	// The close landed. Record it before reporting a branch failure so a retry
	// cannot close the same PR twice.
	h.mu.Lock()
	h.seen[key] = out
	h.mu.Unlock()

	observe.Event(observe.LevelInfo, "pr_close_dup", observe.Fields{
		"repo":              in.Repo,
		observe.FieldPR:     in.PR,
		"duplicateOf":       in.DuplicateOf,
		observe.FieldActor:  h.actor,
		"action":            out.Action,
		"branchDeleted":     out.BranchDeleted,
		observe.FieldReason: out.Reason,
	})
	return out, nil
}

// AttachPRCloseDup installs beacon_pr_close_dup as an opt-in destructive tool.
// It stays out of toolDescriptors so the pinned tools/list contract does not
// change; a composition root that wires PRCleanup calls this helper.
func AttachPRCloseDup(server *mcpsdk.Server, handler *PRCloseDupHandler) {
	if server == nil || handler == nil {
		return
	}
	derived, err := OutputSchema[PRCloseDupOutput]()
	if err != nil || derived == nil {
		derived = &jsonschema.Schema{Type: "object"}
	}
	annotations := mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(true)}
	server.RemoveTools(PRCloseDupToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         PRCloseDupToolName,
		Description:  "Close a duplicate pull request and optionally delete its head branch. Dry run by default; confirm performs the close.",
		OutputSchema: derived,
		Annotations:  &annotations,
	}, handler.Handle)
}
