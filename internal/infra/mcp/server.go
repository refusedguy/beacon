// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// The server is transport-agnostic: Run serves it over stdio, while tests drive
// the same instance over an in-memory transport (ADR 0003, ADR 0007).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// ServerName and ServerVersion identify Beacon in the MCP handshake.
	ServerName    = "beacon"
	ServerVersion = "v1.0.0"
)

// LegacyProtocolVersions is the legacy initialize-handshake version set.
// Version 2026-07-28 is deliberately absent: it drops the handshake in favour
// of a stateless model and carries the experimental tasks extension that v1
// does not use. See WORKER-PROMPT §2 and ADR 0003.
var LegacyProtocolVersions = []string{
	"2025-11-25",
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

// LegacyProtocolVersionsCopy returns a fresh copy of the legacy version set.
// The initialize response holds this copy, so a client that edits the returned
// slice cannot change the package global used by later handshakes (ARDH-22).
func LegacyProtocolVersionsCopy() []string {
	return slices.Clone(LegacyProtocolVersions)
}

// Task dialect capability keys. They are read from the client's capabilities
// at the MCP boundary; no protocol version is inferred and the handshake stays
// on LegacyProtocolVersions. A 2026-07-28 client declares the extension in the
// per-request _meta (io.modelcontextprotocol/clientCapabilities), while a
// 2025-11-25 client declared Tasks as an experimental capability at initialize.
const (
	clientCapabilitiesMetaKey = "io.modelcontextprotocol/clientCapabilities"
	tasksExtensionKey         = "io.modelcontextprotocol/tasks"
	tasksExperimentalKey      = "tasks"
)

// DialectFromRequest detects the task dialect of one tool call from the
// client's advertised capabilities. It never inspects the negotiated protocol
// version and never changes it: ADR 0003 and server.go exclude 2026-07-28
// from the handshake, so the extension form is an application-level response
// shape only. Absent any capability the client is treated as having no Tasks.
func DialectFromRequest(req *mcpsdk.CallToolRequest) TaskDialect {
	if req == nil {
		return DialectPlain
	}
	if req.Params != nil && req.Params.Meta != nil {
		if raw, ok := req.Params.Meta[clientCapabilitiesMetaKey]; ok {
			var caps struct {
				Extensions map[string]json.RawMessage `json:"extensions"`
			}
			if b, err := json.Marshal(raw); err == nil && json.Unmarshal(b, &caps) == nil {
				if _, ok := caps.Extensions[tasksExtensionKey]; ok {
					return DialectTasks2026
				}
			}
		}
	}
	if req.Session != nil {
		if params := req.Session.InitializeParams(); params != nil && params.Capabilities != nil {
			if _, ok := params.Capabilities.Experimental[tasksExperimentalKey]; ok {
				return DialectTasks2025
			}
		}
	}
	return DialectPlain
}

// InterruptReceiptFromRequest reports whether the caller can accept a cancel
// receipt. Absence of the flag keeps the legacy cancel contract: older hosts
// and the frozen eval suite omit it and still expect ok. A caller that
// explicitly declares interrupt_receipt_v1 as false cannot deliver the
// receipt, so it is refused before any write instead of losing the signal.
func InterruptReceiptFromRequest(req *mcpsdk.CallToolRequest) bool {
	if req == nil {
		return true
	}
	if req.Params != nil && req.Params.Meta != nil {
		if v, ok := req.Params.Meta[interruptReceiptCapability]; ok && receiptFalse(v) {
			return false
		}
		if raw, ok := req.Params.Meta[clientCapabilitiesMetaKey]; ok {
			if receiptFalseInExtensions(raw) {
				return false
			}
		}
	}
	if req.Session != nil {
		if params := req.Session.InitializeParams(); params != nil && params.Capabilities != nil {
			if v, ok := params.Capabilities.Experimental[interruptReceiptCapability]; ok && receiptFalse(v) {
				return false
			}
		}
	}
	return true
}

// receiptFalse reports an explicit boolean false. Any other value, including a
// bare presence marker or a JSON object, means the caller accepts a receipt.
func receiptFalse(v any) bool {
	b, ok := v.(bool)
	return ok && !b
}

// receiptFalseInExtensions reads the per-request extension map the 2026 dialect
// uses. The extension value is a raw JSON boolean when the caller opts out.
func receiptFalseInExtensions(raw any) bool {
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var caps struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if json.Unmarshal(b, &caps) != nil {
		return false
	}
	v, ok := caps.Extensions[interruptReceiptCapability]
	return ok && string(v) == "false"
}

// CycleInstructions is delivered to clients in the MCP handshake. The agent
// cycle lives here, not in a separate tool (WORKER-PROMPT §4.1).
const CycleInstructions = `Beacon cycle protocol:

1. Register or renew a watch with beacon_watch_pr and keep the returned watchID.
2. Finish your turn with the report "waiting, watchID". Do not poll in a loop.
3. Wake only on keep_working, rebase, retry or ready. Re-run failed checks with your own rights; Beacon is read-only on CI.
4. Read state with beacon_wait_result and beacon_get_result. Mark content read with beacon_ack.
5. The orchestrator merges only after beacon_get_result returns verdict=ready, idempotently by (watchID, revision). An idle child report is not done.
6. Close a finished or superseded watch with beacon_cancel_watch. beacon_list_running and beacon_stats are read-only.
7. Claim a todo card with beacon_task_claim before you work on it. The first claim marks the card in_progress and returns its context.
8. Close a card with beacon_task_close using a terminal status and a reason. A repeat with the same values changes nothing.
9. Record deferred wiring with beacon_defer_wiring when part of the work needs another agent's files.

Twelve tools: beacon_watch_pr, beacon_wait_result, beacon_get_result, beacon_list_running, beacon_ack, beacon_cancel_watch, beacon_stats, beacon_defer_wiring, beacon_task_claim, beacon_task_close, beacon_task_list, beacon_wake.

Self-teaching next action: beacon_get_result returns nextAction, nextCommand and nextHint. Branch on nextAction, not on prose.
  keep_working - do not rebase an unchanged head; fix only the failedChecks returned by beacon_get_result.
  rebase       - run nextCommand exactly: git fetch origin main && git rebase origin/main && git push --force-with-lease.
  retry        - run nextCommand exactly: gh run rerun --failed.
  blocked      - stop and wait for the blocker to be resolved; do not fix main yourself.
  wait         - no action yet; finish the turn with "waiting, <watchID>".

Verdicts: watching, ready, rebase, retry, keep_working, timeout, merged, closed. A watching-to-watching tick changes no revision and wakes nobody.`

// toolKind is the honest annotation class of one advertised MCP tool. A tool
// that writes readAt or claimedAt is additive, not read-only; cancel_watch is
// the only destructive tool. kindAdditiveOnce is additive but leaves the stub's
// idempotentHint unset, as beacon_task_claim does.
type toolKind int

const (
	kindAdditive toolKind = iota
	kindRead
	kindDestructive
	kindAdditiveOnce
)

// toolDescriptor is the single source of truth for one advertised MCP tool.
// Its fields feed ToolNames, toolAnnotations and outputSchemaFactories, so a
// tool cannot be present in some copies and absent from the rest. Handler calls
// the existing Attach helper; it does not replace the helper with a generic
// AddTool loop, because ADR 0263 forbids silent registration (ARDH-07).
type toolDescriptor struct {
	Name        string
	Description string
	OutputType  func() (*jsonschema.Schema, error)
	Kind        toolKind
	Handler     func(*mcpsdk.Server, Dependencies, map[string]*jsonschema.Schema, string)
}

// toolDescriptors lists exactly the advertised tools. Adding a name here is
// the only way to change the derived tables and the real-handler sequence.
// Opt-in tools such as t3_thread_read stay out: the advertised set has a
// byte-pinned tools/list contract that an eleventh name would break.
var toolDescriptors = []toolDescriptor{
	{
		Name:        "beacon_watch_pr",
		Description: "Register or renew a Watch on a pull request head and subscribe the caller.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[WatchPROutput]() },
		Kind:        kindAdditive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, _ map[string]*jsonschema.Schema, _ string) {
			handler := NewWatchPRHandler(deps.Store, deps.CI, deps.Clock)
			handler.SetBlockerIncidents(deps.Blockers)
			handler.SetFixerBinding(deps.FixerBinding)
			AttachWatchPR(s, handler)
		},
	},
	{
		Name:        "beacon_wait_result",
		Description: "Bounded long-poll for a Watch result; a terminal verdict marks the caller's obligation read. The watchID is a leased handle valid for 30 minutes; an expired handle returns WATCH_EXPIRED.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[WaitResultOutput]() },
		Kind:        kindAdditive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, _ map[string]*jsonschema.Schema, _ string) {
			AttachWaitResult(s, NewWaitResultServiceFromDeps(deps))
		},
	},
	{
		Name:        "beacon_get_result",
		Description: "Read a Watch result without logs and mark its content as read.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[GetResultOutput]() },
		Kind:        kindAdditive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, _ map[string]*jsonschema.Schema, _ string) {
			handler := NewGetResultHandler(deps.Store, deps.Stamper, deps.Clock)
			handler.SetAfter(deps.After)
			AttachGetResult(s, handler)
		},
	},
	{
		Name:        "beacon_list_running",
		Description: "List running Watches from a read-only state snapshot.",
		OutputType:  listRunningOutputSchema,
		Kind:        kindRead,
		Handler: func(s *mcpsdk.Server, deps Dependencies, schemas map[string]*jsonschema.Schema, _ string) {
			attachListRunning(s, deps.Snapshot, schemas["beacon_list_running"])
		},
	},
	{
		Name:        "beacon_ack",
		Description: "Record the intention to claim one obligation by stamping claimedAt; idempotent. This is an acknowledgement, not a result: read the verdict with beacon_get_result or beacon_wait_result. With decision=approve or decision=reject it resumes a stopped agent once in the same Watch. With decision=blocked:not_mine it blocks the Watch, recording the failed checks and the calling session as blocker. With decision=blocked:resolved it clears a confirmed block and returns the Watch to watching. The response reports resumed, resumeRevision, blocked and unblocked when a decision was applied; a plain ack returns only ok.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[OKOutput]() },
		Kind:        kindAdditive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, _ map[string]*jsonschema.Schema, _ string) {
			handler := NewAckHandler(deps.Store, deps.Stamper, deps.Clock)
			handler.SetBlockerIncidents(deps.Blockers)
			if deps.Continuations != nil {
				handler.SetContinuationRegistry(deps.Continuations)
			}
			if deps.GenerationLedger != nil {
				handler.SetGenerationStore(deps.GenerationLedger)
			}
			attachAckWithPolicy(s, handler, deps.ToolPolicy)
		},
	},
	{
		Name:        "beacon_cancel_watch",
		Description: "Cancel a Watch or one subscriber; idempotent, history is kept.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[OKOutput]() },
		Kind:        kindDestructive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, _ map[string]*jsonschema.Schema, _ string) {
			RegisterCancelWatch(s, NewCancelWatcher(deps.Store, deps.Events, deps.Clock))
		},
	},
	{
		Name:        "beacon_stats",
		Description: "Report persisted state counters from a read-only snapshot.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[StatsOutput]() },
		Kind:        kindRead,
		Handler: func(s *mcpsdk.Server, deps Dependencies, schemas map[string]*jsonschema.Schema, version string) {
			attachStats(s, deps, schemas["beacon_stats"], version)
		},
	},
	{
		Name:        "beacon_defer_wiring",
		Description: "Record a deferred wiring card for a PR, idempotent by (watchID, reason). Returns cardID, created, status and the ready markdown for docs/tasks/WIRING-<pr>.md.",
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[DeferWiringOutput]() },
		Kind:        kindAdditive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, _ map[string]*jsonschema.Schema, _ string) {
			AttachDeferWiring(s, NewDeferWiringHandler(deps.DeferWiring))
		},
	},
	{
		Name:        TaskClaimToolName,
		Description: "Atomically claim one todo task card for an agent and return its full context.",
		OutputType:  taskClaimOutputSchema,
		Kind:        kindAdditiveOnce,
		Handler: func(s *mcpsdk.Server, deps Dependencies, schemas map[string]*jsonschema.Schema, _ string) {
			attachTaskClaim(s, deps, schemas[TaskClaimToolName])
		},
	},
	{
		Name:        TaskCloseToolName,
		Description: "Close one task card with a terminal status and a reason. Idempotent by (cardID, status, reason): a repeat with the same values returns ok without a second write. A card held in_progress by another agent can only be closed by its owner.",
		OutputType:  taskCloseOutputSchema,
		Kind:        kindAdditive,
		Handler: func(s *mcpsdk.Server, deps Dependencies, schemas map[string]*jsonschema.Schema, _ string) {
			attachTaskClose(s, NewTaskCloseHandler(deps.TaskCloseStore), schemas[TaskCloseToolName])
		},
	},
	{
		Name:        TaskListToolName,
		Description: taskListDescription,
		OutputType:  func() (*jsonschema.Schema, error) { return OutputSchema[TaskListOutput]() },
		Kind:        kindRead,
		Handler: func(s *mcpsdk.Server, deps Dependencies, schemas map[string]*jsonschema.Schema, _ string) {
			attachTaskList(s, deps, schemas[TaskListToolName])
		},
	},
}

