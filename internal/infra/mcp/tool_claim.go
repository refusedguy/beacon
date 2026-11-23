// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements the claim boundary of ADR 0032 p4: one task has exactly
// one owner at a time. A claim is atomic, a second owner cannot take the same
// task until the first releases it, and release is explicit. A claim retry that
// carries the same idempotency key (T3.23) returns the original owner instead of
// creating a second claim.
//
// The idempotency key is namespaced with claimNamespace before being stored, so
// it never shares a slot with the task-start key of ADR 0031. The raw key comes
// from the caller; the namespace is applied here, at the tool boundary.
//
// ADR 0037 p5 adds a fencing token to each claim. Every assignment, including a
// reassignment after a daemon restart, issues a monotonically higher token. A
// worker presents that token before changing a task (here: releasing the claim),
// and a token older than the one the task currently holds is refused. A lagging
// worker whose lease was reassigned cannot change the new state. The fencing
// token is a lease number, distinct from the task revision of T3.45 and from
// the event.id of ADR 0034.
//
// ADR 0038 p1 adds a source epoch on top of the fencing token. A lease is issued
// at the source epoch that was live when it was claimed. When the source is
// restored after a crash, Recover advances that epoch and every earlier lease
// becomes invalid, even when the returning worker has the same owner ID and a
// fencing token that is still current (T3.51). The worker must take a new lease
// before it can change the task again. The epoch is a separate fact from the
// lease number: the token orders leases, the epoch marks a restored source.
//
// ADR 0044 p2 adds a lease over a whole worktree. It is a different object from
// the task lease of T3.51 and ADR 0037: that token protects a reassigned task,
// this token protects the right to write into a directory until a TTL ends. The
// lease keeps an owner, an expiry and one monotonic fencing token. After the TTL
// passes, the worktree is free and the old token no longer authorizes a write,
// even if the same owner takes a new lease.
//
// ADR 0046 p1 adds a heartbeat over the worktree lease. It returns false when
// the lease was taken away, so the worker stops on the first false beat and
// does not write its result a second time. The beat is a cheap fencing check,
// not a new token: it does not renew the lease and it does not replace the
// fencing token of T3.51, which still guards the write itself. The two checks
// apply in sequence, beat first and token at the write boundary.
//
// Both stores live in memory behind one mutex each. Wiring them to the durable
// stores is a separate integration task; the tools are attached explicitly and
// are not part of the default NewServer contract set.
package mcp

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// typedTool turns a domain function into a go-sdk typed tool handler. The SDK
// parses request arguments into I and renders O as structured output.
// emptyResult picks the success result: an empty CallToolResult when true, nil
// when false. Errors keep the nil result and zero output of the old per-tool
// adapters, so error shapes do not change.
func typedTool[I, O any](fn func(context.Context, I) (O, error), emptyResult bool) func(context.Context, *mcpsdk.CallToolRequest, I) (*mcpsdk.CallToolResult, O, error) {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, in I) (*mcpsdk.CallToolResult, O, error) {
		out, err := fn(ctx, in)
		if err != nil {
			return nil, *new(O), err
		}
		if emptyResult {
			return &mcpsdk.CallToolResult{}, out, nil
		}
		return nil, out, nil
	}
}

// GenerationLedger is the generation-stamping port shared by beacon_claim and
// beacon_ack. It is declared here, in infra, so this package never imports the
// app layer (ADR 0005): the orchestrator claim model implements it and the
// composition root injects one shared instance into both handlers. IDs and
// generation tokens are plain strings, so no app type crosses the boundary.
type GenerationLedger interface {
	// IssueFor allocates a fresh generation for an owner and makes it current.
	IssueFor(ownerID string) string
	// GenerationFor returns the owner's current generation, or empty.
	GenerationFor(ownerID string) string
	// ResolveBlocked rotates the generation of a task row and returns it to
	// pending. The bool reports whether the row was live and moved.
	ResolveBlocked(taskID string) (string, bool)
}

// ClaimToolName and ReleaseToolName are the names the tools are registered
// under when AttachClaim is called. They are deliberately absent from ToolNames.
const (
	ClaimToolName   = "beacon_claim"
	ReleaseToolName = "beacon_release"
)

// claimNamespace scopes an idempotency_key that identifies a task claim. It
// keeps a claim key separate from StartNamespace (task start) and from the
// clientRequestId of ADR 0028 even when callers pass the same raw string.
const claimNamespace = "task-claim"

// ClaimInput is the argument object of beacon_claim. TaskID names the one task
// being claimed, OwnerID names the agent taking responsibility for it, and
// IdempotencyKey makes a retry of the same operation return the same claim.
type ClaimInput struct {
	TaskID         string `json:"taskID" jsonschema:"identifier of the one task being claimed"`
	OwnerID        string `json:"ownerID" jsonschema:"identifier of the agent that owns the task until release"`
	IdempotencyKey string `json:"idempotencyKey" jsonschema:"caller key that makes a claim retry idempotent"`
	// GenerationUUID pins the owner generation the claim is presented at. It
	// is the claimed_by_generation_uuid of the worker taking the task. Empty
	// keeps the pre-T3.457 contract; a value that no longer matches the owner's
	// current generation is refused so a worker whose generation rotated after
	// a blocked:resolved cannot continue (T3.457, ADR 0189).
	GenerationUUID string `json:"generationUUID,omitempty" jsonschema:"owner generation the claim is presented at; an older generation is refused; empty skips the check"`
}

// ClaimOutput is the claim held after the call. IdempotencyKey echoes the
// namespaced key produced by domain.IdempotencyKey, so a caller can trace the
// claim to the exact request that created it. FencingToken is the lease number
// of this assignment (ADR 0037 p5); the worker must present it to change the
// task, and a newer assignment makes it stale. SourceEpoch is the source
// generation the lease was issued at (ADR 0038 p1); a restored source makes the
// lease invalid even when its fencing token is still current.
type ClaimOutput struct {
	TaskID         string `json:"taskID"`
	OwnerID        string `json:"ownerID"`
	IdempotencyKey string `json:"idempotencyKey"`
	FencingToken   uint64 `json:"fencingToken"`
	SourceEpoch    uint64 `json:"sourceEpoch"`
	// GenerationUUID is the generation stamped on this claim, the
	// claimed_by_generation_uuid of the row. A blocked:resolved rotates it, so
	// a worker must present the value from a fresh claim to continue.
	GenerationUUID string `json:"generationUUID,omitempty"`
}

