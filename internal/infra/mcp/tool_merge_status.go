// Package mcp: beacon_merge_status reports why a green Watch has not merged
// and how much of its wake, rebase and retry budget it has spent.
//
// The tool is opt-in, like t3_thread_read. It stays outside toolDescriptors
// and outputSchemaFactories, so the advertised tools/list keeps its ten names.
// A composition root that wants the tool calls AttachMergeStatus.
package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// MergeStatusToolName is the MCP name of the merge status reader.
	MergeStatusToolName = "beacon_merge_status"

	mergeStatusReasonRequireApproval     = "require_approval"
	mergeStatusReasonResolverUnavailable = "resolver_unavailable"
	mergeStatusReasonWaitFor             = "wait_for:"
	mergeStatusReasonWaitForMain         = "wait_for_main:"
	mergeStatusReasonAutoMergeDisabled   = "auto_merge_disabled"
	mergeStatusReasonMergerUnavailable   = "merger_unavailable"

	// defaultMergeStatusRebaseBudget matches the project default in the bridge.
	// A Watch with MaxRebases set overrides it.
	defaultMergeStatusRebaseBudget = 3
)

// MergeStatusInput selects one Watch. Unlike beacon_get_result it needs no
// caller obligation, so an agent can inspect a Watch it has not claimed.
type MergeStatusInput struct {
	WatchID string `json:"watchID" jsonschema:"Watch to inspect; required"`
}

// MergeStatusOutput is the response of beacon_merge_status. HoldReason is
// empty only when the merge may run. The four budget fields are counters from
// persisted state, not estimates from the current process.
type MergeStatusOutput struct {
	WatchID      string `json:"watchID"`
	Verdict      string `json:"verdict"`
	AutoMerge    bool   `json:"autoMerge"`
	MergerBound  bool   `json:"mergerBound"`
	HoldReason   string `json:"holdReason,omitempty"`
	BlockerID    string `json:"blockerID,omitempty"`
	WakeUsed     int    `json:"wakeUsed"`
	WakeCap      int    `json:"wakeCap"`
	RebaseUsed   int    `json:"rebaseUsed"`
	RebaseBudget int    `json:"rebaseBudget"`
	RetryBudget  int    `json:"retryBudget"`
}

// MergeDependencyResolver answers whether one dependency reference is
// satisfied. Errors keep the hold, matching the fail-closed rule of the
// orchestrator gate. The interface has no context so MergeHold stays pure; a
// caller with a context passes a closure that captures it.
type MergeDependencyResolver interface {
	Satisfied(ref string) (bool, error)
}

// MergeDependencyResolverFunc adapts a function to MergeDependencyResolver.
type MergeDependencyResolverFunc func(ref string) (bool, error)

func (f MergeDependencyResolverFunc) Satisfied(ref string) (bool, error) {
	if f == nil {
		return false, errors.New("mcp: nil merge dependency resolver")
	}
	return f(ref)
}

// MergeHoldPolicy is the policy input of MergeHold. It carries the two axes
// the verdict applier checks before it calls its hold rule plus the three
// axes the rule itself reads.
type MergeHoldPolicy struct {
	AutoMerge       bool
	MergerBound     bool
	RequireApproval bool
	WaitFor         []string
	WaitForMain     []string
}

// MergeHold returns the reason a green verdict stays away from applyMerge,
// or an empty string when the merge may run. The four hold strings come from
// the verdict applier. autoMerge and the Merger binding are checked first so
// a Watch with no merge path never reports an empty reason.
func MergeHold(p MergeHoldPolicy, resolver MergeDependencyResolver) string {
	if p.RequireApproval {
		return mergeStatusReasonRequireApproval
	}
	if !p.AutoMerge {
		return mergeStatusReasonAutoMergeDisabled
	}
	if !p.MergerBound {
		return mergeStatusReasonMergerUnavailable
	}
	if len(p.WaitFor) == 0 && len(p.WaitForMain) == 0 {
		return ""
	}
	if resolver == nil {
		return mergeStatusReasonResolverUnavailable
	}
	for _, ref := range p.WaitFor {
		ok, err := resolver.Satisfied(ref)
		if err != nil || !ok {
			return mergeStatusReasonWaitFor + ref
		}
	}
	for _, ref := range p.WaitForMain {
		ok, err := resolver.Satisfied(ref)
		if err != nil || !ok {
			return mergeStatusReasonWaitForMain + ref
		}
	}
	return ""
}