// ToolNames is the exact set of advertised tool names. T2.1 registers stubs;
// each real handler replaces its stub when wired. Derived from toolDescriptors
// so the name cannot drift from the annotations, schema or handler tables.
var ToolNames = func() []string {
	names := make([]string, 0, len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		names = append(names, descriptor.Name)
	}
	return names
}()

// AdvertisedToolNames returns the exact set of names the production server
// advertises. It adds the opt-in wake tool to the descriptor-backed ToolNames.
// Consumers that assert the tools/list contract derive their expected set from
// this function, so a new registration cannot stay local to one copy.
func AdvertisedToolNames() []string {
	names := make([]string, 0, len(ToolNames)+1)
	names = append(names, ToolNames...)
	return append(names, WakeToolName)
}

// ToolNamesCopy returns a fresh copy of the advertised tool names. Callers that
// sort, append or otherwise edit the result cannot change the global registry
// used by the init check and the MCP client (ARDH-22).
func ToolNamesCopy() []string {
	return slices.Clone(ToolNames)
}

// ToolPolicy separates the two tool lists the Claude Agent SDK keeps apart
// (CSx-04; types.py:2433-2453 and :2522-2527 of anthropics/claude-agent-sdk-python
// at 3184360). disallowedTools removes names from the advertised set, while
// allowedTools marks names that run without a permission request. Beacon used
// to derive both from toolDescriptors, so narrowing one silently narrowed the
// other. The two lists are now independent: limiting tools/list never changes
// the ack decision rule, and an auto-allowed tool stays advertised unless it is
// also disallowed.
type ToolPolicy struct {
	allowedTools    []string
	disallowedTools []string
}

