// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file derives tool output schemas from the Go output types and defines
// the single error envelope returned to MCP clients (ADR 0003, ADR 0009).
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// FailureExcerptUnavailable reports that the excerpt source failed for this
	// check. An empty FailureExcerpt alone cannot separate a failed fetch from
	// a check with no annotations, so the flag is emitted only on failure
	// (EH-02). It is optional to keep the wire shape stable for callers that
	// never configure a source.
	FailureExcerptUnavailable bool `json:"failureExcerptUnavailable,omitempty"`
}

// LinterSeverity is the weight class of one linter remark (CA-15). A fatal
// remark breaks parsing or execution and belongs in the blocking channel. An
// informational remark is style only and stays out of the wake path.
type LinterSeverity string

const (
	LinterSeverityFatal         LinterSeverity = "fatal"
	LinterSeverityInformational LinterSeverity = "informational"
)

// fatalLinterCodes are the flake8 codes aider selects as fatal
// (aider/linter.py:136-168). E9 is a family prefix, so ClassifyLinterSeverity
// treats every E9xx code as fatal. The other codes are matched exactly:
// undefined names (F821, F823, F831), misplaced imports (F406, F407) and
// break/continue or return errors (F701, F702, F704, F706).
var fatalLinterCodes = map[string]struct{}{
	"F821": {}, "F823": {}, "F831": {},
	"F406": {}, "F407": {},
	"F701": {}, "F702": {}, "F704": {}, "F706": {},
}

// ClassifyLinterSeverity maps one linter code to its severity. The rule is a
// pure function so the local pre-pass and any reporting channel share one
// decision. Codes outside the fatal set are informational: a remark that
// cannot break parsing or execution must not wake an executor.
func ClassifyLinterSeverity(code string) LinterSeverity {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if strings.HasPrefix(normalized, "E9") {
		return LinterSeverityFatal
	}
	if _, ok := fatalLinterCodes[normalized]; ok {
		return LinterSeverityFatal
	}
	return LinterSeverityInformational
}

// LinterRemark is one finding handed to the local pre-pass before it is
// routed to a channel.
type LinterRemark struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// SplitLinterRemarks partitions linter remarks into a blocking and an
// informational list. fatalOnly is the local pre-pass flag: when true, only
// fatal codes reach the blocking channel and every other remark is marked
// informational, so it is reported without a wake. With fatalOnly false the
// pre-pass keeps the baseline where every remark is blocking. The failedChecks
// fields required and flakyMatched are not changed by this split.
func SplitLinterRemarks(remarks []LinterRemark, fatalOnly bool) (blocking, informational []LinterRemark) {
	for _, remark := range remarks {
		if fatalOnly && ClassifyLinterSeverity(remark.Code) != LinterSeverityFatal {
			informational = append(informational, remark)
			continue
		}
		blocking = append(blocking, remark)
	}
	return blocking, informational
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

	// FailureExcerptUnavailable reports that at least one check's excerpt fetch
	// failed, so the aggregate is incomplete. The matching failedChecks entry
	// carries the per-check flag (EH-02). Optional for wire stability: a Watch
	// whose caller has no excerpt source never sets it.
	FailureExcerptUnavailable bool `json:"failureExcerptUnavailable,omitempty"`

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

// The beacon_list_running response declares its watch shape in this package.
// Before BCX-45 the output schema reflected domain.Watch, so a field added to
// the domain reached tools/list without a decision here. Each DTO below mirrors
// a domain type field for field. Exposing a new domain field now means editing
// the DTO, its conversion and the golden schema in the same change.
type CheckOutput struct {
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
	Required   bool   `json:"required"`
	DetailsURL string `json:"detailsUrl"`
}

type MergeInfoOutput struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
}

type FlakyConfigOutput struct {
	Names      []string       `json:"names"`
	MaxRetries int            `json:"maxRetries"`
	Used       map[string]int `json:"used"`
}

type PolicyOutput struct {
	RequireUpToDate  bool `json:"requireUpToDate"`
	IgnoreReviews    bool `json:"ignoreReviews"`
	MaxWakesPerWatch int  `json:"maxWakesPerWatch"`
	MaxRebases       int  `json:"maxRebases,omitempty"`
}

type MergePolicyOutput struct {
	AutoMerge       bool     `json:"autoMerge"`
	RequireApproval bool     `json:"requireApproval"`
	WaitFor         []string `json:"waitFor"`
	WaitForMain     []string `json:"waitForMain"`
}

type SubscriberOutput struct {
	ID             string             `json:"id"`
	Role           domain.Role        `json:"role"`
	Adapter        string             `json:"adapter"`
	Target         string             `json:"target"`
	OrchestratorID string             `json:"orchestratorID,omitempty"`
	FixFor         string             `json:"fixFor,omitempty"`
	Phase          domain.WorkerPhase `json:"phase,omitempty"`
	LastWakeAt     time.Time          `json:"lastWakeAt,omitempty"`
	LastError      string             `json:"lastError,omitempty"`
	AddedAt        time.Time          `json:"addedAt"`
	LastActivity   time.Time          `json:"lastActivity"`
}

