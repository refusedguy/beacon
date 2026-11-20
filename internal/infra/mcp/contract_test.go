package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// toolsListByteBudget is the ceiling for the serialized tools/list result.
// The budget counts real bytes (method Tirith), not the presence of fields.
// 16 KiB still fits eight detailed input/output schemas while catching runaway
// growth in descriptions and schemas. CI logs the measured size next to the
// ceiling so drift is visible before it becomes a failure.
const toolsListByteBudget = 16 * 1024

// expectedToolNames is the complete tool contract: the seven tools from
// WORKER-PROMPT 4.1 plus beacon_defer_wiring (W12-B). The order is not part of
// the contract, so the test compares sorted slices.
var expectedToolNames = []string{
	"beacon_watch_pr",
	"beacon_wait_result",
	"beacon_get_result",
	"beacon_list_running",
	"beacon_ack",
	"beacon_cancel_watch",
	"beacon_stats",
	"beacon_defer_wiring",
}

// toolKind is the annotation class assigned to a tool in WORKER-PROMPT 4.1.
type toolKind int

const (
	kindAdditive toolKind = iota
	kindRead
	kindDestructive
)

// expectedToolKinds maps every contracted tool to its honest annotation class.
// A tool that writes readAt or claimedAt is additive, not read-only: the
// distinction is the lesson from Tirith ADR-0033/0034 recorded in the audit.
var expectedToolKinds = map[string]toolKind{
	"beacon_watch_pr":     kindAdditive,
	"beacon_wait_result":  kindAdditive,
	"beacon_get_result":   kindAdditive,
	"beacon_list_running": kindRead,
	"beacon_ack":          kindAdditive,
	"beacon_cancel_watch": kindDestructive,
	"beacon_stats":        kindRead,
	"beacon_defer_wiring": kindAdditive,
}

// listAllTools walks every tools/list page and returns the tools together with
// the number of bytes produced by serializing each response. Counting every
// page keeps the "exactly eight, no ninth" check honest if the server paginates.
func listAllTools(t *testing.T, session *sdkmcp.ClientSession) ([]*sdkmcp.Tool, int) {
	t.Helper()

	var tools []*sdkmcp.Tool
	totalBytes := 0
	cursor := ""
	seen := make(map[string]bool)

	for {
		params := &sdkmcp.ListToolsParams{}
		if cursor != "" {
			params.Cursor = cursor
		}

		response, err := session.ListTools(context.Background(), params)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}

		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal tools/list response: %v", err)
		}
		totalBytes += len(raw)

		tools = append(tools, response.Tools...)
		if response.NextCursor == "" {
			return tools, totalBytes
		}
		if seen[response.NextCursor] {
			t.Fatalf("tools/list pagination loop: cursor %q repeated", response.NextCursor)
		}
		seen[response.NextCursor] = true
		cursor = response.NextCursor
	}
}

func TestToolsListContract(t *testing.T) {
	server := NewServer()
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "beacon-contract-test", Version: "v0.0.0"},
		nil,
	)

	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	tools, responseBytes := listAllTools(t, clientSession)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	wantNames := slices.Clone(expectedToolNames)
	slices.Sort(wantNames)
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tools/list names = %v, want exactly %v", names, wantNames)
	}

	byName := make(map[string]*sdkmcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	for _, name := range expectedToolNames {
		tool := byName[name]
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", name)
			continue
		}
		schemaJSON, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Errorf("%s: marshal output schema: %v", name, err)
			continue
		}
		if len(schemaJSON) == 0 || string(schemaJSON) == "null" || string(schemaJSON) == "{}" {
			t.Errorf("%s: output schema is empty: %s", name, schemaJSON)
		}

		annotations := tool.Annotations
		if annotations == nil {
			t.Errorf("%s: missing annotations", name)
			continue
		}
		switch expectedToolKinds[name] {
		case kindAdditive:
			if annotations.ReadOnlyHint {
				t.Errorf("%s: additive tool must not be read-only", name)
			}
			if annotations.DestructiveHint == nil || *annotations.DestructiveHint {
				t.Errorf("%s: additive tool must set destructiveHint=false", name)
			}
		case kindRead:
			if !annotations.ReadOnlyHint {
				t.Errorf("%s: read tool must set readOnlyHint=true", name)
			}
		case kindDestructive:
			if annotations.ReadOnlyHint {
				t.Errorf("%s: destructive tool must not be read-only", name)
			}
			if annotations.DestructiveHint == nil || !*annotations.DestructiveHint {
				t.Errorf("%s: destructive tool must set destructiveHint=true", name)
			}
		}
	}

	t.Logf("tools/list serialized to %d bytes (budget %d)", responseBytes, toolsListByteBudget)
	if responseBytes > toolsListByteBudget {
		t.Errorf("tools/list response is %d bytes, over the %d byte budget", responseBytes, toolsListByteBudget)
	}
}