// ReleaseInput is the argument object of beacon_release. It names the task and
// the owner giving it up; only the current owner can release a claim.
// FencingToken is the lease number returned by the claim. Zero means the caller
// did not pin a lease, so no staleness check is made (the same convention as a
// zero revision in T3.45). A token that differs from the task's current one is
// refused, so a worker whose lease was reassigned cannot release it.
// SourceEpoch is the source generation returned by the claim; zero skips that
// check (T3.51 compatibility), and a lease from an older source epoch is
// refused even with a current fencing token (ADR 0038 p1).
type ReleaseInput struct {
	TaskID       string `json:"taskID" jsonschema:"identifier of the task being released"`
	OwnerID      string `json:"ownerID" jsonschema:"identifier of the current owner releasing the task"`
	FencingToken uint64 `json:"fencingToken,omitempty" jsonschema:"lease token from beacon_claim; an older token is refused; zero skips the check"`
	SourceEpoch  uint64 `json:"sourceEpoch,omitempty" jsonschema:"source epoch from beacon_claim; a lease from an older epoch is refused after a crash; zero skips the check"`
	// GenerationUUID is the generation from the claim being released. A
	// generation rotated away by a blocked:resolved is refused even when the
	// fencing token and source epoch still match; zero value skips the check.
	GenerationUUID string `json:"generationUUID,omitempty" jsonschema:"generationUUID from beacon_claim; an older generation is refused after a resolve; empty skips the check"`
}

// ReleaseOutput confirms which owner gave the task up.
type ReleaseOutput struct {
	TaskID  string `json:"taskID"`
	OwnerID string `json:"ownerID"`
}

// RecoveryOutput reports the source epoch installed by Recover. It is returned
// to the daemon so the recovery fact can be traced in its journal.
type RecoveryOutput struct {
	SourceEpoch uint64 `json:"sourceEpoch"`
}

// FencingTokenError reports a mutation whose fencing token is no longer the one
// the task holds. The task was reassigned to a newer lease, so the change is
// discarded before any state is touched (ADR 0037 p5).
type FencingTokenError struct {
	Presented uint64
	Current   uint64
}

func (e *FencingTokenError) Error() string {
	return fmt.Sprintf("claim: fencing token %d is stale, task was reassigned at token %d", e.Presented, e.Current)
}

// IsStaleFencingToken reports whether err is a stale fencing-token rejection.
func IsStaleFencingToken(err error) bool { return domain.IsErr[*FencingTokenError](err) }

// LeaseInvalidatedError reports a lease issued at a source epoch that has since
// been restored. The lease is invalid and the worker cannot continue the task
// with it, even if the same owner returns with a fencing token that is still
// current. It must take a new lease first (ADR 0038 p1).
type LeaseInvalidatedError struct {
	LeaseEpoch  uint64
	SourceEpoch uint64
}

func (e *LeaseInvalidatedError) Error() string {
	return fmt.Sprintf("claim: lease from source epoch %d is invalid, source restored at epoch %d; take a new lease", e.LeaseEpoch, e.SourceEpoch)
}

// IsLeaseInvalidated reports whether err is a lease-invalidation rejection.
// It is distinct from IsStaleFencingToken: the token may still be current.
func IsLeaseInvalidated(err error) bool { return domain.IsErr[*LeaseInvalidatedError](err) }

// claimRecord is one held task. Key is the namespaced idempotency key that
// created the claim; it is the identity of the operation, not of the task.
// Token is the fencing token of this lease (ADR 0037 p5). Epoch is the source
// generation that was live when the lease was issued (ADR 0038 p1).
type claimRecord struct {
	taskID  string
	ownerID string
	key     string
	token   uint64
	epoch   uint64
}

// claimStore holds the single owner of each task and the operation that claimed
// it. Both maps change under one lock, so a task can never have two owners and
// a key can never name two different claims. nextToken is a store-wide counter:
// every assignment takes the next value, so tokens are strictly monotonic even
// across release and reassignment. sourceEpoch is the generation of the live
// source; Recover advances it and every lease from an earlier epoch is invalid
// (ADR 0038 p1).
type claimStore struct {
	mu          sync.Mutex
	byTask      map[string]string      // taskID -> namespaced key of its current claim
	byKey       map[string]claimRecord // namespaced key -> claim that used it
	nextToken   uint64
	sourceEpoch uint64
}

func newClaimStore() *claimStore {
	return &claimStore{
		byTask:      make(map[string]string),
		byKey:       make(map[string]claimRecord),
		sourceEpoch: 1,
	}
}

// nextFencingToken returns the next strictly increasing lease number. It is
// called with the store lock held.
func (s *claimStore) nextFencingToken() uint64 {
	s.nextToken++
	return s.nextToken
}

// sourceRestored records that the source was restarted after a crash. It
// advances the epoch under the same lock as claims, so no lease can be issued
// at a new epoch concurrently with recovery. Fencing tokens are not touched:
// invalidation is a separate fact from the lease number of T3.51.
func (s *claimStore) sourceRestored() RecoveryOutput {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceEpoch++
	return RecoveryOutput{SourceEpoch: s.sourceEpoch}
}

