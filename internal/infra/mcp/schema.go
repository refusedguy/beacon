// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file derives tool output schemas from the Go output types and defines
// the single error envelope returned to MCP clients (ADR 0003, ADR 0009).
package mcp

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/refusedguy/beacon/internal/domain"
)

// Output types for the contracted tools (WORKER-PROMPT §4.1, W12-B and ADR 0268).
// Every schema shown to a client is derived from one of these structs by reflection,
// never by a hand-written JSON literal.

// WatchPROutput is the response of beacon_watch_pr. The five leading fields
// are the original contract (ADR 0094); Executor extends the notify metadata
// with the descriptor supplied at registration (T5.32).
type WatchPROutput struct {
	WatchID     string `json:"watchID"`
	Revision    uint64 `json:"revision"`
	Status      string `json:"status"`
	Verdict     string `json:"verdict"`
	PollAfterMs int64  `json:"pollAfterMs"`

	// Executor echoes the executor descriptor carried in notify. It is a
	// pointer with omitempty so a response from a caller that omitted the
	// descriptor keeps its previous shape and still validates against the
	// derived schema. The core carries these opaque values through without
	// interpreting agent names (ADR 0088).
	Executor *ExecutorMetadata `json:"executor,omitempty"`
}

// ExecutorMetadata is the notify-metadata descriptor of the registered
// executor as reflected into the watch_pr output schema. Every field is
// optional: a descriptor may be partially supplied, and a response without
// executor stays valid (T5.32).
type ExecutorMetadata struct {
	ID           string   `json:"id,omitempty"`
	Role         string   `json:"role,omitempty"`
	Runtime      string   `json:"runtime,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	ConfigID     string   `json:"configId,omitempty"`
}

// WaitResultOutput is the response of beacon_wait_result.
type WaitResultOutput struct {
	Status      string                  `json:"status"`
	Verdict     string                  `json:"verdict,omitempty"`
	Revision    uint64                  `json:"revision"`
	Checks      map[string]domain.Check `json:"checks,omitempty"`
	PollAfterMs *int64                  `json:"pollAfterMs,omitempty"`

	// Reason names why the wait ended (ADR 0046 p3). It is declared on the
	// advertised output type so a client that validates a response against
	// OutputSchema accepts the reason the handler returns (BCX-43). The field
	// is optional, so a response without a reason keeps its previous shape.
	Reason WaitStopReason `json:"reason,omitempty"`
}

// FailedCheck is one failed check returned by beacon_get_result.
type FailedCheck struct {
	Name         string `json:"name"`
	Conclusion   string `json:"conclusion"`
	DetailsURL   string `json:"detailsUrl"`
	Required     bool   `json:"required"`
	FlakyMatched bool   `json:"flakyMatched"`
	// FailureExcerpt is a short, redacted annotation excerpt for this check.
	// It is optional so a response from a caller without an excerpt source
	// keeps its previous wire shape (W11-B).
	FailureExcerpt string `json:"failureExcerpt,omitempty"`
}

// MergeSnapshot is the merge block of beacon_get_result.
type MergeSnapshot struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
}

// Machine-readable next actions attached to a result. An agent branches on
// nextAction, not on prose: the four wake verdicts map one-to-one, and every
// other verdict reports "wait" with an explanatory hint. The values come from
// the domain verdict contract in internal/domain/types.go, so a rename changes
// this schema with the verdict.
const (
	NextActionKeepWorking = string(domain.VerdictKeepWorking)
	NextActionRebase      = string(domain.VerdictRebase)
	NextActionRetry       = string(domain.VerdictRetry)
	NextActionBlocked     = string(domain.VerdictBlocked)
	NextActionWait        = string(domain.VerdictWait)
)

// Commands paired with an action. They are literal shell lines so a client can
// run them without reconstructing a command from a description.
const (
	RebaseCommand = "git fetch origin main && git rebase origin/main && git push --force-with-lease"
	RetryCommand  = "gh run rerun --failed"
)

// GetResultOutput is the response of beacon_get_result. ADR 0094 lists every
// field and its purpose, so a client can read the contract in one place.
//
// The first seven fields are required and appear in every response. The rest
// are optional and carry the omitempty tag. A client must ignore fields it
// does not know, since a new optional field may arrive without a version bump.
//
// The task dialect fields (resultType, taskId, jobID, taskStatus,
// pollIntervalMs, instructions) describe the MCP task form. A plain client
// whose call finishes before --after does not see them. The MCP boundary, not
// the domain, chooses them: ADR 0031 §2 and ADR 0003 keep protocol versions out
// of the core, and LegacyProtocolVersions still excludes 2026-07-28.
//
// nextAction, nextCommand and nextHint are the self-teaching part of the
// response. The tool tells the caller the one step that follows this verdict.
// failureExcerpt, failureExcerptTruncated and failureExcerptLogUrl report
// failed check output and a link to the full CI log (W11-B, ATX-36). configId
// carries the Watch configuration identity (ADR 0087, T5.36).
type GetResultOutput struct {
	Status       string        `json:"status"`
	Verdict      string        `json:"verdict"`
	Revision     uint64        `json:"revision"`
	Merge        MergeSnapshot `json:"merge"`
	FailedChecks []FailedCheck `json:"failedChecks"`
	ObligationID string        `json:"obligationID"`
	RetryBudget  int           `json:"retryBudget"`

	ResultType     string `json:"resultType,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
	JobID          string `json:"jobID,omitempty"`
	TaskStatus     string `json:"taskStatus,omitempty"`
	PollIntervalMs int64  `json:"pollIntervalMs,omitempty"`
	Instructions   string `json:"instructions,omitempty"`

	// NextAction is one of the NextAction* values. NextCommand is the exact
	// command to run when the action prescribes one. NextHint explains the
	// decision in one sentence and is always present.
	NextAction  string `json:"nextAction,omitempty"`
	NextCommand string `json:"nextCommand,omitempty"`
	NextHint    string `json:"nextHint,omitempty"`

	// FailureExcerpt is the aggregate of the per-check excerpts, capped at
	// 2 KB. It is optional: a Watch whose checks have no annotations, or a
	// server without an excerpt source, omits it and keeps the stable schema
	// (W11-B).
	FailureExcerpt string `json:"failureExcerpt,omitempty"`

	// FailureExcerptTruncated reports whether the byte ceiling cut any excerpt.
	// When true the agent has only part of the error text and should read the
	// full CI log at FailureExcerptLogURL (ATX-36).
	FailureExcerptTruncated bool `json:"failureExcerptTruncated,omitempty"`

	// FailureExcerptLogURL links to the check run whose excerpt was cut, so the
	// agent can read the full CI log. It is set only when an excerpt is present.
	FailureExcerptLogURL string `json:"failureExcerptLogUrl,omitempty"`

	// ConfigID is the Watch's configuration identity from ADR 0087 (T5.36).
	// It is optional and emitted last, so a Watch without one keeps the exact
	// previous bytes and field order.
	ConfigID string `json:"configId,omitempty"`
}

