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
// Ownership lives in an in-memory store guarded by one mutex. The whole
// check-then-set runs under that lock, so concurrent claims of one task have a
// single winner. Wiring this store to the durable task store is a separate
// integration task; like wake, the tools are attached explicitly and are not
// part of the default NewServer contract set.
package mcp

import (
	"context"
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
// claim to the exact request that created it.
type ClaimOutput struct {
	TaskID         string `json:"taskID"`
	OwnerID        string `json:"ownerID"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// ReleaseInput is the argument object of beacon_release. It names the task and
// the owner giving it up; only the current owner can release a claim.
type ReleaseInput struct {
	TaskID  string `json:"taskID" jsonschema:"identifier of the task being released"`
	OwnerID string `json:"ownerID" jsonschema:"identifier of the current owner releasing the task"`
}

// ReleaseOutput confirms which owner gave the task up.
type ReleaseOutput struct {
	TaskID  string `json:"taskID"`
	OwnerID string `json:"ownerID"`
}

// claimRecord is one held task. Key is the namespaced idempotency key that
// created the claim; it is the identity of the operation, not of the task.
type claimRecord struct {
	taskID  string
	ownerID string
	key     string
}

// claimStore holds the single owner of each task and the operation that claimed
// it. Both maps change under one lock, so a task can never have two owners and
// a key can never name two different claims.
type claimStore struct {
	mu     sync.Mutex
	byTask map[string]string      // taskID -> namespaced key of its current claim
	byKey  map[string]claimRecord // namespaced key -> claim that used it
}

func newClaimStore() *claimStore {
	return &claimStore{
		byTask: make(map[string]string),
		byKey:  make(map[string]claimRecord),
	}
}

// claim is the atomic boundary of ADR 0032 p4. A repeated key with the same
// task and owner returns the existing claim. Every other request for a held
// task is rejected, whether it comes from another owner or reuses a key from a
// different operation. A free task can be claimed once.
func (s *claimStore) claim(taskID, ownerID, rawKey string) (ClaimOutput, error) {
	key := domain.IdempotencyKey(claimNamespace, rawKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	if current, held := s.byTask[taskID]; held {
		rec := s.byKey[current]
		if rec.key == key && rec.ownerID == ownerID {
			return claimOutput(rec), nil
		}
		return ClaimOutput{}, claimConflict(
			"claim: task " + strconv.Quote(taskID) + " is already owned by " + strconv.Quote(rec.ownerID))
	}
	if rec, used := s.byKey[key]; used {
		return ClaimOutput{}, claimConflict(
			"claim: idempotency key " + strconv.Quote(key) + " already owns task " + strconv.Quote(rec.taskID))
	}

	rec := claimRecord{taskID: taskID, ownerID: ownerID, key: key}
	s.byKey[key] = rec
	s.byTask[taskID] = key
	return claimOutput(rec), nil
}

// release removes the claim only when the named owner holds the task. A
// different owner cannot give up someone else's task.
func (s *claimStore) release(taskID, ownerID string) (ReleaseOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, held := s.byTask[taskID]
	if !held {
		return ReleaseOutput{}, claimConflict("claim: task " + strconv.Quote(taskID) + " is not claimed")
	}
	if rec := s.byKey[key]; rec.ownerID != ownerID {
		return ReleaseOutput{}, claimConflict(
			"claim: task " + strconv.Quote(taskID) + " is owned by " + strconv.Quote(rec.ownerID) +
				", not " + strconv.Quote(ownerID))
	}

	delete(s.byTask, taskID)
	delete(s.byKey, key)
	return ReleaseOutput{TaskID: taskID, OwnerID: ownerID}, nil
}

func claimOutput(rec claimRecord) ClaimOutput {
	return ClaimOutput{TaskID: rec.taskID, OwnerID: rec.ownerID, IdempotencyKey: rec.key}
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

// Release validates the request at the boundary, then frees the claim. Only the
// current owner can release; the task can be claimed again afterwards.
func (h *ClaimHandler) Release(_ context.Context, in ReleaseInput) (ReleaseOutput, error) {
	if in.TaskID == "" || in.OwnerID == "" {
		return ReleaseOutput{}, domain.NewAppError(domain.CodeInvalidInput,
			"claim: taskID and ownerID are required")
	}
	if h == nil || h.store == nil {
		return ReleaseOutput{}, domain.NewAppError(domain.CodePersistFailed, "claim: no claim store configured")
	}
	return h.store.release(in.TaskID, in.OwnerID)
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
		Description: "Claim one task so exactly one owner holds it. A retry with the same idempotencyKey returns the same claim; a second owner is rejected until the first calls beacon_release.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, handler.Handle)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ReleaseToolName,
		Description: "Explicitly release a task claim. Only the current owner can release; another owner may claim the task afterwards.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  false,
		},
	}, handler.HandleRelease)
}