// NewToolPolicy builds a policy from the auto-allowed and unavailable tool
// names. Both slices are cloned so a caller cannot mutate the policy after a
// server has been built from it (ARDH-22). An empty disallowed list advertises
// every declared tool; an empty allowed list keeps ack's human-decision path.
func NewToolPolicy(allowedTools, disallowedTools []string) ToolPolicy {
	return ToolPolicy{
		allowedTools:    slices.Clone(allowedTools),
		disallowedTools: slices.Clone(disallowedTools),
	}
}

// availableToolNames returns the declared names minus the policy's
// disallowedTools. With no disallowed names it returns a full copy of the
// declared set, so the byte-pinned tools/list contract is unchanged.
func (p ToolPolicy) availableToolNames() []string {
	names := ToolNamesCopy()
	if len(p.disallowedTools) == 0 {
		return names
	}
	blocked := make(map[string]struct{}, len(p.disallowedTools))
	for _, name := range p.disallowedTools {
		blocked[name] = struct{}{}
	}
	kept := names[:0]
	for _, name := range names {
		if _, found := blocked[name]; !found {
			kept = append(kept, name)
		}
	}
	return kept
}

// AutoAllowDecision reports the decision and reason for an ack that targets a
// tool in allowedTools. The third result is false when the tool still needs a
// human decision, which keeps the plain ack contract for every other tool.
func (p ToolPolicy) AutoAllowDecision(toolName string) (decision, reason string, ok bool) {
	for _, name := range p.allowedTools {
		if name == toolName {
			return string(DecisionApprove), "allowedTools:" + name, true
		}
	}
	return "", "", false
}

// ackToolMetaKey is the _meta key a caller uses to name the tool being
// acknowledged. It is parsed only at the MCP boundary; the core never sees it.
const ackToolMetaKey = "io.beacon/ackTool"

// autoAllowAck wraps the real ack handler so an ack for a tool in allowedTools
// is resolved without a human decision (CSx-04). A request that already carries
// a decision or a continueOf keeps its original meaning. The auto-allowed
// reason travels in decisionID so the recorded decision names its source.
type autoAllowAck struct {
	handler *AckHandler
	policy  ToolPolicy
}

