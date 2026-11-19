// Package mcp: beacon_stats as a pure read of Beacon state.
package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// StatsInput is the empty input of beacon_stats. The tool takes no arguments.
type StatsInput struct{}

// StatsHandler implements beacon_stats over the read-only snapshot port.
// It holds no writer and takes no argument.
type StatsHandler struct {
	reader SnapshotReader
}

// NewStatsHandler builds the handler. Pass handler.Handle to mcpsdk.AddTool
// when wiring the tool into a server.
func NewStatsHandler(reader SnapshotReader) *StatsHandler {
	return &StatsHandler{reader: reader}
}

// Handle copies the values already present in a state snapshot into the stats
// response. It performs no write. Slices are allocated even when empty so the
// response always carries an array, never JSON null.
func (h *StatsHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, _ StatsInput) (*mcpsdk.CallToolResult, StatsOutput, error) {
	snap, err := h.reader.Snapshot()
	if err != nil {
		return nil, StatsOutput{}, fmt.Errorf("beacon_stats: %w", err)
	}

	loadErrors := make([]string, len(snap.LoadErrors))
	copy(loadErrors, snap.LoadErrors)

	out := StatsOutput{
		PointsThisHour: snap.PointsThisHour,
		Budget:         snap.Budget,
		ActiveWatches:  len(snap.ActiveWatches),
		Persist: PersistInfo{
			Seq:       snap.PersistSeq,
			LastError: snap.LastPersistErr,
		},
		LoadErrors: loadErrors,
		Ladder: LadderInfo{
			Pending:   snap.LadderPending,
			Escalated: snap.LadderEscalated,
		},
		Version: snap.Version,
	}
	return &mcpsdk.CallToolResult{}, out, nil
}