// connectContractClient wires a client and a fresh server over an in-memory
// transport. Cleanup closes both ends so no goroutine outlives the test.
func connectContractClient(t *testing.T) *sdkmcp.ClientSession {
	t.Helper()

	server := NewServer()
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "beacon-schema-contract-test", Version: "v0.0.0"},
		nil,
	)
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

// isClosedObject reports whether s is the false schema that jsonschema.For
// emits for a Go struct's additionalProperties. MarshalJSON renders that shape
// as the JSON literal false, so matching the rendered form is stable across
// wire round trips. Only that shape is relaxed below; a map's value schema is a
// real schema and stays in force.
func isClosedObject(s *jsonschema.Schema) bool {
	if s == nil || s.Not == nil {
		return false
	}
	raw, err := json.Marshal(s)
	return err == nil && string(raw) == "false"
}

// relaxSchema walks a schema tree and opens struct-style additionalProperties.
// The advertised schemas are shared, so callers must pass a clone.
func relaxSchema(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if isClosedObject(s.AdditionalProperties) {
		// An empty schema accepts any additional property. This makes the
		// contract tolerant of fields added to an output type later, such as
		// failureExcerpt on get_result, without defining them here.
		s.AdditionalProperties = &jsonschema.Schema{}
	}
	for _, child := range s.Properties {
		relaxSchema(child)
	}
	for _, child := range s.PatternProperties {
		relaxSchema(child)
	}
	for _, child := range s.Defs {
		relaxSchema(child)
	}
	for _, child := range s.Definitions {
		relaxSchema(child)
	}
	for _, child := range s.DependentSchemas {
		relaxSchema(child)
	}
	relaxSchema(s.AdditionalProperties)
	relaxSchema(s.Items)
	for _, child := range s.ItemsArray {
		relaxSchema(child)
	}
	for _, child := range s.PrefixItems {
		relaxSchema(child)
	}
	for _, child := range s.AllOf {
		relaxSchema(child)
	}
	for _, child := range s.AnyOf {
		relaxSchema(child)
	}
	for _, child := range s.OneOf {
		relaxSchema(child)
	}
	relaxSchema(s.Not)
	relaxSchema(s.If)
	relaxSchema(s.Then)
	relaxSchema(s.Else)
	relaxSchema(s.ContentSchema)
	relaxSchema(s.Contains)
	relaxSchema(s.UnevaluatedItems)
	relaxSchema(s.UnevaluatedProperties)
	relaxSchema(s.PropertyNames)
}

// permissiveSchema returns a clone of s whose struct shapes admit unknown
// fields. Map value schemas are untouched, so map contents stay validated.
func permissiveSchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}
	out := s.CloneSchemas()
	relaxSchema(out)
	return out
}