// TaskDialect is the application-level response form a client can consume.
// It is derived from client capabilities at the MCP boundary, not from the
// negotiated protocol version: the handshake never advertises 2026-07-28.
type TaskDialect string

const (
	// DialectPlain has no Tasks: a long call yields an ordinary job id plus
	// the existing result, status and cancel tools (ADR 0031 §2).
	DialectPlain TaskDialect = "plain"
	// DialectTasks2025 speaks the experimental Tasks of the 2025-11-25 core:
	// a real task handle is handed back right away.
	DialectTasks2025 TaskDialect = "tasks-2025-11-25"
	// DialectTasks2026 speaks the io.modelcontextprotocol/tasks extension:
	// resultType "task" appears only once the --after threshold is crossed.
	DialectTasks2026 TaskDialect = "tasks-2026-07-28"
)

const (
	// TaskResultType is the extension discriminator for a created task.
	TaskResultType = "task"
	// TaskStatusWorking is the seed status of a task handle.
	TaskStatusWorking = "working"
	// DefaultTaskPollIntervalMs is the suggested client polling interval.
	// DefaultTaskPollIntervalMs bounds task-dialect clients. The old 5s
	// default made agents spam tools; a terminal verdict still wakes them.
	DefaultTaskPollIntervalMs int64 = 180000
	// PlainJobInstructions tells a client without Tasks how to collect its job.
	PlainJobInstructions = "use beacon_get_result and beacon_wait_result with the jobID; cancel with beacon_cancel_watch"
)