// claim is the atomic boundary of ADR 0032 p4. A repeated key with the same
// task and owner returns the existing claim. Every other request for a held
// task is rejected, whether it comes from another owner or reuses a key from a
// different operation. A free task can be claimed once. A new claim issues a
// fresh fencing token at the current source epoch. A retry whose lease belongs
// to an older source epoch takes a new lease instead of reviving the invalid
// one (ADR 0038 p1); every other retry stays idempotent.
func (s *claimStore) claim(taskID, ownerID, rawKey string) (ClaimOutput, error) {
	key := domain.IdempotencyKey(claimNamespace, rawKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	if current, held := s.byTask[taskID]; held {
		rec := s.byKey[current]
		if rec.key == key && rec.ownerID == ownerID {
			if rec.epoch == s.sourceEpoch {
				return claimOutput(rec), nil
			}
			// The source was restored since this lease was taken. The retry
			// takes a new lease at the current epoch with a fresh token.
			rec = claimRecord{taskID: taskID, ownerID: ownerID, key: key, token: s.nextFencingToken(), epoch: s.sourceEpoch}
			s.byKey[key] = rec
			s.byTask[taskID] = key
			return claimOutput(rec), nil
		}
		return ClaimOutput{}, claimConflict(
			"claim: task " + strconv.Quote(taskID) + " is already owned by " + strconv.Quote(rec.ownerID))
	}
	if rec, used := s.byKey[key]; used {
		return ClaimOutput{}, claimConflict(
			"claim: idempotency key " + strconv.Quote(key) + " already owns task " + strconv.Quote(rec.taskID))
	}

	rec := claimRecord{taskID: taskID, ownerID: ownerID, key: key, token: s.nextFencingToken(), epoch: s.sourceEpoch}
	s.byKey[key] = rec
	s.byTask[taskID] = key
	return claimOutput(rec), nil
}

// reassign hands a task to a new owner and issues a fresh, strictly higher
// fencing token. It models the daemon restart of ADR 0037 p5: the old lease is
// replaced, so any token the previous owner holds becomes stale. A retry naming
// the same owner and key returns the same token, so a retried reassignment does
// not invalidate the worker it just installed. The single owner invariant is
// preserved, one task still has exactly one record. A lease from an older
// source epoch is never idempotent: reassign installs a new lease at the
// current epoch (ADR 0038 p1).
func (s *claimStore) reassign(taskID, ownerID, rawKey string) (ClaimOutput, error) {
	key := domain.IdempotencyKey(claimNamespace, rawKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	if rec, used := s.byKey[key]; used && rec.taskID != taskID {
		return ClaimOutput{}, claimConflict(
			"claim: idempotency key " + strconv.Quote(key) + " already owns task " + strconv.Quote(rec.taskID))
	}
	if current, held := s.byTask[taskID]; held {
		rec := s.byKey[current]
		if rec.key == key && rec.ownerID == ownerID && rec.epoch == s.sourceEpoch {
			return claimOutput(rec), nil
		}
		delete(s.byKey, current)
	}

	rec := claimRecord{taskID: taskID, ownerID: ownerID, key: key, token: s.nextFencingToken(), epoch: s.sourceEpoch}
	s.byKey[key] = rec
	s.byTask[taskID] = key
	return claimOutput(rec), nil
}

// release removes the claim only when the named owner holds the task, the
// presented fencing token is the one the task still holds, and the source that
// issued the lease is still live. A different owner cannot give up someone
// else's task, and a stale token cannot change a task that was reassigned since
// the caller claimed it. A lease from an epoch earlier than the current source
// epoch is invalid even with a current token (ADR 0038 p1): the worker must
// take a new lease first. Token 0 and epoch 0 skip their checks, so callers
// that pin neither keep the T3.27 and T3.51 release contract.
func (s *claimStore) release(taskID, ownerID string, token, epoch uint64) (ReleaseOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, held := s.byTask[taskID]
	if !held {
		return ReleaseOutput{}, claimConflict("claim: task " + strconv.Quote(taskID) + " is not claimed")
	}
	rec := s.byKey[key]
	if rec.epoch != s.sourceEpoch {
		return ReleaseOutput{}, &LeaseInvalidatedError{LeaseEpoch: rec.epoch, SourceEpoch: s.sourceEpoch}
	}
	if epoch != 0 && epoch != rec.epoch {
		return ReleaseOutput{}, &LeaseInvalidatedError{LeaseEpoch: epoch, SourceEpoch: s.sourceEpoch}
	}
	if token != 0 && token != rec.token {
		return ReleaseOutput{}, &FencingTokenError{Presented: token, Current: rec.token}
	}
	if rec.ownerID != ownerID {
		return ReleaseOutput{}, claimConflict(
			"claim: task " + strconv.Quote(taskID) + " is owned by " + strconv.Quote(rec.ownerID) +
				", not " + strconv.Quote(ownerID))
	}

	delete(s.byTask, taskID)
	delete(s.byKey, key)
	return ReleaseOutput{TaskID: taskID, OwnerID: ownerID}, nil
}

func claimOutput(rec claimRecord) ClaimOutput {
	return ClaimOutput{
		TaskID:         rec.taskID,
		OwnerID:        rec.ownerID,
		IdempotencyKey: rec.key,
		FencingToken:   rec.token,
		SourceEpoch:    rec.epoch,
	}
}

// claimConflict has the 409 status of other conflicts. It uses the duplicate
// code of T3.23 so callers can branch on it with domain.IsRejectDuplicate, but
// names the owner instead of claiming a task was "started" twice.
func claimConflict(message string) error {
	return &domain.AppError{
		Code:    domain.CodeRejectDuplicate,
		Message: message,
		Status:  409,
	}
}

// ClaimHandler implements the claim and release tools over one claim store. It
// performs no I/O of its own beyond the in-memory lock.
type ClaimHandler struct {
	store *claimStore
	// generations issues and checks owner generations (T3.457). It is optional
	// so a handler built only for the fencing-token contract still works; with
	// it installed every claim output carries the generation stamped for the
	// owner, and a claim presented at an older generation is refused.
	generations GenerationLedger
}

// NewClaimHandler builds a handler with a fresh, empty claim store. The
// generation ledger is injected separately so one instance can be shared with
// the ack handler and the CI bridge at the composition root.
func NewClaimHandler() *ClaimHandler {
	return &ClaimHandler{store: newClaimStore()}
}

// SetGenerationStore shares the generation ledger with a composition root. The
// same ledger is installed on the ack handler and the CI bridge so a resolve
// rotates the generation that beacon_claim stamped. A nil argument disables
// the generation check for callers that have not wired the port yet.
func (h *ClaimHandler) SetGenerationStore(g GenerationLedger) {
	if h != nil {
		h.generations = g
	}
}

// GenerationStore returns the ledger used by this handler, or nil when no
// generation store was installed. A composition root uses it to share one
// ledger across the claim, ack and bridge handlers.
func (h *ClaimHandler) GenerationStore() GenerationLedger {
	if h == nil {
		return nil
	}
	return h.generations
}

// generationFor stamps the owner's generation on a new claim. An owner that
// already has a generation keeps it, so an idempotent retry returns the same
// ClaimOutput; an owner taking the task for the first time gets a fresh one.
func (h *ClaimHandler) generationFor(ownerID string) string {
	if h == nil || h.generations == nil || ownerID == "" {
		return ""
	}
	if g := h.generations.GenerationFor(ownerID); g != "" {
		return g
	}
	return h.generations.IssueFor(ownerID)
}

// checkGeneration refuses a claim presented at a generation that is no longer
// the owner's current one. A resolve rotates that generation, so a worker that
// kept the previous ClaimOutput cannot claim or continue the task.
func (h *ClaimHandler) checkGeneration(in ClaimInput) error {
	if in.GenerationUUID == "" || h == nil || h.generations == nil {
		return nil
	}
	current := h.generations.GenerationFor(in.OwnerID)
	if current == "" || current != in.GenerationUUID {
		return domain.NewAppError(domain.CodeGone, "claim: generation is stale, the owner's generation rotated after a resolve")
	}
	return nil
}

// stampGeneration attaches the owner's generation to a claim result.
func (h *ClaimHandler) stampGeneration(out ClaimOutput, ownerID string) ClaimOutput {
	if out.GenerationUUID == "" {
		out.GenerationUUID = string(h.generationFor(ownerID))
	}
	return out
}

// Handle adapts Claim to the go-sdk typed tool handler signature.
func (h *ClaimHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in ClaimInput) (*mcpsdk.CallToolResult, ClaimOutput, error) {
	return typedTool(h.Claim, true)(ctx, req, in)
}

// HandleRelease adapts Release to the go-sdk typed tool handler signature.
func (h *ClaimHandler) HandleRelease(ctx context.Context, req *mcpsdk.CallToolRequest, in ReleaseInput) (*mcpsdk.CallToolResult, ReleaseOutput, error) {
	return typedTool(h.Release, true)(ctx, req, in)
}

// HandleReassign adapts Reassign to the go-sdk typed tool handler signature.
// It is deliberately not registered as a tool: reassignment is a daemon
// decision, not a client-facing call, and the seven default tools stay seven.
func (h *ClaimHandler) HandleReassign(ctx context.Context, req *mcpsdk.CallToolRequest, in ClaimInput) (*mcpsdk.CallToolResult, ClaimOutput, error) {
	return typedTool(h.Reassign, true)(ctx, req, in)
}

// HandleRecover adapts Recover to a daemon-facing handler. It is deliberately
// not registered as a tool: recovery is a kernel decision after a crash, not a
// client-facing call.
func (h *ClaimHandler) HandleRecover(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, RecoveryOutput, error) {
	recoverFn := func(ctx context.Context, _ struct{}) (RecoveryOutput, error) {
		return h.Recover(ctx)
	}
	return typedTool(recoverFn, true)(ctx, req, struct{}{})
}

// Claim validates the request at the boundary, then takes the atomic claim.
func (h *ClaimHandler) Claim(_ context.Context, in ClaimInput) (ClaimOutput, error) {
	if in.TaskID == "" || in.OwnerID == "" || in.IdempotencyKey == "" {
		return ClaimOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"claim: taskID, ownerID and idempotencyKey are required")
	}
	if h == nil || h.store == nil {
		return ClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, "claim: no claim store configured")
	}
	if err := h.checkGeneration(in); err != nil {
		return ClaimOutput{}, err
	}
	out, err := h.store.claim(in.TaskID, in.OwnerID, in.IdempotencyKey)
	if err != nil {
		return ClaimOutput{}, err
	}
	return h.stampGeneration(out, in.OwnerID), nil
}