type BlockedInfoOutput struct {
	Reason       string    `json:"reason"`
	FailedChecks []string  `json:"failedChecks,omitempty"`
	Since        time.Time `json:"since"`
	BlockedBy    string    `json:"blockedBy"`
	FixAttempts  int       `json:"fixAttempts,omitempty"`
}

type DescentOutput struct {
	Layer      domain.DepthLayer `json:"layer"`
	Hypothesis string            `json:"hypothesis,omitempty"`
	Falsifier  string            `json:"falsifier,omitempty"`
	Evidence   string            `json:"evidence,omitempty"`
	Steps      int               `json:"steps"`
}

// WatchOutput is one watch row of beacon_list_running. It repeats the JSON
// field names and types of domain.Watch, but the field set lives here. A field
// added to the domain stays out of tools/list until someone adds it here,
// converts it and updates the golden schema.
type WatchOutput struct {
	ID             string                 `json:"id"`
	Repo           string                 `json:"repo"`
	PR             int                    `json:"pr"`
	HeadSHA        string                 `json:"headSHA"`
	BaseRef        string                 `json:"baseRef"`
	Issue          string                 `json:"issue"`
	Status         domain.Status          `json:"status"`
	Verdict        domain.Verdict         `json:"verdict"`
	Blocked        *BlockedInfoOutput     `json:"blocked,omitempty"`
	Revision       uint64                 `json:"revision"`
	Checks         map[string]CheckOutput `json:"checks"`
	Merge          MergeInfoOutput        `json:"merge"`
	Flaky          FlakyConfigOutput      `json:"flaky"`
	Policy         PolicyOutput           `json:"policy"`
	MergePolicy    MergePolicyOutput      `json:"mergePolicy"`
	Subscribers    []SubscriberOutput     `json:"subscribers"`
	OrchestratorID string                 `json:"orchestratorID,omitempty"`
	FixFor         string                 `json:"fixFor,omitempty"`
	BlockerID      string                 `json:"blockerID,omitempty"`
	FixBlockerID   string                 `json:"fixBlockerID,omitempty"`
	DeadlineAt     time.Time              `json:"deadlineAt"`
	NoChecksSince  time.Time              `json:"noChecksSince"`
	CreatedAt      time.Time              `json:"createdAt"`
	ClosedAt       time.Time              `json:"closedAt"`
	CloseReason    string                 `json:"closeReason"`
	ConfigID       string                 `json:"configId,omitempty"`
	Depth          *DescentOutput         `json:"depth,omitempty"`
}

