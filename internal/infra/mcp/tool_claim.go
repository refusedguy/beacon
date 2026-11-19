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
// Ownership lives in an in-memory store guarded by one mutex. The whole
// check-then-set runs under that lock, so concurrent claims of one task have a
// single winner. Wiring this store to the durable task store is a separate
// integration task; like wake, the tools are attached explicitly and are not
// part of the default NewServer contract set.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

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
func IsStaleFencingToken(err error) bool {
	var stale *FencingTokenError
	return errors.As(err, &stale)
}

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
func IsLeaseInvalidated(err error) bool {
	var invalid *LeaseInvalidatedError
	return errors.As(err, &invalid)
}

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
}

// NewClaimHandler builds a handler with a fresh, empty claim store.
func NewClaimHandler() *ClaimHandler {
	return &ClaimHandler{store: newClaimStore()}
}

// Handle adapts Claim to the go-sdk typed tool handler signature.
func (h *ClaimHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in ClaimInput) (*mcpsdk.CallToolResult, ClaimOutput, error) {
	out, err := h.Claim(ctx, in)
	if err != nil {
		return nil, ClaimOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// HandleRelease adapts Release to the go-sdk typed tool handler signature.
func (h *ClaimHandler) HandleRelease(ctx context.Context, _ *mcpsdk.CallToolRequest, in ReleaseInput) (*mcpsdk.CallToolResult, ReleaseOutput, error) {
	out, err := h.Release(ctx, in)
	if err != nil {
		return nil, ReleaseOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// HandleReassign adapts Reassign to the go-sdk typed tool handler signature.
// It is deliberately not registered as a tool: reassignment is a daemon
// decision, not a client-facing call, and the seven default tools stay seven.
func (h *ClaimHandler) HandleReassign(ctx context.Context, _ *mcpsdk.CallToolRequest, in ClaimInput) (*mcpsdk.CallToolResult, ClaimOutput, error) {
	out, err := h.Reassign(ctx, in)
	if err != nil {
		return nil, ClaimOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// HandleRecover adapts Recover to a daemon-facing handler. It is deliberately
// not registered as a tool: recovery is a kernel decision after a crash, not a
// client-facing call.
func (h *ClaimHandler) HandleRecover(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, RecoveryOutput, error) {
	out, err := h.Recover(ctx)
	if err != nil {
		return nil, RecoveryOutput{}, err
	}
	return &mcpsdk.CallToolResult{}, out, nil
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
	return h.store.claim(in.TaskID, in.OwnerID, in.IdempotencyKey)
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
	return h.store.reassign(in.TaskID, in.OwnerID, in.IdempotencyKey)
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
	return h.store.release(in.TaskID, in.OwnerID, in.FencingToken, in.SourceEpoch)
}

// AttachClaim registers both claim tools on a server. NewServer keeps its
// contracted stub set and does not call this function; registration is a
// separate integration task. Repeating the attach leaves one of each tool.
func AttachClaim(server *mcpsdk.Server, handler *ClaimHandler) {
	if server == nil || handler == nil {
		return
	}
	no := false
	server.RemoveTools(ClaimToolName, ReleaseToolName)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ClaimToolName,
		Description: "Claim one task so exactly one owner holds it. A retry with the same idempotencyKey returns the same claim and the same fencingToken; a second owner is rejected until the first calls beacon_release. Present fencingToken and sourceEpoch to beacon_release so a reassigned task or a lease invalidated after a crash cannot be changed by a stale owner; after recovery take a new lease first.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, handler.Handle)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ReleaseToolName,
		Description: "Explicitly release a task claim. Only the current owner can release; a fencingToken that is stale because the task was reassigned is refused, and a lease whose sourceEpoch was restored after a crash is refused even with a current token. Another owner may claim the task afterwards.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  false,
		},
	}, handler.HandleRelease)
}