func (a autoAllowAck) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in AckInput) (*mcpsdk.CallToolResult, OKOutput, error) {
	if in.Decision == "" && in.ContinueOf == "" && req != nil && req.Params != nil && req.Params.Meta != nil {
		if raw, present := req.Params.Meta[ackToolMetaKey]; present {
			if toolName, isString := raw.(string); isString {
				if decision, reason, ok := a.policy.AutoAllowDecision(toolName); ok {
					in.Decision = decision
					in.DecisionID = reason
				}
			}
		}
	}
	return a.handler.Handle(ctx, req, in)
}

// attachAckWithPolicy installs the real ack tool with the auto-allow wrapper.
// The description and annotations match AttachAck so the tools/list contract
// stays byte-identical; only the handler gains the policy rule.
func attachAckWithPolicy(s *mcpsdk.Server, handler *AckHandler, policy ToolPolicy) {
	if s == nil || handler == nil {
		return
	}
	s.RemoveTools("beacon_ack")
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "beacon_ack",
		Description: "Record the intention to claim one obligation by stamping claimedAt; idempotent. This is an acknowledgement, not a result: read the verdict with beacon_get_result or beacon_wait_result. With decision=approve or decision=reject it resumes a stopped agent once in the same Watch. With decision=blocked:not_mine it blocks the Watch, recording the failed checks and the calling session as blocker. With decision=blocked:resolved it clears a confirmed block and returns the Watch to watching. The response reports resumed, resumeRevision, blocked and unblocked when a decision was applied; a plain ack returns only ok.",
		Annotations: attachToolAnnotations(true),
	}, (&autoAllowAck{handler: handler, policy: policy}).Handle)
}

func boolPtr(b bool) *bool { return &b }

// annotationsForKind maps a descriptor's honest annotation class to the SDK
// annotations used by the stub registration. DestructiveHint is a pointer in
// the SDK, so it must be set explicitly even when false.
func annotationsForKind(kind toolKind) mcpsdk.ToolAnnotations {
	switch kind {
	case kindRead:
		return mcpsdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(false)}
	case kindDestructive:
		return mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(true)}
	case kindAdditiveOnce:
		return mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(false)}
	default:
		return mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true}
	}
}

// toolAnnotations records the honest annotation class of each advertised tool
// (WORKER-PROMPT §4.1). Derived from toolDescriptors so an annotation cannot
// drift from the name it belongs to. It is filled in init rather than by an
// initializer: the descriptor Handler closures call attach helpers that read
// this map, and an initializer would close a package initialization cycle.
var toolAnnotations map[string]mcpsdk.ToolAnnotations

func init() {
	toolAnnotations = make(map[string]mcpsdk.ToolAnnotations, len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		toolAnnotations[descriptor.Name] = annotationsForKind(descriptor.Kind)
	}
}

// ---------------------------------------------------------------------------
// CSx-05. In-process MCP bridge.
//
// The SDK's SdkMcpBridge (anthropics/claude-agent-sdk-python 3184360,
// src/claude_agent_sdk/_internal/sdk_mcp_bridge.py:343-379) dispatches a
// message straight into a server instance. Beacon keeps that shape behind a
// narrow port. A local handler receives the tool name and the raw arguments,
// then returns the same CallToolResult the HTTP client would. The remote path
// stays the default. The local one is opt-in through LocalMCPConfig.
// ---------------------------------------------------------------------------

// InProcessHandler is the port an embedding process implements to serve MCP
// tool calls inside its own address space. It mirrors SdkMcpBridge.handle:
// name plus raw arguments and meta in, CallToolResult out. No transport is
// implied, so this layer never imports net/http (ADR 0005).
type InProcessHandler interface {
	Handle(ctx context.Context, name string, arguments, meta json.RawMessage) (mcpsdk.CallToolResult, error)
}

// InProcessHandleFunc adapts a function to InProcessHandler. A test or a
// composition root can supply the bridge body without declaring a new type.
type InProcessHandleFunc func(ctx context.Context, name string, arguments, meta json.RawMessage) (mcpsdk.CallToolResult, error)

func (f InProcessHandleFunc) Handle(ctx context.Context, name string, arguments, meta json.RawMessage) (mcpsdk.CallToolResult, error) {
	if f == nil {
		return mcpsdk.CallToolResult{}, errors.New("mcp: nil in-process handler function")
	}
	return f(ctx, name, arguments, meta)
}

// AttachInProcessTools replaces the advertised handlers with local ones that
// delegate to bridge. It walks toolDescriptors, the same list the remote tools
// come from, so the local name set cannot drift from the advertised one.
// RemoveTools runs before each AddTool and a duplicate descriptor is rejected.
// A name therefore has one handler, and repeated calls change nothing.
func AttachInProcessTools(s *mcpsdk.Server, bridge *InProcessBridge) error {
	if s == nil {
		return errors.New("mcp: nil server")
	}
	if bridge == nil {
		return errors.New("mcp: nil in-process bridge")
	}
	outputs, err := AllOutputSchemas()
	if err != nil || outputs == nil {
		// Same fallback as AttachRemoteTools: a broken optional schema must not
		// leave a contracted tool unregistered.
		outputs = map[string]*jsonschema.Schema{
			"beacon_list_running": {Type: "object"},
			"beacon_stats":        {Type: "object"},
		}
	}
	seen := make(map[string]struct{}, len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		name := descriptor.Name
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("mcp: duplicate tool descriptor %q", name)
		}
		seen[name] = struct{}{}
		output := outputs[name]
		if output == nil {
			output = &jsonschema.Schema{Type: "object"}
		}
		description := remoteToolDescriptions[name]
		if description == "" {
			description = descriptor.Description
		}
		annotations := toolAnnotations[name]
		s.RemoveTools(name)
		s.AddTool(&mcpsdk.Tool{
			Name:         name,
			Description:  description,
			InputSchema:  remoteInputSchema(name),
			OutputSchema: output,
			Annotations:  &annotations,
		}, inProcessToolHandler(bridge, name))
	}
	return nil
}