// Reassign hands a task to a new owner with a higher fencing token. It models a
// daemon restart that moves a task to a fresh lease; every token issued before
// it becomes stale. It is not registered as a client tool.
func (h *ClaimHandler) Reassign(_ context.Context, in ClaimInput) (ClaimOutput, error) {
	if in.TaskID == "" || in.OwnerID == "" || in.IdempotencyKey == "" {
		return ClaimOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"claim: taskID, ownerID and idempotencyKey are required")
	}
	if h == nil || h.store == nil {
		return ClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, "claim: no claim store configured")
	}
	if err := h.checkGeneration(in); err != nil {
		return ClaimOutput{}, err
	}
	out, err := h.store.reassign(in.TaskID, in.OwnerID, in.IdempotencyKey)
	if err != nil {
		return ClaimOutput{}, err
	}
	return h.stampGeneration(out, in.OwnerID), nil
}

// Recover records that the source was restored after a crash. Every lease
// issued before this call becomes invalid: its owner cannot continue the task
// until it takes a new lease, even with a same owner ID and a fencing token
// that is still current. Fencing tokens are left untouched, so this is a
// separate fact from the lease number of T3.51 (ADR 0038 p1). It is not a
// client tool; the daemon calls it when it restores a task source.
func (h *ClaimHandler) Recover(_ context.Context) (RecoveryOutput, error) {
	if h == nil || h.store == nil {
		return RecoveryOutput{}, domain.NewAppError(domain.CodePersistFailed, "claim: no claim store configured")
	}
	return h.store.sourceRestored(), nil
}

// Release validates the request at the boundary, then frees the claim. Only the
// current owner can release, and if a fencing token is pinned it must still be
// the one the task holds. A lease from a source epoch that has since been
// restored is refused even with a current token; the worker must take a new
// lease first. The task can be claimed again afterwards.
func (h *ClaimHandler) Release(_ context.Context, in ReleaseInput) (ReleaseOutput, error) {
	if in.TaskID == "" || in.OwnerID == "" {
		return ReleaseOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"claim: taskID and ownerID are required")
	}
	if h == nil || h.store == nil {
		return ReleaseOutput{}, domain.NewAppError(domain.CodePersistFailed, "claim: no claim store configured")
	}
	if in.GenerationUUID != "" {
		current := ""
		if h.generations != nil {
			current = h.generations.GenerationFor(in.OwnerID)
		}
		if current == "" || current != in.GenerationUUID {
			return ReleaseOutput{}, domain.NewAppError(domain.CodeGone,
				"claim: generation is stale, the owner's generation rotated after a resolve")
		}
	}
	return h.store.release(in.TaskID, in.OwnerID, in.FencingToken, in.SourceEpoch)
}

// AttachClaim registers both claim tools on a server. NewServer keeps its
// contracted stub set and does not call this function; registration is a
// separate integration task. Repeating the attach leaves one of each tool.
func AttachClaim(server *mcpsdk.Server, handler *ClaimHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools(ClaimToolName, ReleaseToolName)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ClaimToolName,
		Description: "Claim one task so exactly one owner holds it. A retry with the same idempotencyKey returns the same claim and the same fencingToken; a second owner is rejected until the first calls beacon_release. Present fencingToken and sourceEpoch to beacon_release so a reassigned task or a lease invalidated after a crash cannot be changed by a stale owner; after recovery take a new lease first.",
		Annotations: attachToolAnnotations(true),
	}, handler.Handle)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ReleaseToolName,
		Description: "Explicitly release a task claim. Only the current owner can release; a fencingToken that is stale because the task was reassigned is refused, and a lease whose sourceEpoch was restored after a crash is refused even with a current token. Another owner may claim the task afterwards.",
		Annotations: attachToolAnnotations(false),
	}, handler.HandleRelease)
}

// Worktree lease tool names. They stay out of ToolNames; registration is done
// by AttachWorktreeLease and is not part of the default NewServer set.
const (
	LeaseWorktreeToolName     = "beacon_lease_worktree"
	WriteWorktreeToolName     = "beacon_write_worktree"
	ReleaseWorktreeToolName   = "beacon_release_worktree"
	HeartbeatWorktreeToolName = "beacon_heartbeat_worktree"
)

// WorktreeLeaseInput is the argument object of beacon_lease_worktree. The
// worktree is leased as a whole, so only its identifier and the owning agent
// are named. TTLSeconds is the lifetime of the lease. When it elapses the
// worktree is free without an explicit release.
type WorktreeLeaseInput struct {
	WorktreeID string `json:"worktreeID" jsonschema:"identifier of the worktree leased as a whole"`
	OwnerID    string `json:"ownerID" jsonschema:"identifier of the agent that holds the whole worktree"`
	TTLSeconds int64  `json:"ttlSeconds" jsonschema:"lease lifetime in seconds; the worktree is free once it elapses"`
}

