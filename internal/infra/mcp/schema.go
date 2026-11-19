// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file derives tool output schemas from the Go output types and defines
// the single error envelope returned to MCP clients (ADR 0003, ADR 0009).
package mcp

import (
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/refusedguy/beacon/internal/domain"
)

// Output types for the seven tools (WORKER-PROMPT §4.1). Every schema shown to
// a client is derived from one of these structs by reflection, never by a
// hand-written JSON literal.

// WatchPROutput is the response of beacon_watch_pr.
type WatchPROutput struct {
	WatchID     string `json:"watchID"`
	Revision    uint64 `json:"revision"`
	Status      string `json:"status"`
	Verdict     string `json:"verdict"`
	PollAfterMs int64  `json:"pollAfterMs"`
}

// WaitResultOutput is the response of beacon_wait_result.
type WaitResultOutput struct {
	Status      string                  `json:"status"`
	Verdict     string                  `json:"verdict,omitempty"`
	Revision    uint64                  `json:"revision"`
	Checks      map[string]domain.Check `json:"checks,omitempty"`
	PollAfterMs *int64                  `json:"pollAfterMs,omitempty"`
}

// FailedCheck is one failed check returned by beacon_get_result.
type FailedCheck struct {
	Name         string `json:"name"`
	Conclusion   string `json:"conclusion"`
	DetailsURL   string `json:"detailsUrl"`
	Required     bool   `json:"required"`
	FlakyMatched bool   `json:"flakyMatched"`
}

// MergeSnapshot is the merge block of beacon_get_result.
type MergeSnapshot struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
}

// GetResultOutput is the response of beacon_get_result.
type GetResultOutput struct {
	Status       string        `json:"status"`
	Verdict      string        `json:"verdict"`
	Revision     uint64        `json:"revision"`
	Merge        MergeSnapshot `json:"merge"`
	FailedChecks []FailedCheck `json:"failedChecks"`
	ObligationID string        `json:"obligationID"`
	RetryBudget  int           `json:"retryBudget"`
}

// WatchCounts holds the counters of beacon_list_running.
type WatchCounts struct {
	Watches     int            `json:"watches"`
	ByVerdict   map[string]int `json:"byVerdict"`
	Subscribers int            `json:"subscribers"`
}

// WatchEvent is one item in a cursor-delivered event stream. Seq is a
// monotonic, opaque position the client passes back as the cursor. The event
// carries only identifiers and the latest status and verdict, never the full
// check history (ADR 0025).
type WatchEvent struct {
	Seq     uint64 `json:"seq"`
	WatchID string `json:"watchID"`
	Status  string `json:"status"`
	Verdict string `json:"verdict"`
}

// ListRunningOutput is the response of beacon_list_running. Status and Cursor
// replace a replayed history: a subscriber that lost its connection continues
// from Cursor and receives only WatchEvent values newer than it. Once Status
// is terminal, Verdict is set and Events is empty, so the verdict is obtained
// without replaying old events (ADR 0025).
type ListRunningOutput struct {
	Counts  WatchCounts    `json:"counts"`
	Watches []domain.Watch `json:"watches"`
	Status  string         `json:"status"`
	Cursor  string         `json:"cursor"`
	Events  []WatchEvent   `json:"events,omitempty"`
	Verdict string         `json:"verdict,omitempty"`
}

// OKOutput is the response of beacon_ack and beacon_cancel_watch.
type OKOutput struct {
	OK bool `json:"ok"`
}

// PersistInfo is the persistence block of beacon_stats.
type PersistInfo struct {
	Seq       uint64 `json:"seq"`
	LastError string `json:"lastError"`
}

// LadderInfo is the escalation ladder block of beacon_stats.
type LadderInfo struct {
	Pending   int `json:"pending"`
	Escalated int `json:"escalated"`
}

// StatsOutput is the response of beacon_stats.
type StatsOutput struct {
	PointsThisHour int         `json:"pointsThisHour"`
	Budget         int         `json:"budget"`
	ActiveWatches  int         `json:"activeWatches"`
	Persist        PersistInfo `json:"persist"`
	LoadErrors     []string    `json:"loadErrors"`
	Ladder         LadderInfo  `json:"ladder"`
	Version        string      `json:"version"`
}