func inProcessToolHandler(bridge *InProcessBridge, name string) mcpsdk.ToolHandler {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var arguments, meta json.RawMessage
		if req != nil && req.Params != nil {
			arguments = req.Params.Arguments
			if len(req.Params.Meta) > 0 {
				if raw, err := json.Marshal(req.Params.Meta); err == nil {
					meta = raw
				}
			}
		}
		result, err := bridge.CallTool(ctx, name, arguments, meta)
		if err != nil {
			// A tool failure belongs in Content with IsError set, so the agent
			// can see why the call failed and correct its next turn.
			out := &mcpsdk.CallToolResult{}
			out.SetError(fmt.Errorf("mcp: %s локально: %w", name, err))
			return out, nil
		}
		return &result, nil
	}
}

type stubInput struct{}

type stubOutput struct {
	OK bool `json:"ok" jsonschema:"true when the stub handled the call"`
}

func stubHandler(_ context.Context, _ *mcpsdk.CallToolRequest, _ stubInput) (*mcpsdk.CallToolResult, stubOutput, error) {
	return &mcpsdk.CallToolResult{}, stubOutput{OK: true}, nil
}

// ParentNotice is the projection of a child thread's question into the parent
// chat. It has the same wire shape as the server transport's notice, so one
// adapter at the composition root can serve both layers.
type ParentNotice struct {
	ParentID  string `json:"parentID"`
	ChildID   string `json:"childID"`
	ChildName string `json:"childName"`
	Question  string `json:"question"`
}

// ParentNotifier delivers a notice to a parent chat. It owns no chat client
// and no store. The composition root binds the real adapter.
type ParentNotifier interface {
	Notify(ctx context.Context, notice ParentNotice) error
}

// ChildThreadResolver resolves a child thread to its parent id and name. The
// server transport owns the reference store; this port lets the MCP layer use
// it without importing another infra package (ADR 0005).
type ChildThreadResolver interface {
	Resolve(childID string) (parentID, childName string, ok bool)
}

// ErrNoLineage reports that AskChildQuestion has no reference store to resolve
// the child, or that the question cannot be projected.
var ErrNoLineage = errors.New("mcp: child-thread lineage is not wired")

// AskChildQuestion projects a child's question into a parent notice and
// delivers it through the ports in deps. It is the MCP-side entry for a child
// agent's question and adds no tool, so the eight-tool contract stands. A nil
// notifier builds the notice without delivering it.
func (d Dependencies) AskChildQuestion(ctx context.Context, childID, question string) (ParentNotice, error) {
	if d.ChildThreads == nil {
		return ParentNotice{}, ErrNoLineage
	}
	parentID, childName, ok := d.ChildThreads.Resolve(childID)
	if !ok || strings.TrimSpace(question) == "" {
		return ParentNotice{}, ErrNoLineage
	}
	notice := ParentNotice{
		ParentID:  parentID,
		ChildID:   childID,
		ChildName: childName,
		Question:  strings.TrimSpace(question),
	}
	if d.ParentNotifier == nil {
		return notice, nil
	}
	if err := d.ParentNotifier.Notify(ctx, notice); err != nil {
		return notice, err
	}
	return notice, nil
}

// StateStore is the local aggregate of the two narrow domain state ports.
// Consumers that need both watches and obligations embed it; the concrete
// store is assembled once in the composition root and passed to every tool.
type StateStore interface {
	domain.WatchStore
	domain.ObligationStore
}

