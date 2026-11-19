// Package mcp: beacon_list_running as a pure read of Beacon state.
package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// StateSnapshot is the read-only view of Beacon state shared by the two
// observability tools. A SnapshotReader produces it. No field is sourced from
// a write path, so the tools only project what was already persisted.
type StateSnapshot struct {
	ActiveWatches   []domain.Watch
	PointsThisHour  int
	Budget          int
	PersistSeq      uint64
	LastPersistErr  string
	LoadErrors      []string
	LadderPending   int
	LadderEscalated int
	Version         string
}

// SnapshotReader is the single read-only state accessor used by
// beacon_list_running and beacon_stats. The interface deliberately has no
// write method: a tool holding only this dependency cannot mutate state, which
// is non-variant 9 of WORKER-PROMPT §4.2.
type SnapshotReader interface {
	Snapshot() (StateSnapshot, error)
}

// SnapshotFunc adapts a read function to SnapshotReader. The wiring layer can
// pass a closure over the store, the point counter and the ladder reader
// without adding another type to this package.
type SnapshotFunc func() (StateSnapshot, error)

func (f SnapshotFunc) Snapshot() (StateSnapshot, error) { return f() }

// ListRunningFilters narrows the watches returned by beacon_list_running.
// Empty fields are ignored, so an empty filter selects every running watch.
type ListRunningFilters struct {
	Verdict string `json:"verdict,omitempty" jsonschema:"only watches with this verdict"`
	Status  string `json:"status,omitempty" jsonschema:"only watches with this status"`
}

func (f *ListRunningFilters) matches(w domain.Watch) bool {
	if f.Verdict != "" && string(w.Verdict) != f.Verdict {
		return false
	}
	if f.Status != "" && string(w.Status) != f.Status {
		return false
	}
	return true
}

// ListRunningInput is the input of beacon_list_running. Filters are optional.
type ListRunningInput struct {
	Filters *ListRunningFilters `json:"filters,omitempty" jsonschema:"optional filter on running watches"`
}

// ListRunningHandler implements beacon_list_running over the read-only
// snapshot port. It holds no writer, so the handler cannot mutate state.
type ListRunningHandler struct {
	reader SnapshotReader
}

// NewListRunningHandler builds the handler. Pass handler.Handle to
// mcpsdk.AddTool when wiring the tool into a server.
func NewListRunningHandler(reader SnapshotReader) *ListRunningHandler {
	return &ListRunningHandler{reader: reader}
}

// Handle reads a snapshot and projects it onto counts and a watch array. Every
// counter is derived from the returned array, so counts stay consistent with
// watches for any filter.
func (h *ListRunningHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, in ListRunningInput) (*mcpsdk.CallToolResult, ListRunningOutput, error) {
	snap, err := h.reader.Snapshot()
	if err != nil {
		return nil, ListRunningOutput{}, fmt.Errorf("beacon_list_running: %w", err)
	}

	watches := make([]domain.Watch, 0, len(snap.ActiveWatches))
	for _, w := range snap.ActiveWatches {
		if in.Filters != nil && !in.Filters.matches(w) {
			continue
		}
		watches = append(watches, w)
	}

	byVerdict := make(map[string]int)
	subscribers := 0
	for _, w := range watches {
		byVerdict[string(w.Verdict)]++
		subscribers += len(w.Subscribers)
	}

	out := ListRunningOutput{
		Counts: WatchCounts{
			Watches:     len(watches),
			ByVerdict:   byVerdict,
			Subscribers: subscribers,
		},
		Watches: watches,
	}
	return &mcpsdk.CallToolResult{}, out, nil
}
