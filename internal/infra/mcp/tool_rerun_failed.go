// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_rerun_failed (AT-06): it re-runs the failed
// checks of a Watch through MCP while keeping the flaky retry budget and the
// (watchID, revision, runID) idempotency key. A zero budget returns
// CodeLimitExceeded before the provider is called. Only checks that matched
// the flaky set are eligible. The write path stays behind RerunPort, so this
// layer holds no os/exec and imports no sibling infra package.
package mcp

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/shared"
)

// RerunFailedToolName is the MCP name of the rerun tool.
const RerunFailedToolName = "beacon_rerun_failed"

// RerunFailedInput is the argument object of beacon_rerun_failed. The runID is
// optional because a check details URL carries it; the handler resolves it
// from the Watch when the caller leaves it out.
type RerunFailedInput struct {
	WatchID   string `json:"watchID" jsonschema:"Watch whose failed checks are rerun; required"`
	CheckName string `json:"checkName,omitempty" jsonschema:"limit the rerun to one flaky matched check"`
	RunID     int64  `json:"runID,omitempty" jsonschema:"workflow run id from the check details URL; resolved from the Watch when omitted"`
}

// RerunFailedOutput is the response of beacon_rerun_failed. The rerun flag is
// false for a repeat of the same idempotency key. RetryBudget reports the
// budget left after this call. ObligationID is the idempotency key so a caller
// can trace the spend in the store.
type RerunFailedOutput struct {
	WatchID      string `json:"watchID"`
	Revision     uint64 `json:"revision"`
	RetryBudget  int    `json:"retryBudget"`
	Rerun        bool   `json:"rerun"`
	RunID        int64  `json:"runID"`
	ObligationID string `json:"obligationID,omitempty"`
}

// RerunPort is the write boundary of beacon_rerun_failed. Watch reads the
// current state so the handler computes the budget from the same projection
// get_result advertises. RerunFailed is the only call that reaches GitHub; the
// composition-root adapter implements it with the existing internal/infra/github
// layer and never with os/exec. RerunRecorded and SpendRerunBudget keep the
// idempotency fact and the budget spend in one store transaction.
type RerunPort interface {
	Watch(id string) (domain.Watch, error)
	RerunFailed(ctx context.Context, repo string, runID int64) error
	RerunRecorded(ctx context.Context, key string) (bool, error)
	SpendRerunBudget(ctx context.Context, key string, checkNames []string) error
}

// RerunFailedHandler implements beacon_rerun_failed over RerunPort.
type RerunFailedHandler struct {
	port RerunPort
}

// NewRerunFailedHandler builds a handler. A nil port is kept so Rerun reports
// a persist error instead of returning a successful projection.
func NewRerunFailedHandler(port RerunPort) *RerunFailedHandler {
	return &RerunFailedHandler{port: port}
}

// Handle adapts Rerun to the go-sdk typed tool handler signature.
func (h *RerunFailedHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in RerunFailedInput) (*mcpsdk.CallToolResult, RerunFailedOutput, error) {
	out, err := h.Rerun(ctx, in)
	if err != nil {
		return nil, RerunFailedOutput{}, err
	}
	return nil, out, nil
}

// Rerun validates the input, refuses a spent budget before any provider call,
// and re-runs the failed checks once per (watchID, revision, runID). A repeat
// of the same key returns rerun=false and leaves the spend counter untouched.
func (h *RerunFailedHandler) Rerun(ctx context.Context, in RerunFailedInput) (RerunFailedOutput, error) {
	if in.WatchID == "" {
		return RerunFailedOutput{}, domain.NewAppError(domain.CodeInvalidInput, "rerun_failed: watchID is required")
	}
	if h == nil || h.port == nil {
		return RerunFailedOutput{}, domain.NewAppError(domain.CodePersistFailed, "rerun_failed: port is nil")
	}

	w, err := h.port.Watch(in.WatchID)
	if err != nil {
		return RerunFailedOutput{}, domain.NewAppErrorWithCause(domain.CodeWatchNotFound, "rerun_failed: watch: "+err.Error(), err)
	}

	eligible := flakyFailedChecks(w)
	if in.CheckName != "" {
		eligible = filterFailedChecks(eligible, in.CheckName)
		if len(eligible) == 0 {
			return RerunFailedOutput{}, domain.NewAppError(domain.CodeInvalidInput, "rerun_failed: checkName is not a flaky-matched failed check")
		}
	}
	budget := remainingRetryBudget(w, eligible)
	if budget <= 0 {
		return RerunFailedOutput{}, domain.NewAppError(domain.CodeLimitExceeded, "rerun_failed: no retry budget left for Watch "+w.ID)
	}

	runID := in.RunID
	if runID <= 0 {
		runID = resolveRunID(eligible)
		if runID <= 0 {
			return RerunFailedOutput{}, domain.NewAppError(domain.CodeInvalidInput, "rerun_failed: runID is required and cannot be resolved from the Watch")
		}
	}

	key := rerunIdempotencyKey(w.ID, w.Revision, runID)
	out := RerunFailedOutput{
		WatchID:      w.ID,
		Revision:     w.Revision,
		RetryBudget:  budget,
		RunID:        runID,
		ObligationID: key,
	}
	recorded, err := h.port.RerunRecorded(ctx, key)
	if err != nil {
		return RerunFailedOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "rerun_failed: idempotency read: "+err.Error(), err)
	}
	if recorded {
		return out, nil
	}

	if err := h.port.RerunFailed(ctx, w.Repo, runID); err != nil {
		return RerunFailedOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "rerun_failed: provider: "+err.Error(), err)
	}
	if err := h.port.SpendRerunBudget(ctx, key, failedCheckNames(eligible)); err != nil {
		return RerunFailedOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, "rerun_failed: spend budget: "+err.Error(), err)
	}
	out.Rerun = true
	out.RetryBudget = budget - 1
	return out, nil
}