// Dependencies groups the ports the ten real handlers need. NewServer's
// zero-dependency form still registers every real handler; handlers that lack a
// backing port report a domain error instead of pretending to succeed. A
// composition root supplies the real ports through NewServerWithDeps.
//
// The ports are all declared inside internal/domain or this package, so wiring
// stays inside the layer matrix (ADR 0005): a caller in cmd or internal/app
// builds them from store/github and passes them in; this package never imports
// a sibling infra package.
type Dependencies struct {
	Store   StateStore
	CI      domain.CIProvider
	Stamper ObligationStamper
	Writer  WaitResultWriter
	Events  CancelEventLog
	Source  VerdictSource
	// Snapshot should fill StateSnapshot.LiveWatches, not only ActiveWatches,
	// so blocked Watches stay visible in beacon_list_running and beacon_stats.
	Snapshot SnapshotReader
	Clock    domain.Clock

	// Blockers is the durable incident lifecycle port. A nil value keeps the
	// legacy in-memory behaviour for callers that have not wired SQLite yet.
	Blockers domain.BlockerIncidents
	// FixerBinding binds a fixer Watch to an incident. Only watch_pr writes it.
	FixerBinding domain.FixerBinding

	// SharedCIWaiter is the gate for the expensive CI wait at the long-poll
	// entry (ADR 0037 p3). The composition root supplies it so this package
	// keeps only the port and never imports the store implementation
	// (ADR 0005). A nil value keeps the direct, non-coalescing default.
	SharedCIWaiter SharedCIWaiter

	// After is the MCP-boundary threshold that switches a slow response into
	// the client's task dialect form. Zero disables the switch. It belongs to
	// the boundary, never to the domain (ADR 0031 §2).
	After time.Duration

	// CallScopes binds wait_result calls to the calling agent's capability
	// context and departure channels, keyed by the opaque sessionID in _meta
	// (ADR 0040 §1). A nil value keeps the pre-T3.66 context-only behaviour.
	CallScopes *CapabilityScopeRegistry

	// PrecheckJournal and PrecheckReason carry T3.82's startup failure reason
	// into wait_result. The composition root builds the journal from app and
	// passes it here, so this package still imports neither app nor store
	// (ADR 0005).
	PrecheckJournal PrecheckJournal
	PrecheckReason  string

	// CIWait is the expensive operation run through SharedCIWaiter. It stays
	// nil until a composition root binds one to a concrete Watch; SetCIWait
	// then restores the honest no-op default instead of inventing a CI poll.
	CIWait func(context.Context) error

	// WaitResultIdempotency replaces the in-process result index when a
	// durable one exists. Nil is not passed to the setter: that would disable
	// idempotency. The in-memory default is a real process-lifetime store.
	WaitResultIdempotency WaitResultIdempotencyStore

	// Continuations shares ack's answer-round counter when another tool needs
	// to observe it. Nil keeps the handler's own real counter (ADR 0031 §3).
	Continuations *ContinuationRegistry

	// DeferWiring is the durable write side of beacon_defer_wiring (W12-B). A
	// nil value keeps the tool registered but it reports PERSIST_FAILED until a
	// composition root supplies the store port from W12-A.
	DeferWiring DeferWriter

	// TaskClaimStore is the card store behind beacon_task_claim (IDX-05). A nil
	// value keeps the tool registered, but its handler reports PERSIST_FAILED
	// until a composition root supplies the store port.
	TaskClaimStore TaskClaimStore

	// TaskCloseStore is the card store behind beacon_task_close. A nil value
	// keeps the tool registered, but its handler reports PERSIST_FAILED until a
	// composition root supplies the store port.
	TaskCloseStore TaskCloseStore

	// TaskListStore is the read side of the card store behind
	// beacon_task_list. Nil falls back to a lister over TaskClaimStore, so a
	// composition root that wires claim gets the list tool without a second
	// store. The tool never calls GitHub; callers pass excludeOwnerFiles.
	TaskListStore TaskListStore

	// ChildThreads and ParentNotifier carry the child-thread lineage wiring at
	// the MCP boundary. Task claim and task close are the two card tools, so the
	// question route stays a port pair instead of another tool. The composition
	// root supplies both from the server's reference store and the chat adapter.
	// A nil resolver keeps AskChildQuestion honest about the missing wiring.
	ChildThreads   ChildThreadResolver
	ParentNotifier ParentNotifier

	// GraphIndexReader and GenerationLedger are the ports from ADR 0314. A nil
	// graph reader keeps stats honest about an unverified index. A nil ledger
	// leaves the generation check off. RegistryStatsReader overrides the
	// snapshot-backed default that stats builds from Snapshot.
	GraphIndexReader    GraphIndexReader
	RegistryStatsReader RegistryStatsReader
	GenerationLedger    GenerationLedger

	// Version is the release stamp passed at server creation. An empty value
	// falls back to BuildVersion, the binary's build artifact. The description
	// of beacon_stats is the one place the version reaches tools/list.
	Version string

	// ToolPolicy carries the two independent tool lists from CSx-04. Its zero
	// value advertises every declared tool and leaves ack's human decisions
	// untouched, which preserves the existing tools/list contract.
	ToolPolicy ToolPolicy
}

// CapabilityScopeRegistry owns one cancellation scope per calling agent
// (ADR 0040 §1). The key is the opaque sessionID from the request _meta; the
// core never interprets it. A scope holds both the CapabilityContext (the task
// tree) and the three departure channels of CallerExit. The registry lives in
// this package and is injected by the composition root, so no bridge, app or
// store import is introduced (ADR 0005).
//
// With no registry wired, wait_result derives its departures from the handler
// context and binds no capability context, which is the original behaviour.
type CapabilityScopeRegistry struct {
	mu      sync.Mutex
	journal CancelJournal
	clock   domain.Clock
	scopes  map[CapabilityID]*capabilityScope
}

type capabilityScope struct {
	context          *CapabilityContext
	cancelled        chan struct{}
	sessionClosed    chan struct{}
	connectionBroken chan struct{}
}

// NewCapabilityScopeRegistry builds an empty registry. A nil journal and a nil
// clock fall back to the in-memory sink and wall clock used by
// NewCapabilityContext.
func NewCapabilityScopeRegistry(journal CancelJournal, clock domain.Clock) *CapabilityScopeRegistry {
	if journal == nil {
		journal = NewMemoryCancelJournal()
	}
	if clock == nil {
		clock = waitResultWallClock{}
	}
	return &CapabilityScopeRegistry{
		journal: journal,
		clock:   clock,
		scopes:  make(map[CapabilityID]*capabilityScope),
	}
}

// ScopeFor binds req to the calling agent's capability context. A request
// without a sessionID stays unbound, so a client that does not identify itself
// keeps the pre-capability wait.
func (r *CapabilityScopeRegistry) ScopeFor(req *mcpsdk.CallToolRequest) *CapabilityContext {
	scope := r.scopeFor(req)
	if scope == nil {
		return nil
	}
	return scope.context
}

// DeparturesFor returns the departure channels of the caller's capability
// scope. The handler context remains an independent select case in wait_result,
// so installing this provider never removes go-sdk cancellation on a broken
// connection or a cancelled call.
func (r *CapabilityScopeRegistry) DeparturesFor(req *mcpsdk.CallToolRequest) CallerExit {
	scope := r.scopeFor(req)
	if scope == nil {
		return CallerExit{}
	}
	return CallerExit{
		Cancelled:        scope.cancelled,
		SessionClosed:    scope.sessionClosed,
		ConnectionBroken: scope.connectionBroken,
	}
}