// ListRunningOutput is the response of beacon_list_running. Status and Cursor
// replace a replayed history: a subscriber that lost its connection continues
// from Cursor and receives only WatchEvent values newer than it. Once Status
// is terminal, Verdict is set and Events is empty, so the verdict is obtained
// without replaying old events (ADR 0025).
type ListRunningOutput struct {
	Counts  WatchCounts   `json:"counts"`
	Watches []WatchOutput `json:"watches"`
	Status  string        `json:"status"`
	Cursor  string        `json:"cursor"`
	Events  []WatchEvent  `json:"events,omitempty"`
	Verdict string        `json:"verdict,omitempty"`
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

// OKOutput is the response of beacon_ack and beacon_cancel_watch. A plain ack
// marshals to exactly {"ok":true}: the decision fields have omitempty and stay
// absent. A decision ack also reports which effect it applied, so the caller
// can tell the first application from a repeat without reading the Watch.
type OKOutput struct {
	OK bool `json:"ok"`
	// Resumed is true only when this ack resumed a stopped agent. A repeat of
	// the same decision leaves it false.
	Resumed bool `json:"resumed,omitempty"`
	// ResumeRevision is the Watch revision after the resume. It is zero when
	// this ack applied no decision.
	ResumeRevision uint64 `json:"resumeRevision,omitempty"`
	// Blocked is true when this ack applied a blocked:not_mine decision.
	Blocked bool `json:"blocked,omitempty"`
	// Unblocked is true when this ack applied a blocked:resolved decision to a
	// blocked Watch.
	Unblocked bool `json:"unblocked,omitempty"`
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

// StatsOutcomeSchemaVersion is the version of the disjoint outcome report
// published by beacon_stats. Version 2 keeps every Watch in exactly one main
// list and records failure classes as additive lists (SWE-bench reporting.py).
const StatsOutcomeSchemaVersion = 2

// OutcomeReport is the run outcome derived from one Beacon snapshot. The eight
// ID lists follow swebench/harness/reporting.py:35-46 and :139-160. A Watch
// belongs to exactly one of the four main lists: completed, incomplete,
// resolved or unresolved. The four failure lists are additive. A classified
// Watch stays in its main list, so classification never changes a denominator.
//
// The report is a projection, not a second store. It is rendered once into the
// stats response and read back from there by OutcomeReportFrom, which keeps
// beacon_stats and any metrics reader on the same values.
type OutcomeReport struct {
	SchemaVersion       int      `json:"schemaVersion"`
	CompletedIDs        []string `json:"completed_ids"`
	IncompleteIDs       []string `json:"incomplete_ids"`
	ResolvedIDs         []string `json:"resolved_ids"`
	UnresolvedIDs       []string `json:"unresolved_ids"`
	ErrorIDs            []string `json:"error_ids"`
	EmptyPatchIDs       []string `json:"empty_patch_ids"`
	InfraFailureIDs     []string `json:"infra_failure_ids"`
	AmbiguousFailureIDs []string `json:"ambiguous_failure_ids"`
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
// extra fields, and the field schemas stay derived from the Go type. The
// created flag is reflected from the struct, so no hand-written property is
// needed here; regenerating the tools/list golden picks it up.
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

// The multi-agent tool model (CCx-22). Codex carries a namespace into a tool
// description and keeps the encrypted marker on parameters when it substitutes
// a child tool's schema. Beacon's advertised tools stay flat, so these helpers
// build descriptions only. Nothing here feeds outputSchemaFactories.
const (
	namespaceKeyword = "namespace"
	encryptedKeyword = "encrypted"
)

// NamespacedTool is one child tool inside a tool namespace. Schema is the
// parameter schema of that child tool.
type NamespacedTool struct {
	Name   string             `json:"name"`
	Schema *jsonschema.Schema `json:"schema,omitempty"`
}

// ToolNamespace groups child tools under a name in a tool description. The
// namespace is additive metadata: a client that ignores the custom keyword
// keeps the flat ten-tool contract.
type ToolNamespace struct {
	Name  string           `json:"name"`
	Tools []NamespacedTool `json:"tools,omitempty"`
}

// setSchemaExtra writes a custom keyword on s without touching validation.
func setSchemaExtra(s *jsonschema.Schema, key string, value any) {
	if s == nil {
		return
	}
	if s.Extra == nil {
		s.Extra = make(map[string]any)
	}
	s.Extra[key] = value
}

// schemaExtra reads a custom keyword from s. A schema that travelled through
// JSON holds the value as a generic map, so the typed and decoded forms are
// both accepted.
func schemaExtra[T any](s *jsonschema.Schema, key string) (T, bool) {
	var zero T
	if s == nil || s.Extra == nil {
		return zero, false
	}
	raw, ok := s.Extra[key]
	if !ok {
		return zero, false
	}
	if typed, ok := raw.(T); ok {
		return typed, true
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return zero, false
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return zero, false
	}
	return out, true
}

// SetToolNamespace attaches a namespace description to a tool schema. The
// namespace lives in a custom keyword, so the validation rules and the shape
// of the advertised tool are unchanged.
func SetToolNamespace(s *jsonschema.Schema, ns ToolNamespace) {
	setSchemaExtra(s, namespaceKeyword, ns)
}

// ToolNamespaceOf returns the namespace attached to a tool schema, if any.
func ToolNamespaceOf(s *jsonschema.Schema) (ToolNamespace, bool) {
	return schemaExtra[ToolNamespace](s, namespaceKeyword)
}

// MarkParameterEncrypted marks one property of s as encrypted. An encrypted
// argument must reach a spawned agent unchanged (CCx-22). The bool reports
// whether the named property exists.
func MarkParameterEncrypted(s *jsonschema.Schema, name string) bool {
	if s == nil || s.Properties == nil || s.Properties[name] == nil {
		return false
	}
	setSchemaExtra(s.Properties[name], encryptedKeyword, true)
	return true
}

// ParameterEncrypted reports whether property name of s carries the encrypted
// marker.
func ParameterEncrypted(s *jsonschema.Schema, name string) bool {
	if s == nil || s.Properties == nil {
		return false
	}
	encrypted, ok := schemaExtra[bool](s.Properties[name], encryptedKeyword)
	return ok && encrypted
}

// cloneToolSchema copies a schema through its JSON form. The library restores
// custom keywords into Extra during unmarshal; PropertyOrder needs an explicit
// copy because it is not part of the JSON form.
func cloneToolSchema(s *jsonschema.Schema) (*jsonschema.Schema, error) {
	if s == nil {
		return nil, nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out jsonschema.Schema
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	out.PropertyOrder = append([]string(nil), s.PropertyOrder...)
	return &out, nil
}

// SubstituteToolSchema returns a copy of override that inherits the namespace
// and the encrypted parameter markers of base. Parameters introduced only by
// override keep their own markers. Without the copy, an encrypted argument is
// substituted as a plain one and never reaches the spawned agent (CCx-22).
func SubstituteToolSchema(base, override *jsonschema.Schema) (*jsonschema.Schema, error) {
	out, err := cloneToolSchema(override)
	if err != nil || out == nil {
		return out, err
	}
	if ns, ok := ToolNamespaceOf(base); ok {
		SetToolNamespace(out, ns)
	}
	for name, prop := range out.Properties {
		if prop == nil || base == nil || base.Properties[name] == nil {
			continue
		}
		if encrypted, ok := schemaExtra[bool](base.Properties[name], encryptedKeyword); ok && encrypted {
			setSchemaExtra(prop, encryptedKeyword, true)
		}
	}
	return out, nil
}