// flakyFailedChecks keeps only failed checks that matched the configured flaky
// set. get_result builds the same set from collectFailedChecks and
// flakyMatched, so an agent sees the same eligible checks in both tools.
func flakyFailedChecks(w domain.Watch) []FailedCheck {
	out := make([]FailedCheck, 0, len(w.Checks))
	for _, fc := range collectFailedChecks(w) {
		if fc.FlakyMatched {
			out = append(out, fc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// filterFailedChecks keeps one named check from the eligible set. The caller
// has already checked that the set holds only flaky-matched failures.
func filterFailedChecks(in []FailedCheck, name string) []FailedCheck {
	return shared.Filter(in, func(fc FailedCheck) bool { return fc.Name == name })
}

// rerunIdempotencyKey reuses the domain obligation key with a rerun namespace.
// The triple (watchID, revision, runID) is injective, so two runs on the same
// Watch revision never share a spend slot.
func rerunIdempotencyKey(watchID string, revision uint64, runID int64) string {
	return domain.ObligationKey(watchID, revision, "rerun:"+strconv.FormatInt(runID, 10))
}

// resolveRunID reads the first workflow run id from the details URLs of the
// eligible checks. The eligible set is sorted by name, so the answer does not
// depend on map iteration order.
func resolveRunID(failed []FailedCheck) int64 {
	for _, fc := range failed {
		if id, ok := parseRunID(fc.DetailsURL); ok {
			return id
		}
	}
	return 0
}

// parseRunID extracts the id after the "runs" segment of a GitHub actions URL.
// It accepts an absolute URL and returns false for a relative or malformed one.
func parseRunID(detailsURL string) (int64, bool) {
	u, err := url.Parse(detailsURL)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range parts {
		if part != "runs" || i+1 >= len(parts) {
			continue
		}
		id, err := strconv.ParseInt(parts[i+1], 10, 64)
		if err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

// RerunFailedOutputSchema exposes the derived output schema for the wiring
// descriptor. The schema comes from the output type above, not from a hand
// written JSON literal (ADR 0003, ADR 0268).
func RerunFailedOutputSchema() (*jsonschema.Schema, error) {
	return OutputSchema[RerunFailedOutput]()
}

// mustRerunFailedOutputSchema is safe to call while registering a tool: the
// reflection over the output type above cannot fail with a fixed struct.
func mustRerunFailedOutputSchema() *jsonschema.Schema {
	schema, err := RerunFailedOutputSchema()
	if err != nil {
		return &jsonschema.Schema{Type: "object"}
	}
	return schema
}

// AttachRerunFailed registers the opt-in handler on a go-sdk server. The
// advertised descriptor stays in the owner's shared wiring PR, like AT-05,
// because the tools/list contract is byte-pinned to its current names.
func AttachRerunFailed(server *mcpsdk.Server, handler *RerunFailedHandler) {
	if server == nil || handler == nil {
		return
	}
	annotations := annotationsForKind(kindAdditive)
	server.RemoveTools(RerunFailedToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         RerunFailedToolName,
		Description:  "Re-run the failed checks of a Watch through MCP, idempotent by (watchID, revision, runID) and bounded by the flaky retry budget.",
		OutputSchema: mustRerunFailedOutputSchema(),
		Annotations:  &annotations,
	}, handler.Handle)
}