// MergeStatusFacts is the read-only projection a MergeStatusReader returns.
// It carries the Watch and every counter the tool reports. The holder of this
// value never writes state and never calls GitHub.
type MergeStatusFacts struct {
	Watch        domain.Watch
	HoldReason   string
	WakeUsed     int
	WakeCap      int
	RebaseUsed   int
	RebaseBudget int
	RetryBudget  int
	MergerBound  bool
}

// MergeStatusReader is the read port behind beacon_merge_status. It has no
// write method, so a handler holding one cannot mutate the Watch.
type MergeStatusReader interface {
	MergeStatus(ctx context.Context, watchID string) (MergeStatusFacts, error)
}

// MergeStatusReaderFunc adapts a read function to MergeStatusReader.
type MergeStatusReaderFunc func(ctx context.Context, watchID string) (MergeStatusFacts, error)

func (f MergeStatusReaderFunc) MergeStatus(ctx context.Context, watchID string) (MergeStatusFacts, error) {
	if f == nil {
		return MergeStatusFacts{}, errors.New("mcp: nil merge status reader function")
	}
	return f(ctx, watchID)
}

// RebaseUsageReader supplies the durable rebase counter kept by the bridge.
// A reader without this port reports zero rebase attempts.
type RebaseUsageReader interface {
	RebaseAttempts(watchID string) int
}

// NewMergeStatusReader builds the store-backed read port. The returned reader
// reports a Watch even when the caller has no obligation on it. Setters bind
// the optional ports; none of them writes state.
func NewMergeStatusReader(store StateStore) *storeMergeStatusReader {
	return &storeMergeStatusReader{store: store}
}

type storeMergeStatusReader struct {
	store       StateStore
	rebase      RebaseUsageReader
	mergerBound bool
	resolver    MergeDependencyResolver
}

func (r *storeMergeStatusReader) SetRebaseUsage(src RebaseUsageReader) {
	if r != nil {
		r.rebase = src
	}
}

func (r *storeMergeStatusReader) SetMergerBound(bound bool) {
	if r != nil {
		r.mergerBound = bound
	}
}

func (r *storeMergeStatusReader) MergeStatus(_ context.Context, watchID string) (MergeStatusFacts, error) {
	if r == nil || r.store == nil {
		return MergeStatusFacts{}, domain.NewAppError(domain.CodePersistFailed, "merge_status: state store is nil")
	}
	w, err := r.store.Watch(watchID)
	if err != nil {
		return MergeStatusFacts{}, mapResultReadError(err, "merge_status: watch")
	}
	rebaseUsed := 0
	if r.rebase != nil {
		rebaseUsed = r.rebase.RebaseAttempts(w.ID)
	}
	policy := MergeHoldPolicy{
		AutoMerge:       w.MergePolicy.AutoMerge,
		MergerBound:     r.mergerBound,
		RequireApproval: w.MergePolicy.RequireApproval,
		WaitFor:         w.MergePolicy.WaitFor,
		WaitForMain:     w.MergePolicy.WaitForMain,
	}
	return MergeStatusFacts{
		Watch:        w,
		HoldReason:   MergeHold(policy, r.resolver),
		WakeUsed:     mergeStatusWakeUsed(r.store, w),
		WakeCap:      w.Policy.WakeCap(),
		RebaseUsed:   rebaseUsed,
		RebaseBudget: mergeStatusRebaseBudget(w.Policy),
		RetryBudget:  remainingRetryBudget(w, collectFailedChecks(w)),
		MergerBound:  r.mergerBound,
	}, nil
}