// TestOutputSchemasValidateRealResponses proves every advertised OutputSchema
// accepts the JSON its handler emits. The cases cover optional blocks, null
// maps and empty slices. A forward-compatibility probe adds a field that the
// current get_result schema does not define and checks that it still passes.
func TestOutputSchemasValidateRealResponses(t *testing.T) {
	clientSession := connectContractClient(t)
	tools, _ := listAllTools(t, clientSession)

	schemas := make(map[string]*jsonschema.Resolved, len(tools))
	for _, tool := range tools {
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("marshal %s output schema: %v", tool.Name, err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal %s output schema: %v", tool.Name, err)
		}
		resolved, err := permissiveSchema(&schema).Resolve(nil)
		if err != nil {
			t.Fatalf("resolve %s output schema: %v", tool.Name, err)
		}
		schemas[tool.Name] = resolved
	}

	check := func(name string, response any) {
		t.Helper()
		resolved, ok := schemas[name]
		if !ok {
			t.Fatalf("no advertised output schema for %s", name)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("%s: marshal response: %v", name, err)
		}
		var instance any
		if err := json.Unmarshal(raw, &instance); err != nil {
			t.Fatalf("%s: unmarshal response: %v", name, err)
		}
		if err := resolved.Validate(instance); err != nil {
			t.Errorf("%s: response %s does not validate against its advertised OutputSchema: %v", name, raw, err)
		}
	}

	// beacon_watch_pr: the executor descriptor is optional, so both the legacy
	// five-field shape and the extended one are real responses.
	check("beacon_watch_pr", WatchPROutput{
		WatchID: "w-1", Revision: 1, Status: "watching", Verdict: "watching", PollAfterMs: 180000,
	})
	check("beacon_watch_pr", WatchPROutput{
		WatchID: "w-2", Revision: 2, Status: "watching", Verdict: "watching", PollAfterMs: 180000,
		Executor: &ExecutorMetadata{ID: "ses_1", Role: "executor", Capabilities: []string{"watch"}, ConfigID: "cfg-1"},
	})

	// beacon_wait_result: an unexecuted Watch omits checks, a finished one may
	// carry an empty map, and the reason field is part of the handler type.
	poll := int64(180000)
	check("beacon_wait_result", WaitReasonOutput{
		WaitResultOutput: WaitResultOutput{Status: "watching", Revision: 3},
	})
	check("beacon_wait_result", WaitReasonOutput{
		WaitResultOutput: WaitResultOutput{
			Status: "ready", Verdict: "ready", Revision: 4,
			Checks: map[string]domain.Check{}, PollAfterMs: &poll,
		},
		Reason: WaitStopTerminal,
	})

	// beacon_get_result: failedChecks is an empty slice (not null) when no
	// check failed, and the dialect plus next-step fields are optional.
	check("beacon_get_result", GetResultOutput{
		Status: "ready", Verdict: "ready", Revision: 5,
		Merge:        MergeSnapshot{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
		FailedChecks: []FailedCheck{},
		ObligationID: "job-1",
		NextAction:   NextActionWait,
		NextHint:     "the orchestrator merges",
	})
	check("beacon_get_result", GetResultOutput{
		Status: "keep_working", Verdict: "keep_working", Revision: 6,
		FailedChecks: []FailedCheck{{Name: "ci/test", Conclusion: "FAILURE", DetailsURL: "https://example.test", Required: true}},
		ObligationID: "job-2", RetryBudget: 1,
		ResultType: TaskResultType, TaskID: "job-2", TaskStatus: TaskStatusWorking,
		PollIntervalMs: DefaultTaskPollIntervalMs, Instructions: PlainJobInstructions,
		NextAction: NextActionKeepWorking, NextHint: "fix the failed check",
	})

	// beacon_list_running: a fresh Watch emits null for its nil checks and
	// flaky.used maps. The derived schema widens exactly those two fields.
	check("beacon_list_running", ListRunningOutput{
		Counts: WatchCounts{Watches: 1, ByVerdict: map[string]int{}, Subscribers: 0},
		Watches: []domain.Watch{{
			ID: "w1", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1,
		}},
		Status: string(domain.StatusWatching), Cursor: "c-1", Events: []WatchEvent{},
	})
	check("beacon_list_running", ListRunningOutput{
		Counts:  WatchCounts{Watches: 0, ByVerdict: map[string]int{}, Subscribers: 0},
		Watches: []domain.Watch{}, Status: string(domain.StatusClosed),
		Cursor: "c-2", Verdict: string(domain.VerdictReady),
	})

	check("beacon_ack", OKOutput{OK: true})
	check("beacon_cancel_watch", OKOutput{OK: true})
	check("beacon_stats", StatsOutput{
		PointsThisHour: 0, Budget: 500, ActiveWatches: 0,
		Persist: PersistInfo{}, LoadErrors: []string{}, Ladder: LadderInfo{}, Version: ServerVersion,
	})

	// Forward compatibility: W11-B adds failureExcerpt to get_result. The
	// schema must admit the new optional field even though this test never
	// declares it, and the test must keep passing once the field is added.
	base, err := json.Marshal(GetResultOutput{
		Status: "ready", Verdict: "ready", Revision: 7,
		FailedChecks: []FailedCheck{}, ObligationID: "job-3",
	})
	if err != nil {
		t.Fatalf("marshal get_result probe base: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(base, &probe); err != nil {
		t.Fatalf("unmarshal get_result probe base: %v", err)
	}
	probe["failureExcerpt"] = "one line of check output"
	probe["nextAction"] = NextActionWait
	probe["nextCommand"] = ""
	probe["nextHint"] = "orchestrator merges"
	check("beacon_get_result", probe)
}

// toolOutputTemplate is the canonical report shape a Beacon tool prints at the
// end of a turn (ADR 0094, HD-045): a human-readable summary first, then exactly
// one fenced JSON block. It lives here as an executable contract so a template
// that opens with the fence is caught by CI.
const toolOutputTemplate = `Gate verify: 4 checks passed, 0 failed. Watch w-1 is watching.
Machine-readable result follows.

` + "```json\n" + `{"schema_version":1,"gate":"verify","status":"pass","blocking":false}
` + "```\n"

// summaryPrecedesJSON reports whether report has a non-empty, human-readable
// summary before its first fenced JSON block. A fence with no prose before it
// (JSON first) is rejected.
func summaryPrecedesJSON(report string) bool {
	fence := strings.Index(report, "```json")
	if fence < 0 {
		return false
	}
	return strings.TrimSpace(report[:fence]) != ""
}

// TestToolOutputSummaryBeforeJSON locks the output order from ADR 0094: the
// human-readable summary comes first, the JSON fence last. The negative case
// proves that a template which prints JSON first fails the check.
func TestToolOutputSummaryBeforeJSON(t *testing.T) {
	if !summaryPrecedesJSON(toolOutputTemplate) {
		t.Fatalf("tool output template must print the summary before the JSON fence:\n%s", toolOutputTemplate)
	}

	jsonFirst := "```json\n" + `{"schema_version":1,"gate":"verify","status":"pass"}` + "\n```\n" +
		"Gate verify: 4 checks passed, 0 failed.\n"
	if summaryPrecedesJSON(jsonFirst) {
		t.Fatalf("a template with the JSON fence before the summary must be rejected:\n%s", jsonFirst)
	}
}