func (r *CapabilityScopeRegistry) scopeFor(req *mcpsdk.CallToolRequest) *capabilityScope {
	if r == nil {
		return nil
	}
	id := SessionIDFromRequest(req)
	if id == nil || *id == "" {
		return nil
	}
	owner := CapabilityID(*id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if scope, ok := r.scopes[owner]; ok {
		return scope
	}
	scope := &capabilityScope{
		context:          NewCapabilityContext(owner, r.journal, r.clock),
		cancelled:        make(chan struct{}),
		sessionClosed:    make(chan struct{}),
		connectionBroken: make(chan struct{}),
	}
	r.scopes[owner] = scope
	return scope
}

// Depart reports that the agent identified by owner disappeared. It closes the
// channel named by reason; wait_result's depart case then cancels the whole
// capability tree and journals the reason. Closing is idempotent, and a
// departure with no matching scope is ignored. An unknown reason is treated as
// a plain cancellation.
func (r *CapabilityScopeRegistry) Depart(owner CapabilityID, reason CancelReason) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	scope, ok := r.scopes[owner]
	if !ok {
		r.mu.Unlock()
		return false
	}
	var ch chan struct{}
	switch reason {
	case CancelSessionClosed:
		ch = scope.sessionClosed
	case CancelConnectionBroken:
		ch = scope.connectionBroken
	default:
		ch = scope.cancelled
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
	r.mu.Unlock()
	return true
}

// NewWaitResultServiceFromDeps builds the production wait_result service and
// applies every optional port in one place. NewServerWithDeps and the cmd
// composition root both use it, so replacing the tool's service at the MCP
// boundary cannot silently drop a setter again (T3.91).
func NewWaitResultServiceFromDeps(deps Dependencies) *WaitResultService {
	svc := NewWaitResultService(deps.Store, deps.Writer, deps.Source, deps.Clock)
	if deps.WaitResultIdempotency != nil {
		svc.SetIdempotencyStore(deps.WaitResultIdempotency)
	}
	if deps.CallScopes != nil {
		svc.SetDepartureProvider(deps.CallScopes.DeparturesFor)
		svc.SetCapabilityScope(deps.CallScopes.ScopeFor)
	}
	svc.SetCIWait(deps.CIWait)
	svc.SetSharedCIWaiter(deps.SharedCIWaiter)
	if deps.PrecheckJournal != nil {
		svc.SetPrecheckJournal(deps.PrecheckJournal)
	}
	svc.SetPrecheckFailure(deps.PrecheckReason)
	return svc
}

// NewServer builds the Beacon MCP server with all ten real tools registered
// and the cycle protocol attached to the handshake. With no dependencies it
// still exercises the real handlers; inject Dependencies with NewServerWithDeps
// to give them a persistent store and the other production ports.
func NewServer() *mcpsdk.Server {
	return NewServerWithDeps(Dependencies{})
}

// NewServerWithDeps builds the server and replaces every startup stub with the
// real handler from its Attach/Register helper. The PR #21 annotations are kept
// on the initial registration; each helper re-registers its tool with the same
// honest annotation class.
func NewServerWithDeps(deps Dependencies) *mcpsdk.Server {
	version := strings.TrimSpace(deps.Version)
	if version == "" {
		version = BuildVersion()
	}
	s := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: ServerName, Version: ServerVersion},
		&mcpsdk.ServerOptions{
			Instructions:              CycleInstructions,
			SupportedProtocolVersions: LegacyProtocolVersionsCopy(),
		},
	)
	// The available set is the declared names minus the policy's
	// disallowedTools. Registration is the only place tools/list is built, so
	// skipping a name here both hides it from tools/list and makes a call to it
	// an unknown-tool error (CSx-04). With the zero policy every declared tool
	// is registered and the byte-pinned contract is unchanged.
	available := deps.ToolPolicy.availableToolNames()
	advertised := make(map[string]struct{}, len(available))
	for _, name := range available {
		advertised[name] = struct{}{}
	}
	for _, descriptor := range toolDescriptors {
		if _, ok := advertised[descriptor.Name]; !ok {
			continue
		}
		annotations := toolAnnotations[descriptor.Name]
		mcpsdk.AddTool(s, &mcpsdk.Tool{Name: descriptor.Name, Annotations: &annotations}, stubHandler)
	}

	// The real read tools carry an explicit output schema. go-sdk cannot infer
	// one for these types and panics on a nil OutputSchema, so the schema comes
	// from AllOutputSchemas, which relaxes the null a fresh Watch emits for its
	// nil maps (ADR 0003, ADR 0009). A derivation failure is a broken contract
	// type: log it and fall back to a permissive object schema so every real
	// handler is still registered. The precise schema is optional; the real
	// tool is not. Returning here would leave all ten tools on startup stubs
	// (T3.59).
	schemas, err := AllOutputSchemas()
	if err != nil {
		log.Printf("mcp: output schemas: %v; read tools use permissive schemas", err)
		schemas = map[string]*jsonschema.Schema{
			"beacon_list_running": {Type: "object"},
			"beacon_stats":        {Type: "object"},
		}
	}

	for _, descriptor := range toolDescriptors {
		if _, ok := advertised[descriptor.Name]; !ok {
			continue
		}
		descriptor.Handler(s, deps, schemas, version)
	}
	return s
}

// attachListRunning installs the real read-only list_running tool. The handler
// is defined in tool_list_running.go; there is no separate Attach helper there,
// so the registration lives in this composition file. A nil reader falls back
// to an empty snapshot so the tool never panics before a caller wires a store.
// The caller supplies the schema from AllOutputSchemas so go-sdk never infers
// one from the handler's Go type. A nil schema means derivation failed; the
// real handler is registered anyway and go-sdk infers a schema, so a broken
// optional schema never leaves a stub behind (T3.59).
func attachListRunning(s *mcpsdk.Server, reader SnapshotReader, outputSchema *jsonschema.Schema) {
	if s == nil {
		return
	}
	if reader == nil {
		reader = emptySnapshotReader()
	}
	annotations := toolAnnotations["beacon_list_running"]
	s.RemoveTools("beacon_list_running")
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:         "beacon_list_running",
		Description:  "List running Watches from a read-only state snapshot.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, NewListRunningHandler(reader).Handle)
}