// OutputSchema derives the JSON schema for T from its Go type. This is the
// same reflection based inference go-sdk uses when it registers a typed tool
// handler, so an output schema can never drift from its struct definition.
func OutputSchema[T any]() (*jsonschema.Schema, error) {
	return jsonschema.For[T](nil)
}

// outputSchemaFactories pairs each tool name with its output type. Keys are
// tool names from WORKER-PROMPT §4.1.
var outputSchemaFactories = map[string]func() (*jsonschema.Schema, error){
	"beacon_watch_pr":    func() (*jsonschema.Schema, error) { return OutputSchema[WatchPROutput]() },
	"beacon_wait_result": func() (*jsonschema.Schema, error) { return OutputSchema[WaitResultOutput]() },
	"beacon_get_result":  func() (*jsonschema.Schema, error) { return OutputSchema[GetResultOutput]() },
	"beacon_list_running": func() (*jsonschema.Schema, error) {
		return OutputSchema[ListRunningOutput]()
	},
	"beacon_ack":          func() (*jsonschema.Schema, error) { return OutputSchema[OKOutput]() },
	"beacon_cancel_watch": func() (*jsonschema.Schema, error) { return OutputSchema[OKOutput]() },
	"beacon_stats":        func() (*jsonschema.Schema, error) { return OutputSchema[StatsOutput]() },
}

// AllOutputSchemas returns the derived output schema of every tool, keyed by
// tool name. An error means a Go type could not be reflected into a schema.
func AllOutputSchemas() (map[string]*jsonschema.Schema, error) {
	out := make(map[string]*jsonschema.Schema, len(outputSchemaFactories))
	for name, factory := range outputSchemaFactories {
		schema, err := factory()
		if err != nil {
			return nil, fmt.Errorf("output schema for %s: %w", name, err)
		}
		out[name] = schema
	}
	return out, nil
}

// ErrorShape is the single error envelope returned by every MCP tool on
// failure (WORKER-PROMPT §4.1, ADR 0009). The six fields are ok, code, message,
// status and the two optional fields retryAfterMs and newWatchID.
type ErrorShape struct {
	OK           bool             `json:"ok"`
	Code         domain.ErrorCode `json:"code"`
	Message      string           `json:"message"`
	Status       int              `json:"status"`
	RetryAfterMs *int64           `json:"retryAfterMs,omitempty"`
	NewWatchID   string           `json:"newWatchID,omitempty"`
}

// statusByCode is the contract v2 code to status table. Statuses mirror the
// domain table in internal/domain so the MCP boundary has its own explicit
// mapping to assert against.
var statusByCode = map[domain.ErrorCode]int{
	domain.CodeInvalidInput:   400,
	domain.CodeWatchNotFound:  404,
	domain.CodeHeadSHAChanged: 409,
	domain.CodeRateLimited:    429,
	domain.CodeLimitExceeded:  429,
	domain.CodeCancelled:      410,
	domain.CodeGone:           410,
	domain.CodePersistFailed:  503,
}

// StatusForCode returns the contract status for code, or 0 for an unknown code.
func StatusForCode(code domain.ErrorCode) int {
	return statusByCode[code]
}

// ToErrorShape converts an error that wraps a *domain.AppError into the MCP
// error envelope. The bool is false when err carries no AppError, so callers
// can fall back to a generic protocol error. Code specific fields are carried
// through unchanged: HEAD_SHA_CHANGED keeps its newWatchID and RATE_LIMITED
// keeps its retryAfterMs as populated by the rate limit adapter.
func ToErrorShape(err error) (ErrorShape, bool) {
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		return ErrorShape{}, false
	}

	status := StatusForCode(appErr.Code)
	if status == 0 {
		status = appErr.Status
	}

	return ErrorShape{
		OK:           false,
		Code:         appErr.Code,
		Message:      appErr.Message,
		Status:       status,
		RetryAfterMs: appErr.RetryAfterMs,
		NewWatchID:   appErr.NewWatchID,
	}, true
}