// mergeStatusWakeUsed reads the durable per-subscriber counters the bridge
// writes. A Watch can have several subscribers, so the largest count is the
// one that explains a park. Missing counters stay at zero.
func mergeStatusWakeUsed(store StateStore, w domain.Watch) int {
	used := 0
	for _, sub := range w.Subscribers {
		if sub.ID == "" {
			continue
		}
		id := domain.ObligationKey("wakes:"+w.ID+":"+sub.ID, 0, "count")
		o, err := store.Obligation(id)
		if err != nil {
			continue
		}
		if n := len(o.Attempts); n > used {
			used = n
		}
	}
	return used
}

func mergeStatusRebaseBudget(p domain.Policy) int {
	if p.MaxRebases > 0 {
		return p.MaxRebases
	}
	return defaultMergeStatusRebaseBudget
}

// MergeStatusHandler projects MergeStatusFacts onto the wire contract. It
// reads only. It never stamps an obligation, so a caller can retry the read.
type MergeStatusHandler struct {
	reader MergeStatusReader
}

// NewMergeStatusHandler builds the read-only handler over one status port.
func NewMergeStatusHandler(reader MergeStatusReader) *MergeStatusHandler {
	return &MergeStatusHandler{reader: reader}
}

func (h *MergeStatusHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in MergeStatusInput) (*mcpsdk.CallToolResult, MergeStatusOutput, error) {
	if strings.TrimSpace(in.WatchID) == "" {
		return nil, MergeStatusOutput{}, domain.NewAppError(domain.CodeInvalidInput, "merge_status: watchID is required")
	}
	if h == nil || h.reader == nil {
		return nil, MergeStatusOutput{}, domain.NewAppError(domain.CodePersistFailed, "merge_status: reader is nil")
	}
	facts, err := h.reader.MergeStatus(ctx, in.WatchID)
	if err != nil {
		return nil, MergeStatusOutput{}, err
	}
	w := facts.Watch
	blockerID := w.BlockerID
	if blockerID == "" && w.Blocked != nil {
		blockerID = w.Blocked.BlockedBy
	}
	return &mcpsdk.CallToolResult{}, MergeStatusOutput{
		WatchID:      w.ID,
		Verdict:      string(w.Verdict),
		AutoMerge:    w.MergePolicy.AutoMerge,
		MergerBound:  facts.MergerBound,
		HoldReason:   facts.HoldReason,
		BlockerID:    blockerID,
		WakeUsed:     facts.WakeUsed,
		WakeCap:      facts.WakeCap,
		RebaseUsed:   facts.RebaseUsed,
		RebaseBudget: facts.RebaseBudget,
		RetryBudget:  facts.RetryBudget,
	}, nil
}

// MergeStatusOutputSchema derives the schema of the opt-in tool. It is kept
// outside outputSchemaFactories so the ten advertised tools keep their pinned
// tools/list contract.
func MergeStatusOutputSchema() (*jsonschema.Schema, error) {
	return OutputSchema[MergeStatusOutput]()
}

// AttachMergeStatus registers beacon_merge_status for callers that opt in.
// A nil reader leaves the server unchanged. A nil schema falls back to the
// derived one, so a broken optional schema cannot suppress the real handler.
func AttachMergeStatus(server *mcpsdk.Server, reader MergeStatusReader, outputSchema *jsonschema.Schema) {
	if server == nil || reader == nil {
		return
	}
	if outputSchema == nil {
		derived, err := MergeStatusOutputSchema()
		if err == nil {
			outputSchema = derived
		}
		if outputSchema == nil {
			outputSchema = &jsonschema.Schema{Type: "object"}
		}
	}
	annotations := mcpsdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
	}
	server.RemoveTools(MergeStatusToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         MergeStatusToolName,
		Description:  "Report why a green Watch has not merged and how much wake, rebase and retry budget it has spent.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, NewMergeStatusHandler(reader).Handle)
}