// ApplyTaskForm selects the response form for one tool result. DialectPlain
// keeps the classic tools/call shape until the call runs longer than after;
// DialectTasks2025 always returns a task handle; DialectTasks2026 switches to
// resultType "task" only after the threshold. A non-positive after disables
// the threshold, so the default server keeps its exact previous responses.
func ApplyTaskForm(out GetResultOutput, dialect TaskDialect, elapsed, after time.Duration) GetResultOutput {
	long := after > 0 && elapsed >= after
	if dialect != DialectTasks2025 && !long {
		return out
	}

	handle := out.ObligationID
	if handle == "" {
		handle = out.Status
	}
	switch dialect {
	case DialectTasks2025:
		out.TaskID = handle
		out.TaskStatus = TaskStatusWorking
		out.PollIntervalMs = DefaultTaskPollIntervalMs
	case DialectTasks2026:
		out.ResultType = TaskResultType
		out.TaskID = handle
		out.TaskStatus = TaskStatusWorking
		out.PollIntervalMs = DefaultTaskPollIntervalMs
	case DialectPlain:
		out.JobID = handle
		out.Instructions = PlainJobInstructions
	}
	return out
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
	// BlockerID and Blocked make a blocked Watch visible in the compact event
	// stream, so a client that reads only Events still sees the whole radar.
	BlockerID string `json:"blockerID,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`
	// Phases maps subscriberID to its worker phase: waiting, in_work,
	// blocked, orphaned or finished.
	Phases map[string]string `json:"phases,omitempty"`
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

// ThreadMessage is one item of a thread history read by t3_thread_read.
// Position is a monotonic, opaque ordering value the client does not have to
// interpret. CreatedBy names the actor that produced the item. CreationSource
// names the ingress that carried it. The two are separate fields so an agent
// message and a human message keep distinct provenance (T3.236, S4.014).
type ThreadMessage struct {
	Position       uint64 `json:"position"`
	Text           string `json:"text"`
	CreatedBy      string `json:"createdBy"`
	CreationSource string `json:"creationSource"`
}

// ThreadReadOutput is the response of t3_thread_read. The tool reads the newest
// part of one thread that fits in the character limit and returns it in
// ascending position order. HasMore is true when older messages remain.
// EarlierPosition is an opaque cursor for the next call; it is omitted when the
// whole history fits. CharCount counts the characters actually returned so a
// client can see why the page ended where it did (T3.236).
type ThreadReadOutput struct {
	ThreadID        string          `json:"threadID"`
	Messages        []ThreadMessage `json:"messages"`
	CharCount       int             `json:"charCount"`
	HasMore         bool            `json:"hasMore"`
	EarlierPosition string          `json:"earlierPosition,omitempty"`
}

// ThreadReadOutputSchema derives the schema of the opt-in t3_thread_read tool.
// It is intentionally outside outputSchemaFactories: the nine advertised tools
// are pinned byte-for-byte by contract_test.go and schema_test.go, so adding a
// tenth factory would break that contract without changing any handler.
func ThreadReadOutputSchema() (*jsonschema.Schema, error) {
	return OutputSchema[ThreadReadOutput]()
}

// OKOutput is the response of beacon_ack and beacon_cancel_watch.
type OKOutput struct {
	OK bool `json:"ok"`
}

// DeferWiringOutput is the response of beacon_defer_wiring (W12-B). Created is
// false for an idempotent repeat of the same (watchID, reason). CardMarkdown
// is the ready-to-commit content of docs/tasks/WIRING-<pr>.md.
type DeferWiringOutput struct {
	CardID       string `json:"cardID"`
	Created      bool   `json:"created"`
	Status       string `json:"status"`
	CardMarkdown string `json:"cardMarkdown"`
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

// StatsOutput is the response of beacon_stats. The seven original fields keep
// their names and types; the registry counters and blocked are additive
// (T5.60, T3.440). docs/adrs/0094-seven-tools.md pins every JSON field of this
// type. TestStatsDocListsEveryField fails when a field is added without the
// same change to that ADR, so update both in one edit.
type StatsOutput struct {
	PointsThisHour int `json:"pointsThisHour"`
	Budget         int `json:"budget"`
	ActiveWatches  int `json:"activeWatches"`
	// Blocked is the number of watches on the radar that carry a blocked
	// status or a block record. It is zero when no watch is blocked (T3.440).
	Blocked    int         `json:"blocked"`
	Persist    PersistInfo `json:"persist"`
	LoadErrors []string    `json:"loadErrors"`
	Ladder     LadderInfo  `json:"ladder"`
	Version    string      `json:"version"`
	// T5.60: registry counters. A handler without a RegistryStatsReader reports
	// zero and an empty config id, so the pre-T5.60 contract is unchanged.
	RegisteredAgents int    `json:"registeredAgents"`
	StaleAgents      int    `json:"staleAgents"`
	ConfigID         string `json:"configId"`
}

// OutputSchema derives the JSON schema for T from its Go type. This is the
// same reflection based inference go-sdk uses when it registers a typed tool
// handler, so an output schema can never drift from its struct definition.
func OutputSchema[T any]() (*jsonschema.Schema, error) {
	return jsonschema.For[T](nil)
}

// outputSchemaFactories pairs each tool name with its output type. Derived
// from toolDescriptors so a name cannot be registered without a schema factory.
var outputSchemaFactories = func() map[string]func() (*jsonschema.Schema, error) {
	out := make(map[string]func() (*jsonschema.Schema, error), len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		out[descriptor.Name] = descriptor.OutputType
	}
	return out
}()

// nullableMap relaxes a reflected map schema to accept JSON null. A nil Go map
// marshals to null, while reflection only infers "object", so the unadjusted
// schema rejects perfectly valid handler output. Only the type is widened;
// the schema of the map's values stays exactly the same.
func nullableMap(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if s.Type == "object" {
		s.Type = ""
		s.Types = []string{"null", "object"}
	}
}

// listRunningOutputSchema derives the list_running schema and accounts for the
// nil maps in a fresh Watch: domain.FlakyConfig.Used has no per-check counters
// and domain.Watch.Checks has no results yet, so the handler legitimately emits
// null for both. Those two fields accept null or an object of the same value
// types, nothing else.
func listRunningOutputSchema() (*jsonschema.Schema, error) {
	s, err := OutputSchema[ListRunningOutput]()
	if err != nil {
		return nil, err
	}
	watches := s.Properties["watches"]
	if watches == nil || watches.Items == nil {
		return s, nil
	}
	flaky := watches.Items.Properties["flaky"]
	if flaky != nil {
		nullableMap(flaky.Properties["used"])
	}
	// Checks is the other map on a Watch: an unexecuted Watch has no checks.
	nullableMap(watches.Items.Properties["checks"])
	return s, nil
}

// taskClaimOutputSchema derives the claim output schema from TaskClaimOutput,
// the type owned by IDX-05 in tool_task_claim.go. The top-level object is open
// to extra fields so a handler can add an additive marker without a schema
// change. Field schemas stay derived from the Go type.
func taskClaimOutputSchema() (*jsonschema.Schema, error) {
	s, err := OutputSchema[TaskClaimOutput]()
	if err != nil {
		return nil, err
	}
	s.AdditionalProperties = &jsonschema.Schema{}
	return s, nil
}

// taskCloseOutputSchema derives the close output schema from TaskCloseOutput,
// the type owned by tool_task_close.go. The top-level object stays open to
// extra fields, and the field schemas stay derived from the Go type.
func taskCloseOutputSchema() (*jsonschema.Schema, error) {
	s, err := OutputSchema[TaskCloseOutput]()
	if err != nil {
		return nil, err
	}
	s.AdditionalProperties = &jsonschema.Schema{}
	return s, nil
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

// StatusForCode returns the contract status for code, or 0 for an unknown
// code. The domain registry is the single source of truth; this boundary
// helper exists so MCP code does not reach into the domain table directly.
// Keeping it as a wrapper also removes the mirror that used to omit codes
// added after the contract v2 list, such as WATCH_EXPIRED and REJECT_DUPLICATE.
func StatusForCode(code domain.ErrorCode) int {
	return domain.StatusForCode(code)
}

// recoveryMeta holds the ADR 0046 repair fields that extend the six-field
// ADR 0009 envelope. The conversion core below produces it. No second
// converter builds these fields, so the package has one error-shape pipeline.
type recoveryMeta struct {
	retryable    bool
	recoveryHint string
	partial      PartialResult
}

// recoveryCarrier is implemented by errors that carry repair metadata. The
// conversion core reads the fields through this interface, so schema.go does
// not import the wake-specific RecoveryError type (ADR 0009, ADR 0046).
type recoveryCarrier interface {
	RecoveryFields() (bool, string, PartialResult)
}

// errorEnvelopeFrom is the one place that walks an error chain and builds the
// error shape. It resolves the AppError and its status once. When the chain
// also carries repair metadata, it returns those fields next to the six-field
// base. Both public converters read from here, so there is no duplicate
// conversion (BCX-69).
func errorEnvelopeFrom(err error) (ErrorShape, recoveryMeta, bool) {
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		return ErrorShape{}, recoveryMeta{}, false
	}

	status := StatusForCode(appErr.Code)
	if status == 0 {
		status = appErr.Status
	}

	base := ErrorShape{
		OK:           false,
		Code:         appErr.Code,
		Message:      appErr.Message,
		Status:       status,
		RetryAfterMs: appErr.RetryAfterMs,
		NewWatchID:   appErr.NewWatchID,
	}

	var meta recoveryMeta
	var carrier recoveryCarrier
	if errors.As(err, &carrier) {
		meta.retryable, meta.recoveryHint, meta.partial = carrier.RecoveryFields()
	}
	return base, meta, true
}

// ToErrorShape converts an error that wraps a *domain.AppError into the MCP
// error envelope. The bool is false when err carries no AppError, so callers
// can fall back to a generic protocol error. Code specific fields are carried
// through unchanged: HEAD_SHA_CHANGED keeps its newWatchID and RATE_LIMITED
// keeps its retryAfterMs as populated by the rate limit adapter.
//
// The live MCP path does not call this converter: handlers return the raw
// error and go-sdk turns it into an isError text result (ADR 0009 exception).
// This function and ErrorShape document the target envelope and are covered by
// schema_test.go. Wiring them into the live path changes the wire contract and
// needs an owner decision (ARDH-17).
func ToErrorShape(err error) (ErrorShape, bool) {
	base, _, ok := errorEnvelopeFrom(err)
	return base, ok
}