// attachStats installs the real read-only stats tool. Its handler is defined in
// tool_stats.go and has no separate Attach helper. The schema comes from
// AllOutputSchemas for the same reason as list_running; a nil schema still
// registers the real handler so the tool never falls back to a stub (T3.59).
// The version is the build stamp passed at server creation. It changes only the
// description; the name, order, schema and handler stay fixed.
func attachStats(s *mcpsdk.Server, deps Dependencies, outputSchema *jsonschema.Schema, version string) {
	if s == nil {
		return
	}
	reader := deps.Snapshot
	if reader == nil {
		reader = emptySnapshotReader()
	}
	handler := NewStatsHandler(reader)
	if deps.GraphIndexReader != nil {
		handler.SetGraphIndexReader(deps.GraphIndexReader)
	}
	registry := deps.RegistryStatsReader
	if registry == nil {
		registry = NewSnapshotRegistryStats(reader)
	}
	handler.SetRegistryStatsReader(registry)
	annotations := toolAnnotations["beacon_stats"]
	s.RemoveTools("beacon_stats")
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:         "beacon_stats",
		Description:  DescriptionWithVersion("Report persisted state counters from a read-only snapshot.", version),
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, handler.Handle)
}

// attachTaskClaim installs the ninth tool over the handler owned by IDX-05 in
// tool_task_claim.go. It builds the handler from deps so the graph index port
// reaches the claim path and graphReady reflects a verified index (AUD-04
// part3). This file adds the registration and the explicit output schema, not
// the claim logic. A nil schema means derivation failed; go-sdk panics when a
// typed handler gets nil, so a permissive object keeps the real handler
// registered just like the fallback for list_running and stats.
func attachTaskClaim(s *mcpsdk.Server, deps Dependencies, outputSchema *jsonschema.Schema) {
	if s == nil {
		return
	}
	handler := NewTaskClaimHandler(deps.TaskClaimStore)
	if deps.GraphIndexReader != nil {
		handler.SetGraphIndexReader(deps.GraphIndexReader)
	}
	if outputSchema == nil {
		outputSchema = &jsonschema.Schema{Type: "object"}
	}
	annotations := toolAnnotations["beacon_task_claim"]
	s.RemoveTools(TaskClaimToolName)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:         TaskClaimToolName,
		Description:  "Atomically claim one todo task card for an agent and return its full context.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, handler.Handle)
}

// attachTaskClose installs beacon_task_close over the handler defined in
// tool_task_close.go. The output schema is passed from AllOutputSchemas so
// go-sdk never infers one from the handler type. A nil schema keeps the real
// handler registered with a permissive object, as the other tools do.
func attachTaskClose(s *mcpsdk.Server, handler *TaskCloseHandler, outputSchema *jsonschema.Schema) {
	if s == nil || handler == nil {
		return
	}
	if outputSchema == nil {
		outputSchema = &jsonschema.Schema{Type: "object"}
	}
	annotations := toolAnnotations[TaskCloseToolName]
	s.RemoveTools(TaskCloseToolName)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:         TaskCloseToolName,
		Description:  "Close one task card with a terminal status and a reason. Idempotent by (cardID, status, reason): a repeat with the same values returns ok without a second write. A card held in_progress by another agent can only be closed by its owner.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, handler.Handle)
}

// attachTaskList installs beacon_task_list over the handler defined in
// tool_task_list.go. When no list port is supplied it reuses the file-backed
// claim store, so the tool reads the same cards as beacon_task_claim without
// requiring a change in the composition root. A nil output schema keeps the
// real handler registered with a permissive object.
func attachTaskList(s *mcpsdk.Server, deps Dependencies, outputSchema *jsonschema.Schema) {
	if s == nil {
		return
	}
	var store TaskListStore = deps.TaskListStore
	if store == nil {
		if claim, ok := deps.TaskClaimStore.(*FileTaskClaimStore); ok {
			store = &ClaimStoreTaskLister{Claim: claim}
		}
	}
	AttachTaskList(s, NewTaskListHandler(store), outputSchema)
}

// AttachThreadRead installs t3_thread_read as an opt-in read-only tool. The
// name is deliberately absent from ToolNames and outputSchemaFactories: the
// ten advertised tools have a byte-for-byte pinned tools/list contract, so an
// eleventh tool must be attached by a composition root that needs thread history.
// A nil source leaves the server unchanged, and a nil schema falls back to the
// derived one, so a broken optional schema cannot suppress the real handler.
func AttachThreadRead(server *mcpsdk.Server, source ThreadHistorySource, outputSchema *jsonschema.Schema) {
	if server == nil || source == nil {
		return
	}
	if outputSchema == nil {
		derived, err := ThreadReadOutputSchema()
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
	server.RemoveTools(ThreadReadToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         ThreadReadToolName,
		Description:  "Read a bounded tail of one thread history with an earlierPosition continuation and provenance on every message.",
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, NewThreadReadHandler(source).Handle)
}

// emptySnapshotReader is the minimal dependency used when no SnapshotReader is
// injected. It is a real SnapshotFunc, not a stub handler: the observability
// tools still run their own projection over the empty state.
func emptySnapshotReader() SnapshotReader {
	return SnapshotFunc(func() (StateSnapshot, error) { return StateSnapshot{}, nil })
}

// EnsureStateDir creates the Beacon state directory before the store opens its
// SQLite file. The filesystem call lives in infra: ADR 0005 keeps file
// capabilities out of cmd, so the composition root only passes a path.
func EnsureStateDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("mcp: empty state directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mcp: state directory %s: %w", dir, err)
	}
	return nil
}

// RunWithDeps serves MCP over stdin/stdout with the supplied production ports.
// The composition root builds them from the real store and calls this instead
// of Run, so every tool receives a non-nil Store.
func RunWithDeps(ctx context.Context, deps Dependencies) error {
	return NewServerWithDeps(deps).Run(ctx, &mcpsdk.StdioTransport{})
}

// Run serves MCP over stdin/stdout with no injected ports. Kept for callers
// that only need the zero-dependency server; `beacon mcp` uses RunWithDeps.
func Run(ctx context.Context) error {
	return RunWithDeps(ctx, Dependencies{})
}