// WorktreeLeaseOutput is the lease installed by acquire. FencingToken orders
// leases taken on this store: every acquire takes a higher token. ExpiresAt is
// the wall-clock instant at which the lease stops authorizing writes.
type WorktreeLeaseOutput struct {
	WorktreeID   string    `json:"worktreeID"`
	OwnerID      string    `json:"ownerID"`
	FencingToken uint64    `json:"fencingToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// WorktreeWriteInput is the argument object of beacon_write_worktree. The call
// records a write of the agent's result into the leased worktree. FencingToken
// must be the one the worktree currently holds and the lease must still be
// live, or the write is refused before any state changes.
type WorktreeWriteInput struct {
	WorktreeID   string `json:"worktreeID" jsonschema:"identifier of the worktree being written"`
	OwnerID      string `json:"ownerID" jsonschema:"identifier of the agent writing into the worktree"`
	FencingToken uint64 `json:"fencingToken" jsonschema:"fencing token from beacon_lease_worktree; a stale token is refused"`
}

// WorktreeWriteOutput confirms the write that was accepted.
type WorktreeWriteOutput struct {
	WorktreeID   string `json:"worktreeID"`
	OwnerID      string `json:"ownerID"`
	FencingToken uint64 `json:"fencingToken"`
}

// WorktreeReleaseInput is the argument object of beacon_release_worktree. It
// frees the lease explicitly before its TTL ends. The caller must be the owner
// and present the current token.
type WorktreeReleaseInput struct {
	WorktreeID   string `json:"worktreeID" jsonschema:"identifier of the worktree being released"`
	OwnerID      string `json:"ownerID" jsonschema:"identifier of the owner releasing the worktree"`
	FencingToken uint64 `json:"fencingToken" jsonschema:"fencing token from beacon_lease_worktree"`
}

// WorktreeReleaseOutput confirms which owner gave the worktree up.
type WorktreeReleaseOutput struct {
	WorktreeID string `json:"worktreeID"`
	OwnerID    string `json:"ownerID"`
}

// WorktreeHeartbeatInput is the argument object of beacon_heartbeat_worktree.
// It names the lease by worktree, owner and the fencing token from acquire. A
// beat is read-only: it never renews the lease TTL.
type WorktreeHeartbeatInput struct {
	WorktreeID   string `json:"worktreeID" jsonschema:"identifier of the leased worktree being checked"`
	OwnerID      string `json:"ownerID" jsonschema:"identifier of the worker beating its lease"`
	FencingToken uint64 `json:"fencingToken" jsonschema:"fencing token from beacon_lease_worktree; a stale token means the lease was taken"`
}

// WorktreeHeartbeatOutput carries the beat result. Alive is true only while the
// caller still holds the live lease; false means the lease was taken away and
// the worker must stop without writing its result again (ADR 0046 p1).
type WorktreeHeartbeatOutput struct {
	Alive bool `json:"alive"`
}

// WorktreeLeaseBusyError reports an acquire on a worktree whose lease is still
// live. A whole worktree has one holder; the caller must wait for the TTL or
// ask the current owner to release it.
type WorktreeLeaseBusyError struct {
	WorktreeID string
	OwnerID    string
	ExpiresAt  time.Time
}

func (e *WorktreeLeaseBusyError) Error() string {
	return fmt.Sprintf("worktree lease: %q is held by %q until %s", e.WorktreeID, e.OwnerID, e.ExpiresAt.Format(time.RFC3339))
}

// IsWorktreeLeaseBusy reports whether err is a busy-worktree rejection.
func IsWorktreeLeaseBusy(err error) bool { return domain.IsErr[*WorktreeLeaseBusyError](err) }

// WorktreeLeaseExpiredError reports a write or release with no live lease. An
// expired lease and a never-leased worktree both leave no authority to write.
type WorktreeLeaseExpiredError struct {
	WorktreeID string
}

func (e *WorktreeLeaseExpiredError) Error() string {
	return fmt.Sprintf("worktree lease: %q has no live lease; take a new lease before writing", e.WorktreeID)
}

// IsWorktreeLeaseExpired reports whether err is an expired-or-free rejection.
func IsWorktreeLeaseExpired(err error) bool { return domain.IsErr[*WorktreeLeaseExpiredError](err) }

// WorktreeLeaseStaleError reports a write with a token that no longer matches
// the worktree. A newer lease was taken since the caller acquired its token,
// so the caller cannot write into the directory any more.
type WorktreeLeaseStaleError struct {
	Presented uint64
	Current   uint64
}

func (e *WorktreeLeaseStaleError) Error() string {
	return fmt.Sprintf("worktree lease: fencing token %d is stale, worktree is leased at token %d", e.Presented, e.Current)
}

// IsStaleWorktreeLease reports whether err is a stale worktree-token rejection.
func IsStaleWorktreeLease(err error) bool { return domain.IsErr[*WorktreeLeaseStaleError](err) }

// WorktreeLeaseOwnerError reports a write or release by an agent that does not
// hold the worktree.
type WorktreeLeaseOwnerError struct {
	WorktreeID string
	OwnerID    string
	Holder     string
}

func (e *WorktreeLeaseOwnerError) Error() string {
	return fmt.Sprintf("worktree lease: %q is held by %q, not %q", e.WorktreeID, e.Holder, e.OwnerID)
}

// worktreeLeaseRecord is one held worktree. FencingToken is the store-wide
// lease number, and ExpiresAt is the instant after which the record grants no
// write right.
type worktreeLeaseRecord struct {
	worktreeID   string
	ownerID      string
	fencingToken uint64
	expiresAt    time.Time
}

// worktreeLeaseStore holds one lease per worktree under a single lock. The
// check-then-set of acquire and the token check of write run under that lock,
// so two agents cannot hold one worktree and a stale token cannot slip through
// a concurrent re-lease. nextToken is store-wide and strictly increasing; it
// is separate from the task fencing counter of T3.51.
type worktreeLeaseStore struct {
	mu        sync.Mutex
	byTree    map[string]worktreeLeaseRecord
	nextToken uint64
	clock     domain.Clock
}

func newWorktreeLeaseStore(clock domain.Clock) *worktreeLeaseStore {
	if clock == nil {
		clock = worktreeLeaseWallClock{}
	}
	return &worktreeLeaseStore{
		byTree: make(map[string]worktreeLeaseRecord),
		clock:  clock,
	}
}

type worktreeLeaseWallClock struct{}

func (worktreeLeaseWallClock) Now() time.Time { return time.Now() }

// activeLease returns the live record for a worktree. An expired record is
// removed on read, so an elapsed lease is treated as free. Callers hold the
// store lock.
func (s *worktreeLeaseStore) activeLease(worktreeID string) (worktreeLeaseRecord, error) {
	rec, exists := s.byTree[worktreeID]
	if !exists {
		return rec, &WorktreeLeaseExpiredError{WorktreeID: worktreeID}
	}
	if !s.clock.Now().Before(rec.expiresAt) {
		delete(s.byTree, worktreeID)
		return rec, &WorktreeLeaseExpiredError{WorktreeID: worktreeID}
	}
	return rec, nil
}

// heartbeat reports whether the caller still holds the lease. It returns a
// plain false, not an error, when the lease was taken away: the TTL elapsed, the
// lease was released, or a new owner acquired it with a higher token. A worker
// beats before continuing and stops on the first false beat, so it cannot
// finish the same work twice after its lease was reassigned (ADR 0046 p1). The
// check is cheap and read-only under the store lock; it does not extend the TTL
// and it does not change what Write accepts. The fencing token is still checked
// at the write boundary, so beat and token apply in sequence.
func (s *worktreeLeaseStore) heartbeat(worktreeID, ownerID string, token uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.activeLease(worktreeID)
	if err != nil {
		return false
	}
	return token == rec.fencingToken && ownerID == rec.ownerID
}

// acquire leases a whole worktree to one owner for ttl. A live lease blocks a
// second holder. An expired lease is replaced by a fresh one with a higher
// token, so any token the previous owner kept becomes stale.
func (s *worktreeLeaseStore) acquire(worktreeID, ownerID string, ttl time.Duration) (WorktreeLeaseOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	if rec, exists := s.byTree[worktreeID]; exists && now.Before(rec.expiresAt) {
		return WorktreeLeaseOutput{}, &WorktreeLeaseBusyError{
			WorktreeID: worktreeID,
			OwnerID:    rec.ownerID,
			ExpiresAt:  rec.expiresAt,
		}
	}

	s.nextToken++
	rec := worktreeLeaseRecord{
		worktreeID:   worktreeID,
		ownerID:      ownerID,
		fencingToken: s.nextToken,
		expiresAt:    now.Add(ttl),
	}
	s.byTree[worktreeID] = rec
	return WorktreeLeaseOutput{
		WorktreeID:   rec.worktreeID,
		OwnerID:      rec.ownerID,
		FencingToken: rec.fencingToken,
		ExpiresAt:    rec.expiresAt,
	}, nil
}

// write accepts a result only while the caller's lease is live, its token is
// the one the worktree holds, and its owner matches. An expired lease or a
// stale token leaves every record the caller cannot change untouched.
func (s *worktreeLeaseStore) write(worktreeID, ownerID string, token uint64) (WorktreeWriteOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.activeLease(worktreeID)
	if err != nil {
		return WorktreeWriteOutput{}, err
	}
	if token != rec.fencingToken {
		return WorktreeWriteOutput{}, &WorktreeLeaseStaleError{Presented: token, Current: rec.fencingToken}
	}
	if ownerID != rec.ownerID {
		return WorktreeWriteOutput{}, &WorktreeLeaseOwnerError{
			WorktreeID: worktreeID,
			OwnerID:    ownerID,
			Holder:     rec.ownerID,
		}
	}
	return WorktreeWriteOutput{WorktreeID: worktreeID, OwnerID: ownerID, FencingToken: token}, nil
}

// release frees a live lease held by the named owner with the current token.
// A late release of an elapsed lease is refused because that lease is already
// free.
func (s *worktreeLeaseStore) release(worktreeID, ownerID string, token uint64) (WorktreeReleaseOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.activeLease(worktreeID)
	if err != nil {
		return WorktreeReleaseOutput{}, err
	}
	if token != rec.fencingToken {
		return WorktreeReleaseOutput{}, &WorktreeLeaseStaleError{Presented: token, Current: rec.fencingToken}
	}
	if ownerID != rec.ownerID {
		return WorktreeReleaseOutput{}, &WorktreeLeaseOwnerError{
			WorktreeID: worktreeID,
			OwnerID:    ownerID,
			Holder:     rec.ownerID,
		}
	}
	delete(s.byTree, worktreeID)
	return WorktreeReleaseOutput{WorktreeID: worktreeID, OwnerID: ownerID}, nil
}

// WorktreeLeaseHandler serves the worktree lease tools over one lease store.
// It performs no I/O of its own beyond the in-memory lock.
type WorktreeLeaseHandler struct {
	store *worktreeLeaseStore
}

// NewWorktreeLeaseHandler builds a handler with a fresh, empty lease store. A
// nil clock falls back to wall time.
func NewWorktreeLeaseHandler(clock domain.Clock) *WorktreeLeaseHandler {
	return &WorktreeLeaseHandler{store: newWorktreeLeaseStore(clock)}
}

// HandleAcquire adapts Acquire to the go-sdk typed tool handler signature.
func (h *WorktreeLeaseHandler) HandleAcquire(ctx context.Context, req *mcpsdk.CallToolRequest, in WorktreeLeaseInput) (*mcpsdk.CallToolResult, WorktreeLeaseOutput, error) {
	return typedTool(h.Acquire, true)(ctx, req, in)
}

// HandleWrite adapts Write to the go-sdk typed tool handler signature.
func (h *WorktreeLeaseHandler) HandleWrite(ctx context.Context, req *mcpsdk.CallToolRequest, in WorktreeWriteInput) (*mcpsdk.CallToolResult, WorktreeWriteOutput, error) {
	return typedTool(h.Write, true)(ctx, req, in)
}

// HandleHeartbeat adapts Heartbeat to the go-sdk typed tool handler signature.
func (h *WorktreeLeaseHandler) HandleHeartbeat(ctx context.Context, req *mcpsdk.CallToolRequest, in WorktreeHeartbeatInput) (*mcpsdk.CallToolResult, WorktreeHeartbeatOutput, error) {
	return typedTool(h.Heartbeat, true)(ctx, req, in)
}

// HandleRelease adapts Release to the go-sdk typed tool handler signature.
func (h *WorktreeLeaseHandler) HandleRelease(ctx context.Context, req *mcpsdk.CallToolRequest, in WorktreeReleaseInput) (*mcpsdk.CallToolResult, WorktreeReleaseOutput, error) {
	return typedTool(h.Release, true)(ctx, req, in)
}

// Acquire validates the request at the boundary, then leases the whole
// worktree for the requested lifetime.
func (h *WorktreeLeaseHandler) Acquire(_ context.Context, in WorktreeLeaseInput) (WorktreeLeaseOutput, error) {
	if in.WorktreeID == "" || in.OwnerID == "" || in.TTLSeconds <= 0 {
		return WorktreeLeaseOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"worktree lease: worktreeID, ownerID and a positive ttlSeconds are required")
	}
	if h == nil || h.store == nil {
		return WorktreeLeaseOutput{}, domain.NewAppError(domain.CodePersistFailed, "worktree lease: no lease store configured")
	}
	return h.store.acquire(in.WorktreeID, in.OwnerID, time.Duration(in.TTLSeconds)*time.Second)
}

// Write validates the request at the boundary, then records a write only when
// the lease is live and the caller presents its current token.
func (h *WorktreeLeaseHandler) Write(_ context.Context, in WorktreeWriteInput) (WorktreeWriteOutput, error) {
	if in.WorktreeID == "" || in.OwnerID == "" || in.FencingToken == 0 {
		return WorktreeWriteOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"worktree lease: worktreeID, ownerID and fencingToken are required")
	}
	if h == nil || h.store == nil {
		return WorktreeWriteOutput{}, domain.NewAppError(domain.CodePersistFailed, "worktree lease: no lease store configured")
	}
	return h.store.write(in.WorktreeID, in.OwnerID, in.FencingToken)
}

// Heartbeat validates the request at the boundary, then reports whether the
// caller still holds the live lease. A lost lease is a false result, not an
// error: the worker reads the boolean and stops before writing a result again.
func (h *WorktreeLeaseHandler) Heartbeat(_ context.Context, in WorktreeHeartbeatInput) (WorktreeHeartbeatOutput, error) {
	if in.WorktreeID == "" || in.OwnerID == "" || in.FencingToken == 0 {
		return WorktreeHeartbeatOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"worktree heartbeat: worktreeID, ownerID and fencingToken are required")
	}
	if h == nil || h.store == nil {
		return WorktreeHeartbeatOutput{}, domain.NewAppError(domain.CodePersistFailed, "worktree lease: no lease store configured")
	}
	return WorktreeHeartbeatOutput{Alive: h.store.heartbeat(in.WorktreeID, in.OwnerID, in.FencingToken)}, nil
}

// Release validates the request at the boundary, then frees the lease held by
// the named owner with the current token.
func (h *WorktreeLeaseHandler) Release(_ context.Context, in WorktreeReleaseInput) (WorktreeReleaseOutput, error) {
	if in.WorktreeID == "" || in.OwnerID == "" || in.FencingToken == 0 {
		return WorktreeReleaseOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"worktree lease: worktreeID, ownerID and fencingToken are required")
	}
	if h == nil || h.store == nil {
		return WorktreeReleaseOutput{}, domain.NewAppError(domain.CodePersistFailed, "worktree lease: no lease store configured")
	}
	return h.store.release(in.WorktreeID, in.OwnerID, in.FencingToken)
}

// AttachWorktreeLease registers the four lease tools on a server, including
// the heartbeat. NewServer does not call it; repeating the attach leaves one of
// each tool.
func AttachWorktreeLease(server *mcpsdk.Server, handler *WorktreeLeaseHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools(LeaseWorktreeToolName, WriteWorktreeToolName, ReleaseWorktreeToolName, HeartbeatWorktreeToolName)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        LeaseWorktreeToolName,
		Description: "Lease a whole worktree to one owner for a TTL in seconds. The lease returns a fencing token and an expiry. A second owner is refused until the TTL ends or the owner releases it.",
		Annotations: attachToolAnnotations(false),
	}, handler.HandleAcquire)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        WriteWorktreeToolName,
		Description: "Record a result written into a leased worktree. The lease must still be live and the fencing token must be the one the worktree holds. A stale token from an expired or replaced lease is refused.",
		Annotations: attachToolAnnotations(false),
	}, handler.HandleWrite)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        HeartbeatWorktreeToolName,
		Description: "Beat a worktree lease to learn whether it is still held. The result carries a single alive flag: true while the owner and fencing token still match a live lease, false once the lease expired, was released, or was taken by a new owner. Stop on the first false beat and do not write the result again. A beat is read-only and does not extend the TTL; the fencing token is still checked when writing.",
		Annotations: attachToolAnnotations(true),
	}, handler.HandleHeartbeat)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ReleaseWorktreeToolName,
		Description: "Explicitly release a live worktree lease before its TTL ends. Only the owner holding the current fencing token can release it.",
		Annotations: attachToolAnnotations(false),
	}, handler.HandleRelease)
}

// ADR 0049 p6 adds a holder lease over a reclaimable resource: an agent or a
// worktree. Its two facts are the list of holders and the monotonic extension
// rule. A short extend never moves the deadline earlier than the one already
// granted, and the holder list survives every extend. It is a reclaim protocol,
// not a second TTL-and-fencing lease: the worktree lease of T3.97 and its
// fencing token are unchanged, and the holder lease has no write boundary of
// its own. Install and Holders are daemon-side reclaim decisions; only Extend
// is exposed as a tool, because an agent extends the lease it holds.

// ExtendHolderLeaseToolName registers the holder-side extend call. It stays out
// of ToolNames, like the other optional claim and lease tools.
const ExtendHolderLeaseToolName = "beacon_extend_holder_lease"

// HolderLeaseInput installs a holder lease on an agent or worktree resource.
// Holders is the full list of agents that hold it. TTLSeconds is the deadline
// granted at install; later extends may never shorten that grant.
type HolderLeaseInput struct {
	ResourceID string   `json:"resourceID" jsonschema:"identifier of the agent or worktree resource being leased"`
	Holders    []string `json:"holders" jsonschema:"agents that hold the resource"`
	TTLSeconds int64    `json:"ttlSeconds" jsonschema:"granted lifetime in seconds that later extends may not shorten"`
}

// HolderLeaseOutput is the lease installed. Holders is the stored list in its
// original order and ExpiresAt is the deadline the extension rule protects.
type HolderLeaseOutput struct {
	ResourceID string    `json:"resourceID"`
	Holders    []string  `json:"holders"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// HolderLeaseExtendInput asks to extend a live holder lease. HolderID must be
// one of the lease's holders. TTLSeconds is the requested lifetime measured
// from now; the new deadline is the later of the current one and now plus this
// value, so a short request leaves the granted deadline intact.
type HolderLeaseExtendInput struct {
	ResourceID string `json:"resourceID" jsonschema:"identifier of the leased resource"`
	HolderID   string `json:"holderID" jsonschema:"one of the holders asking for the extension"`
	TTLSeconds int64  `json:"ttlSeconds" jsonschema:"requested lifetime in seconds; a short request never shortens the granted deadline"`
}

// HolderLeaseExtendOutput reports the deadline after the extension and whether
// it moved. Holders is the unchanged holder list carried by the lease.
type HolderLeaseExtendOutput struct {
	ResourceID string    `json:"resourceID"`
	Holders    []string  `json:"holders"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Extended   bool      `json:"extended"`
}

// HolderLeaseHoldersInput names the resource whose holders are inspected for a
// reclaim decision.
type HolderLeaseHoldersInput struct {
	ResourceID string `json:"resourceID" jsonschema:"identifier of the leased resource"`
}

// HolderLeaseHoldersOutput is the holder list of a live lease.
type HolderLeaseHoldersOutput struct {
	ResourceID string   `json:"resourceID"`
	Holders    []string `json:"holders"`
}

// HolderLeaseNotFoundError reports an extend or inspection of a resource with
// no live holder lease. An elapsed lease is no longer extendable: the resource
// must be installed again for reclaim.
type HolderLeaseNotFoundError struct {
	ResourceID string
}

func (e *HolderLeaseNotFoundError) Error() string {
	return fmt.Sprintf("holder lease: %q has no live lease", e.ResourceID)
}

// IsHolderLeaseNotFound reports whether err is a missing-or-expired lease.
func IsHolderLeaseNotFound(err error) bool { return domain.IsErr[*HolderLeaseNotFoundError](err) }

// HolderLeaseNotHolderError reports an extension requested by an agent that is
// not in the lease's holder list.
type HolderLeaseNotHolderError struct {
	ResourceID string
	HolderID   string
}

func (e *HolderLeaseNotHolderError) Error() string {
	return fmt.Sprintf("holder lease: %q is not a holder of %q", e.HolderID, e.ResourceID)
}

// IsHolderLeaseNotHolder reports whether err is a non-holder rejection.
func IsHolderLeaseNotHolder(err error) bool { return domain.IsErr[*HolderLeaseNotHolderError](err) }

// holderLeaseRecord is one live holder lease. Holders is a private copy of the
// installed list and ExpiresAt is the deadline the monotonic rule protects.
type holderLeaseRecord struct {
	holders   []string
	expiresAt time.Time
}

// holderLeaseStore holds one holder lease per resource under a single lock. The
// active read and the extend write share that lock, so two concurrent short
// extends cannot race past each other and move the deadline backwards. It is
// separate from the T3.97 worktree lease store: neither record nor token from
// that lease appears here.
type holderLeaseStore struct {
	mu         sync.Mutex
	byResource map[string]holderLeaseRecord
	clock      domain.Clock
}

func newHolderLeaseStore(clock domain.Clock) *holderLeaseStore {
	if clock == nil {
		clock = worktreeLeaseWallClock{}
	}
	return &holderLeaseStore{
		byResource: make(map[string]holderLeaseRecord),
		clock:      clock,
	}
}

// activeHolderLease returns the live record for a resource. An expired record
// is removed on read, so an elapsed holder lease is reclaimable from scratch.
// Callers hold the store lock.
func (s *holderLeaseStore) activeHolderLease(resourceID string) (holderLeaseRecord, error) {
	rec, exists := s.byResource[resourceID]
	if !exists {
		return rec, &HolderLeaseNotFoundError{ResourceID: resourceID}
	}
	if !s.clock.Now().Before(rec.expiresAt) {
		delete(s.byResource, resourceID)
		return rec, &HolderLeaseNotFoundError{ResourceID: resourceID}
	}
	return rec, nil
}

// isHolder reports whether holderID names one of the record's holders.
func isHolder(holders []string, holderID string) bool {
	for _, holder := range holders {
		if holder == holderID {
			return true
		}
	}
	return false
}

// cloneHolders copies a holder list so callers cannot mutate the stored lease.
func cloneHolders(holders []string) []string {
	return append([]string(nil), holders...)
}

// install places a holder lease on a resource for ttl. The holders are copied
// into the record and the deadline is fixed here; later extends can only keep
// it or move it later, never earlier. A live lease is replaced so the resource
// can be reclaimed with a fresh holder set.
func (s *holderLeaseStore) install(resourceID string, holders []string, ttl time.Duration) HolderLeaseOutput {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := holderLeaseRecord{
		holders:   cloneHolders(holders),
		expiresAt: s.clock.Now().Add(ttl),
	}
	s.byResource[resourceID] = rec
	return HolderLeaseOutput{
		ResourceID: resourceID,
		Holders:    cloneHolders(rec.holders),
		ExpiresAt:  rec.expiresAt,
	}
}

// extend applies the ADR 0049 p6 monotonic rule. Only a listed holder may
// extend. The candidate deadline is now plus ttl; the stored deadline becomes
// the later of the candidate and the one already granted, so a short extension
// cannot cut the term short. The holder list is never changed by an extend.
func (s *holderLeaseStore) extend(resourceID, holderID string, ttl time.Duration) (HolderLeaseExtendOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.activeHolderLease(resourceID)
	if err != nil {
		return HolderLeaseExtendOutput{}, err
	}
	if !isHolder(rec.holders, holderID) {
		return HolderLeaseExtendOutput{}, &HolderLeaseNotHolderError{ResourceID: resourceID, HolderID: holderID}
	}

	candidate := s.clock.Now().Add(ttl)
	extended := candidate.After(rec.expiresAt)
	if extended {
		rec.expiresAt = candidate
		s.byResource[resourceID] = rec
	}
	return HolderLeaseExtendOutput{
		ResourceID: resourceID,
		Holders:    cloneHolders(rec.holders),
		ExpiresAt:  rec.expiresAt,
		Extended:   extended,
	}, nil
}

// holdersOf returns a copy of the holder list of a live lease. Reclaim uses it
// to see who currently owns the resource before installing a fresh lease.
func (s *holderLeaseStore) holdersOf(resourceID string) (HolderLeaseHoldersOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.activeHolderLease(resourceID)
	if err != nil {
		return HolderLeaseHoldersOutput{}, err
	}
	return HolderLeaseHoldersOutput{ResourceID: resourceID, Holders: cloneHolders(rec.holders)}, nil
}

// HolderLeaseHandler serves the holder lease over one store. It performs no I/O
// of its own beyond the in-memory lock.
type HolderLeaseHandler struct {
	store *holderLeaseStore
}

// NewHolderLeaseHandler builds a handler with a fresh, empty holder lease
// store. A nil clock falls back to wall time.
func NewHolderLeaseHandler(clock domain.Clock) *HolderLeaseHandler {
	return &HolderLeaseHandler{store: newHolderLeaseStore(clock)}
}

// HandleExtend adapts Extend to the go-sdk typed tool handler signature.
func (h *HolderLeaseHandler) HandleExtend(ctx context.Context, req *mcpsdk.CallToolRequest, in HolderLeaseExtendInput) (*mcpsdk.CallToolResult, HolderLeaseExtendOutput, error) {
	return typedTool(h.Extend, true)(ctx, req, in)
}

// Install validates the request at the boundary, then installs a holder lease.
// It is a daemon-side reclaim decision and is not registered as a client tool.
func (h *HolderLeaseHandler) Install(_ context.Context, in HolderLeaseInput) (HolderLeaseOutput, error) {
	if in.ResourceID == "" || len(in.Holders) == 0 || in.TTLSeconds <= 0 {
		return HolderLeaseOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"holder lease: resourceID, at least one holder and a positive ttlSeconds are required")
	}
	for _, holder := range in.Holders {
		if holder == "" {
			return HolderLeaseOutput{}, domain.NewAppError(domain.CodeInvalidInput,
				"holder lease: holders must be non-empty identifiers")
		}
	}
	if h == nil || h.store == nil {
		return HolderLeaseOutput{}, domain.NewAppError(domain.CodePersistFailed, "holder lease: no lease store configured")
	}
	return h.store.install(in.ResourceID, in.Holders, time.Duration(in.TTLSeconds)*time.Second), nil
}

// Extend validates the request at the boundary, then applies the monotonic rule.
// A short request is not an error: it is answered with the unchanged deadline
// and Extended false. Only a holder can extend the lease it shares.
func (h *HolderLeaseHandler) Extend(_ context.Context, in HolderLeaseExtendInput) (HolderLeaseExtendOutput, error) {
	if in.ResourceID == "" || in.HolderID == "" || in.TTLSeconds <= 0 {
		return HolderLeaseExtendOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"holder lease: resourceID, holderID and a positive ttlSeconds are required")
	}
	if h == nil || h.store == nil {
		return HolderLeaseExtendOutput{}, domain.NewAppError(domain.CodePersistFailed, "holder lease: no lease store configured")
	}
	return h.store.extend(in.ResourceID, in.HolderID, time.Duration(in.TTLSeconds)*time.Second)
}

// Holders validates the request at the boundary, then returns the holder list
// of a live lease. It is daemon-side and is not registered as a client tool.
func (h *HolderLeaseHandler) Holders(_ context.Context, in HolderLeaseHoldersInput) (HolderLeaseHoldersOutput, error) {
	if in.ResourceID == "" {
		return HolderLeaseHoldersOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"holder lease: resourceID is required")
	}
	if h == nil || h.store == nil {
		return HolderLeaseHoldersOutput{}, domain.NewAppError(domain.CodePersistFailed, "holder lease: no lease store configured")
	}
	return h.store.holdersOf(in.ResourceID)
}

// AttachHolderLease registers the holder-side extend tool on a server. The
// reclaim-side Install and Holders calls stay daemon-facing. NewServer does not
// call this function; repeating the attach leaves one extend tool.
func AttachHolderLease(server *mcpsdk.Server, handler *HolderLeaseHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools(ExtendHolderLeaseToolName)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ExtendHolderLeaseToolName,
		Description: "Extend a holder lease on an agent or worktree. The caller must be one of the lease holders. A short request never shortens the deadline already granted: the lease keeps the later of its current expiry and now plus ttlSeconds. The holder list is unchanged by an extension.",
		Annotations: attachToolAnnotations(false),
	}, handler.HandleExtend)
}
