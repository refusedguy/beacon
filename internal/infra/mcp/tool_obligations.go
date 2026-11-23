// Package mcp: beacon_obligations lists the obligations staged for one Watch.
//
// This is a read-only diagnostic tool (AT-04). It answers "obligation not found"
// without reading the store or the logs: an agent sees which subscriber and
// which revision an obligation was created for. The handler never writes, so
// readAt and claimedAt are reported but never stamped.
//
// The tool is opt-in. It stays outside toolDescriptors so the advertised
// tools/list set keeps its pinned byte-for-byte contract. A composition root
// that needs the diagnostic calls AttachObligations (same pattern as
// t3_thread_read, ADR 0263: no silent registration).
package mcp

import (
	"context"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// ObligationLister is the read port used by beacon_obligations. The store
// adapter exposes WatchObligations; cibridge declares the same method as its
// activitySource (bridge.go:134). A lister never mutates the obligations it
// returns.
type ObligationLister interface {
	WatchObligations(watchID string) ([]domain.Obligation, error)
}

// obligationsWatchReader is the narrow read port for the Watch revision that
// goes into the response. The production store satisfies it.
type obligationsWatchReader interface {
	Watch(id string) (domain.Watch, error)
}

// ObligationsInput is the argument object of beacon_obligations. SubscriberID
// is an optional in-memory filter; it is never interpreted by the core.
type ObligationsInput struct {
	WatchID      string `json:"watchID" jsonschema:"Watch whose obligations are listed; required"`
	SubscriberID string `json:"subscriberID,omitempty" jsonschema:"optional opaque subscriber or session id filter"`
}

// ObligationRow is one obligation projected onto the wire. Timestamps are
// RFC3339 strings and are empty when the fact was never recorded.
type ObligationRow struct {
	ObligationID   string `json:"obligationID"`
	Revision       uint64 `json:"revision"`
	SubscriberID   string `json:"subscriberID"`
	VerdictPointer string `json:"verdictPointer"`
	StagedAt       string `json:"stagedAt,omitempty"`
	ClaimedAt      string `json:"claimedAt,omitempty"`
	ReadAt         string `json:"readAt,omitempty"`
	DeliveredAt    string `json:"deliveredAt,omitempty"`
}

// ObligationsOutput is the response of beacon_obligations. Found is false for
// an empty selection, including a watchID with no obligation rows. Truncated
// is true when the response hit the record ceiling.
type ObligationsOutput struct {
	WatchID     string          `json:"watchID"`
	Revision    uint64          `json:"revision"`
	Obligations []ObligationRow `json:"obligations"`
	Found       bool            `json:"found"`
	Truncated   bool            `json:"truncated,omitempty"`
}

// The ceilings bound the obligation array of one response. They mirror
// getResultMaxRecords so a diagnostic read cannot flood the caller's context.
const (
	obligationsMaxRecords = 200
	obligationsMaxBytes   = 256 * 1024
)

const obligationsNarrowHint = "filter by subscriberID or read a watchID with fewer obligations"

// ObligationsOutputSchema derives the schema of the opt-in beacon_obligations
// tool. It is intentionally outside outputSchemaFactories: the advertised
// tools are pinned byte-for-byte, so an opt-in schema must not change them.
func ObligationsOutputSchema() (*jsonschema.Schema, error) {
	return OutputSchema[ObligationsOutput]()
}

// ObligationsHandler implements beacon_obligations over the two read ports.
// It holds no writer and no cache: every call re-reads the lister and projects
// a fresh snapshot, so a lost or stale client never changes stored state.
type ObligationsHandler struct {
	watches obligationsWatchReader
	lister  ObligationLister
}

// NewObligationsHandler builds the handler. A nil watch reader is allowed; the
// response Revision is then zero. A nil lister makes List return a persist
// error instead of a panic, which is the boundary-discipline rule used by the
// other MCP handlers.
func NewObligationsHandler(watches obligationsWatchReader, lister ObligationLister) *ObligationsHandler {
	return &ObligationsHandler{watches: watches, lister: lister}
}

// obligationTime formats a recorded fact. A zero time is omitted from the wire
// instead of being sent as year one.
func obligationTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// revisionOf reads the current Watch revision. A missing Watch is a normal
// diagnostic answer: the caller still learns that no obligation was found. A
// real storage error is returned so the agent can tell a failure from an empty
// result.
func (h *ObligationsHandler) revisionOf(watchID string) (uint64, error) {
	if h == nil || h.watches == nil {
		return 0, nil
	}
	w, err := h.watches.Watch(watchID)
	if err != nil {
		if notFound, ok := h.watches.(resultStoreNotFound); ok && notFound.IsNotFound(err) {
			return 0, nil
		}
		observe.ActorError("obligations-watch", watchID, err)
		return 0, domain.NewAppError(domain.CodePersistFailed, "obligations: watch: "+err.Error())
	}
	return w.Revision, nil
}

// List projects the obligations of one Watch. A subscriberID filter is applied
// in memory after the read. An empty selection is a success with found=false,
// never an error. The handler does not stamp readAt or claimedAt.
func (h *ObligationsHandler) List(_ context.Context, in ObligationsInput) (ObligationsOutput, error) {
	if in.WatchID == "" {
		return ObligationsOutput{}, domain.NewAppError(domain.CodeInvalidInput, "obligations: watchID is required")
	}
	if h == nil || h.lister == nil {
		return ObligationsOutput{}, domain.NewAppError(domain.CodePersistFailed, "obligations: lister is nil")
	}

	revision, err := h.revisionOf(in.WatchID)
	if err != nil {
		return ObligationsOutput{}, err
	}

	all, err := h.lister.WatchObligations(in.WatchID)
	if err != nil {
		observe.ActorError("obligations-list", in.WatchID, err)
		return ObligationsOutput{}, domain.NewAppError(domain.CodePersistFailed, "obligations: list: "+err.Error())
	}

	selected := all
	if in.SubscriberID != "" {
		selected = make([]domain.Obligation, 0, len(all))
		for _, o := range all {
			if o.SubscriberID == in.SubscriberID {
				selected = append(selected, o)
			}
		}
	}

	truncated := len(selected) > obligationsMaxRecords
	if truncated {
		selected = selected[:obligationsMaxRecords]
	}

	rows := make([]ObligationRow, 0, len(selected))
	for _, o := range selected {
		rows = append(rows, ObligationRow{
			ObligationID:   o.ID,
			Revision:       o.Revision,
			SubscriberID:   o.SubscriberID,
			VerdictPointer: string(o.VerdictPointer),
			StagedAt:       obligationTime(o.StagedAt),
			ClaimedAt:      obligationTime(o.ClaimedAt),
			ReadAt:         obligationTime(o.ReadAt),
			DeliveredAt:    obligationTime(o.DeliveredAt),
		})
	}

	out := ObligationsOutput{
		WatchID:     in.WatchID,
		Revision:    revision,
		Obligations: rows,
		Found:       len(rows) > 0,
		Truncated:   truncated,
	}
	limits := responseLimits{records: obligationsMaxRecords, bytes: obligationsMaxBytes}
	if err := checkResponseLimits("beacon_obligations", "obligation", len(rows), limits, obligationsNarrowHint, out); err != nil {
		return ObligationsOutput{}, err
	}
	return out, nil
}

// Handle adapts List to the go-sdk typed tool handler signature. The tool is
// read-only, so it needs neither the request meta nor an output result wrapper.
func (h *ObligationsHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in ObligationsInput) (*mcpsdk.CallToolResult, ObligationsOutput, error) {
	out, err := h.List(ctx, in)
	if err != nil {
		return nil, ObligationsOutput{}, err
	}
	return nil, out, nil
}

// AttachObligations installs beacon_obligations as an opt-in read-only tool.
// The name is deliberately absent from toolDescriptors: the advertised set has
// a pinned tools/list contract, so a composition root that needs the diagnostic
// calls this helper. A nil server or lister leaves the server unchanged. A nil
// watch reader is allowed and gives revision zero. A nil schema falls back to
// the derived one.
func AttachObligations(server *mcpsdk.Server, watches obligationsWatchReader, lister ObligationLister, outputSchema *jsonschema.Schema) {
	if lister == nil {
		return
	}
	if outputSchema == nil {
		derived, err := ObligationsOutputSchema()
		if err == nil {
			outputSchema = derived
		}
		if outputSchema == nil {
			outputSchema = &jsonschema.Schema{Type: "object"}
		}
	}
	registerTool(server, "beacon_obligations",
		"List the obligations of one Watch for diagnosing obligation not found; read-only and never stamps readAt or claimedAt.",
		outputSchema, newToolAnnotations(true, false, true), NewObligationsHandler(watches, lister).Handle)
}
