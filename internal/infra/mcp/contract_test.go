package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// toolsListByteBudget is the ceiling for the serialized tools/list result.
// The budget counts real bytes (method Tirith), not the presence of fields.
// ADR 0094 set 16 KiB for eight tools. ADR 0268 raised it to 20 KiB for nine
// tools with a derived claim schema. ADR 0270 keeps the ceiling at 20 KiB for
// ten tools. The ceiling still catches runaway growth in descriptions and
// schemas. CI logs the measured size next to it so drift is visible before it
// becomes a failure. The eleventh descriptor tool measured 21183 bytes, so
// ML-01 raises the ceiling to 24 KiB and records that measurement here.
const toolsListByteBudget = 24 * 1024

// toolsListByteCount is the measured serialized size of the tools/list response
// for the contracted eleven descriptor tools. The budget above is a ceiling.
// Do not derive this value from an estimate.
const toolsListByteCount = 21183

// toolsListGoldenSHA256 is the SHA-256 of the same serialized response. A hash
// catches any byte change, including a swap of two tools whose names have equal
// length, which a size check alone would miss. The value is the measured
// eleven-tool response, not a guess.
const toolsListGoldenSHA256 = "dd08458d7b33ea8f6890c87b798b4ce31295e39121a2573597957f562ce7c4a2"

// Golden refresh is driven through this test so the pagination walker below
// stays the one source of the serialized tools/list response. The CLI sets
// BEACON_MCP_GOLDEN to refresh or check; a plain go test only runs the
// planted-drift self-test.
const (
	goldenModeEnv     = "BEACON_MCP_GOLDEN"
	goldenModeRefresh = "refresh"
	goldenModeCheck   = "check"
)

var (
	goldenCountLine = regexp.MustCompile(`(?m)^const toolsListByteCount = [0-9]+$`)
	goldenSHALine   = regexp.MustCompile(`(?m)^const toolsListGoldenSHA256 = "[0-9a-f]{64}"$`)
)

type goldenValues struct {
	count int
	sha   string
}

// goldenFromSource reads the two constants without touching the budget or any
// other declaration in the file.
func goldenFromSource(src string) (goldenValues, error) {
	var out goldenValues
	countLine := goldenCountLine.FindString(src)
	if countLine == "" {
		return out, fmt.Errorf("const toolsListByteCount declaration not found")
	}
	countText := strings.TrimPrefix(countLine, "const toolsListByteCount = ")
	count, err := strconv.Atoi(countText)
	if err != nil {
		return out, fmt.Errorf("parse toolsListByteCount %q: %w", countText, err)
	}
	shaLine := goldenSHALine.FindString(src)
	if shaLine == "" {
		return out, fmt.Errorf("const toolsListGoldenSHA256 declaration not found")
	}
	out = goldenValues{count: count, sha: strings.Trim(shaLine[strings.Index(shaLine, `"`):], `"`)}
	return out, nil
}

// rewriteGoldenSource replaces exactly the two constant declarations. Every
// other byte, including toolsListByteBudget, is preserved.
func rewriteGoldenSource(src string, want goldenValues) (string, error) {
	if got := len(goldenCountLine.FindAllStringIndex(src, -1)); got != 1 {
		return "", fmt.Errorf("expected one toolsListByteCount declaration, got %d", got)
	}
	if got := len(goldenSHALine.FindAllStringIndex(src, -1)); got != 1 {
		return "", fmt.Errorf("expected one toolsListGoldenSHA256 declaration, got %d", got)
	}
	out := goldenCountLine.ReplaceAllLiteralString(src, fmt.Sprintf("const toolsListByteCount = %d", want.count))
	out = goldenSHALine.ReplaceAllLiteralString(out, fmt.Sprintf("const toolsListGoldenSHA256 = %q", want.sha))
	return out, nil
}

func checkGoldenSource(src string, want goldenValues) error {
	got, err := goldenFromSource(src)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("golden drift: size=%d sha=%s, want size=%d sha=%s", got.count, got.sha, want.count, want.sha)
	}
	return nil
}

// applyGoldenMode never writes in check mode. That is what makes a planted
// discrepancy fail instead of silently refreshing the reference.
func applyGoldenMode(path, mode string, want goldenValues) (string, error) {
	srcBytes, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	src := string(srcBytes)
	switch mode {
	case goldenModeCheck:
		if err := checkGoldenSource(src, want); err != nil {
			return "", err
		}
		return "current", nil
	case goldenModeRefresh:
		if err := checkGoldenSource(src, want); err == nil {
			return "unchanged", nil
		}
		updated, err := rewriteGoldenSource(src, want)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		return "updated", nil
	default:
		return "", fmt.Errorf("unknown golden mode %q", mode)
	}
}

// expectedToolNames is the complete advertised tool contract. It is derived
// from the descriptors so the witness cannot omit a tool the registry gained.
// The byte count and SHA above are regenerated by beacon mcp golden --refresh.
// They stay an independent check that catches an unannounced change to a
// description or schema.
//
// The eleven expected names in emitted order are beacon_ack,
// beacon_cancel_watch, beacon_defer_wiring, beacon_get_result,
// beacon_list_running, beacon_stats, beacon_task_claim, beacon_task_close,
// beacon_task_list, beacon_wait_result and beacon_watch_pr.
// The descriptor table owns the names. This list records the expected result
// once the beacon_task_list descriptor is registered. The byte count and SHA
// are regenerated from this walker by beacon mcp golden --refresh.
var expectedToolNames = func() []string {
	names := make([]string, 0, len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		names = append(names, descriptor.Name)
	}
	return names
}()

// toolsListOrder is the fixed order in which the server lists its tools. The
// SDK returns the tools sorted by name, so sorting the descriptor names pins
// that behavior without repeating the names a third time.
var toolsListOrder = func() []string {
	names := slices.Clone(expectedToolNames)
	slices.Sort(names)
	return names
}()

// expectedToolKinds maps every advertised tool to its honest annotation class.
// A tool that writes readAt or claimedAt is additive, not read-only: the
// distinction is the lesson from Tirith ADR-0033/0034 recorded in the audit.
var expectedToolKinds = func() map[string]toolKind {
	out := make(map[string]toolKind, len(toolDescriptors))
	for _, descriptor := range toolDescriptors {
		out[descriptor.Name] = descriptor.Kind
	}
	return out
}()

// listAllTools walks every tools/list page and returns the tools together with
// the number of bytes produced by serializing each response. Counting every
// page keeps the "exactly eleven, no twelfth" check honest if the server paginates.
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
		case kindAdditive, kindAdditiveOnce:
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

// toolsListRawBytes opens a fresh server and client pair over its own in-memory
// transport, walks tools/list and returns the concatenated serialized response
// bytes. Each call builds a new server, so two calls compare independent
// connections rather than two reads of the same session.
func toolsListRawBytes(t *testing.T) []byte {
	t.Helper()

	server := NewServer()
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "beacon-stability-test", Version: "v0.0.0"},
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

	var all []byte
	cursor := ""
	seen := make(map[string]bool)
	for {
		params := &sdkmcp.ListToolsParams{}
		if cursor != "" {
			params.Cursor = cursor
		}
		response, err := clientSession.ListTools(context.Background(), params)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal tools/list response: %v", err)
		}
		all = append(all, raw...)
		if response.NextCursor == "" {
			return all
		}
		if seen[response.NextCursor] {
			t.Fatalf("tools/list pagination loop: cursor %q repeated", response.NextCursor)
		}
		seen[response.NextCursor] = true
		cursor = response.NextCursor
	}
}

// TestToolsListStabilityAcrossConnections pins the tools/list listing as a
// byte-for-byte artifact of CI. Two independent connections must return the
// same bytes. The test also checks unique names, a fixed order and the recorded
// size and hash. Swapping two tools changes the hash even when the byte count
// stays the same, so the negative control fails the test.
func TestToolsListStabilityAcrossConnections(t *testing.T) {
	first := toolsListRawBytes(t)
	second := toolsListRawBytes(t)

	if !bytes.Equal(first, second) {
		t.Fatalf("tools/list differs between connections: first %d bytes, second %d bytes", len(first), len(second))
	}

	if len(first) != toolsListByteCount {
		t.Errorf("tools/list size = %d bytes, want %d", len(first), toolsListByteCount)
	}

	gotHash := sha256.Sum256(first)
	if got := hex.EncodeToString(gotHash[:]); got != toolsListGoldenSHA256 {
		t.Errorf("tools/list SHA-256 = %s, want %s", got, toolsListGoldenSHA256)
	}

	var response sdkmcp.ListToolsResult
	if err := json.Unmarshal(first, &response); err != nil {
		t.Fatalf("unmarshal tools/list response: %v", err)
	}

	gotOrder := make([]string, 0, len(response.Tools))
	seen := make(map[string]bool, len(response.Tools))
	for _, tool := range response.Tools {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		gotOrder = append(gotOrder, tool.Name)
	}
	if !slices.Equal(gotOrder, toolsListOrder) {
		t.Errorf("tools/list order = %v, want %v", gotOrder, toolsListOrder)
	}
}

// TestGoldenRefreshMode pins the generator that keeps the two hand-written
// constants in sync with toolsListRawBytes. The planted-drift subtest proves
// check mode fails without rewriting the reference, and that refresh is
// idempotent. The CLI selects refresh or check through goldenModeEnv.
func TestGoldenRefreshMode(t *testing.T) {
	raw := toolsListRawBytes(t)
	sum := sha256.Sum256(raw)
	want := goldenValues{count: len(raw), sha: hex.EncodeToString(sum[:])}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate contract_test.go")
	}

	t.Run("planted drift", func(t *testing.T) {
		srcBytes, err := os.ReadFile(thisFile)
		if err != nil {
			t.Fatal(err)
		}
		planted, err := rewriteGoldenSource(string(srcBytes), goldenValues{count: want.count, sha: strings.Repeat("0", 64)})
		if err != nil {
			t.Fatal(err)
		}
		if err := checkGoldenSource(planted, want); err == nil {
			t.Fatal("planted SHA drift passed check")
		}

		tmp := filepath.Join(t.TempDir(), "contract_test.go")
		if err := os.WriteFile(tmp, []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyGoldenMode(tmp, goldenModeCheck, want); err == nil {
			t.Fatal("planted SHA drift passed --check")
		}
		after, err := os.ReadFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("check mode rewrote the golden file")
		}

		if report, err := applyGoldenMode(tmp, goldenModeRefresh, want); err != nil || report != "updated" {
			t.Fatalf("refresh report=%q err=%v, want updated", report, err)
		}
		fixed, err := os.ReadFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkGoldenSource(string(fixed), want); err != nil {
			t.Fatalf("refresh did not fix the planted drift: %v", err)
		}
		if report, err := applyGoldenMode(tmp, goldenModeRefresh, want); err != nil || report != "unchanged" {
			t.Fatalf("second refresh report=%q err=%v, want unchanged", report, err)
		}
	})

	mode := os.Getenv(goldenModeEnv)
	if mode == "" {
		return
	}
	report, err := applyGoldenMode(thisFile, mode, want)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("golden %s: size=%d sha=%s report=%s", mode, want.count, want.sha, report)
}

// TestToolDescriptorsDescribeAdvertisedTools proves the single descriptor table
// matches what tools/list advertises. The byte count and SHA come from the
// golden generator; this test catches a descriptor that lost its handler or
// whose description drifted from the tool a client actually sees.
func TestToolDescriptorsDescribeAdvertisedTools(t *testing.T) {
	clientSession := connectContractClient(t)
	tools, _ := listAllTools(t, clientSession)

	if len(tools) != len(toolDescriptors) {
		t.Fatalf("advertised tools = %d, descriptors = %d", len(tools), len(toolDescriptors))
	}
	byName := make(map[string]*sdkmcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	version := BuildVersion()
	for _, descriptor := range toolDescriptors {
		if descriptor.Name == "" {
			t.Error("descriptor with an empty name")
			continue
		}
		if descriptor.Handler == nil {
			t.Errorf("%s: descriptor has no handler", descriptor.Name)
		}
		tool := byName[descriptor.Name]
		if tool == nil {
			t.Errorf("no advertised tool for descriptor %s", descriptor.Name)
			continue
		}
		wantDescription := descriptor.Description
		if descriptor.Name == "beacon_stats" {
			wantDescription = DescriptionWithVersion(wantDescription, version)
		}
		if tool.Description != wantDescription {
			t.Errorf("%s: description = %q, want %q", descriptor.Name, tool.Description, wantDescription)
		}
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
// wait_result is checked without relaxation: its declared schema covers every
// field the handler returns, so relaxSchema must not hide a drift there.
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
		candidate := &schema
		if tool.Name != "beacon_wait_result" {
			candidate = permissiveSchema(&schema)
		}
		resolved, err := candidate.Resolve(nil)
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
			Reason: WaitStopTerminal,
		},
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
		Watches: []WatchOutput{watchOutput(domain.Watch{
			ID: "w1", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1,
		})},
		Status: string(domain.StatusWatching), Cursor: "c-1", Events: []WatchEvent{},
	})
	check("beacon_list_running", ListRunningOutput{
		Counts:  WatchCounts{Watches: 0, ByVerdict: map[string]int{}, Subscribers: 0},
		Watches: []WatchOutput{}, Status: string(domain.StatusClosed),
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

// TestWaitResultResponseMatchesAdvertisedSchema proves a strict client accepts
// the reason the wait_result handler returns. The schema here has no relaxation,
// so a future drift fails this test instead of passing through permissiveSchema.
func TestWaitResultResponseMatchesAdvertisedSchema(t *testing.T) {
	clientSession := connectContractClient(t)
	tools, _ := listAllTools(t, clientSession)

	var raw []byte
	for _, tool := range tools {
		if tool.Name != "beacon_wait_result" {
			continue
		}
		var err error
		raw, err = json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("marshal wait_result output schema: %v", err)
		}
		break
	}
	if raw == nil {
		t.Fatal("no advertised output schema for beacon_wait_result")
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal wait_result output schema: %v", err)
	}
	if _, ok := schema.Properties["reason"]; !ok {
		t.Fatal("advertised wait_result schema does not declare reason")
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve wait_result output schema: %v", err)
	}

	cases := []struct {
		name string
		out  WaitReasonOutput
	}{
		{
			name: "no reason",
			out:  WaitReasonOutput{WaitResultOutput: WaitResultOutput{Status: "watching", Revision: 1}},
		},
		{
			name: "terminal reason",
			out: WaitReasonOutput{WaitResultOutput: WaitResultOutput{
				Status: "ready", Verdict: "ready", Revision: 2, Reason: WaitStopTerminal,
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.out)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			var instance any
			if err := json.Unmarshal(payload, &instance); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if err := resolved.Validate(instance); err != nil {
				t.Errorf("response %s does not validate against the advertised schema: %v", payload, err)
			}
		})
	}
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

// listRunningUnexposedDomainFields records domain.Watch fields that must stay
// out of tools/list. Add a field here when the domain gains one that clients
// should not see. The test then passes only after someone made that decision.
var listRunningUnexposedDomainFields = map[string]bool{
	// noChecksTimeout is the verdict-rule window, not watch state clients act
	// on. NoChecksSince is already exposed, so the timeout stays internal until
	// a client asks for it and the frozen field set is extended deliberately.
	"noChecksTimeout": true,
}

// TestListRunningWatchOutputFields pins the field set of the list_running DTO.
// The output schema reflects WatchOutput, not domain.Watch. A new domain field
// fails this test until it is added to the DTO or to the unexposed list above.
// Adding it to the DTO changes tools/list and requires the golden schema in the
// same change.
func TestListRunningWatchOutputFields(t *testing.T) {
	dto := jsonFieldNames[WatchOutput]()
	domainFields := jsonFieldNames[domain.Watch]()
	for name := range domainFields {
		if dto[name] || listRunningUnexposedDomainFields[name] {
			continue
		}
		t.Errorf("domain.Watch field %q is not covered by WatchOutput or the unexposed list; decide whether to expose it", name)
	}
	for name := range listRunningUnexposedDomainFields {
		if dto[name] {
			t.Errorf("field %q is both exposed and marked unexposed", name)
		}
	}

	frozen := []string{
		"baseRef", "blocked", "blockerID", "checks", "closeReason", "closedAt",
		"configId", "createdAt", "deadlineAt", "depth", "fixBlockerID", "fixFor",
		"flaky", "headSHA", "id", "issue", "merge", "mergePolicy",
		"noChecksSince", "orchestratorID", "policy", "pr", "repo", "revision",
		"status", "subscribers", "verdict",
	}
	got := make([]string, 0, len(dto))
	for name := range dto {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, frozen) {
		t.Fatalf("WatchOutput fields = %v, want %v", got, frozen)
	}
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

// remoteCatalogGaps pins the agent tools the remote map does not describe yet.
// The drift audit records seven remote names against ten descriptor tools
// (docs/research/audit-04-protocols.md, "Измеренное состояние"). AUD-09 and
// AUD-10 own client.go; this card owns only the contract test. Remove a name
// once its remote description and input-schema factory land, so the gap set
// can only shrink.
var remoteCatalogGaps = map[string]bool{
	"beacon_task_claim":   true,
	"beacon_task_close":   true,
	"beacon_defer_wiring": true,
}

// TestOneCatalogIncludesWake pins the advertised catalog used by both the
// tools/list contract and the E2E assertion. The twelve names come from one
// catalog: eleven descriptor tools plus the opt-in wake tool. A duplicate or a
// missing name breaks both consumers at once.
func TestOneCatalogIncludesWake(t *testing.T) {
	advertised := AdvertisedToolNames()
	if len(advertised) != 12 || len(advertised) != len(toolDescriptors)+1 {
		t.Fatalf("advertised catalog = %d names, want 11 descriptors plus %s", len(advertised), WakeToolName)
	}

	seen := make(map[string]bool, len(advertised))
	for _, name := range advertised {
		if name == "" {
			t.Error("advertised catalog has an empty name")
			continue
		}
		if seen[name] {
			t.Errorf("advertised catalog repeats %q", name)
		}
		seen[name] = true
	}
	if !seen[WakeToolName] {
		t.Errorf("advertised catalog omits %s", WakeToolName)
	}

	agent := ToolNamesCopy()
	if len(agent) != len(toolDescriptors) {
		t.Errorf("agent tool names = %d, descriptors = %d", len(agent), len(toolDescriptors))
	}
	for _, name := range agent {
		if name == WakeToolName {
			t.Errorf("%s is registered as an agent tool", WakeToolName)
		}
		if !seen[name] {
			t.Errorf("agent tool %q is missing from the advertised catalog", name)
		}
	}
}

// remoteCatalogNames returns the sorted union of names present in either
// remote table. A name that exists in one table only is drift, so both sides
// feed the same set.
func remoteCatalogNames() []string {
	seen := make(map[string]bool, len(remoteToolDescriptions)+len(remoteInputSchemaFactories))
	for name := range remoteToolDescriptions {
		seen[name] = true
	}
	for name := range remoteInputSchemaFactories {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// remoteCatalogProblems reports names whose remote description or input
// schema is missing or empty. The live catalog test and the negative control
// share this function, so adding a name without either entry fails the same
// check.
func remoteCatalogProblems(names []string, descriptions map[string]string, factories map[string]func() (*jsonschema.Schema, error)) []string {
	problems := make([]string, 0)
	for _, name := range names {
		if strings.TrimSpace(descriptions[name]) == "" {
			problems = append(problems, name+": empty remote description")
		}
		factory, ok := factories[name]
		if !ok || factory == nil {
			problems = append(problems, name+": missing remote input-schema factory")
			continue
		}
		schema, err := factory()
		if err != nil {
			problems = append(problems, name+": remote input schema: "+err.Error())
			continue
		}
		raw, err := json.Marshal(schema)
		if err != nil || len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
			problems = append(problems, name+": empty remote input schema")
		}
	}
	return problems
}

// TestRemoteCatalogContract ties the remote tables to the same agent catalog
// the tools/list contract uses. The wake tool stays in the advertised set as
// an adapter channel and stays out of both remote tables.
func TestRemoteCatalogContract(t *testing.T) {
	if !slices.Contains(AdvertisedToolNames(), WakeToolName) {
		t.Fatalf("advertised catalog omits %s", WakeToolName)
	}
	if _, ok := remoteToolDescriptions[WakeToolName]; ok {
		t.Errorf("%s has a remote description; it is an adapter channel, not an agent tool", WakeToolName)
	}
	if _, ok := remoteInputSchemaFactories[WakeToolName]; ok {
		t.Errorf("%s has a remote input-schema factory; it is an adapter channel, not an agent tool", WakeToolName)
	}

	agent := ToolNamesCopy()
	remoteNames := remoteCatalogNames()
	if len(remoteNames) == 0 {
		t.Fatal("remote catalog is empty")
	}
	for _, name := range remoteNames {
		if !slices.Contains(agent, name) {
			t.Errorf("remote name %q is not in the agent catalog", name)
		}
	}
	if problems := remoteCatalogProblems(remoteNames, remoteToolDescriptions, remoteInputSchemaFactories); len(problems) > 0 {
		t.Errorf("remote catalog drift: %s", strings.Join(problems, "; "))
	}

	// A new agent tool must gain a complete remote entry or a reasoned gap
	// entry here. The pinned gaps are the measured 7/10 drift, not permission
	// to grow it.
	for _, name := range agent {
		_, hasDescription := remoteToolDescriptions[name]
		_, hasFactory := remoteInputSchemaFactories[name]
		if hasDescription || hasFactory {
			continue
		}
		if !remoteCatalogGaps[name] {
			t.Errorf("agent tool %q has no remote entry; add its description and factory or pin a reasoned gap", name)
		}
	}
	for name := range remoteCatalogGaps {
		if slices.Contains(agent, name) {
			continue
		}
		t.Errorf("pinned remote gap %q is no longer an agent tool; remove it from remoteCatalogGaps", name)
	}
}

// TestRemoteCatalogRejectsMissingDescriptionAndFactory is the negative
// control for TestRemoteCatalogContract. It proves the shared validator fails
// a name added without a description, without a factory or with an empty
// input schema, while a complete name passes.
func TestRemoteCatalogRejectsMissingDescriptionAndFactory(t *testing.T) {
	descriptions := make(map[string]string, len(remoteToolDescriptions)+3)
	factories := make(map[string]func() (*jsonschema.Schema, error), len(remoteInputSchemaFactories)+3)
	for name, description := range remoteToolDescriptions {
		descriptions[name] = description
	}
	for name, factory := range remoteInputSchemaFactories {
		factories[name] = factory
	}

	descriptions["beacon_no_description"] = "   "
	factories["beacon_no_description"] = func() (*jsonschema.Schema, error) {
		return &jsonschema.Schema{Type: "object"}, nil
	}
	descriptions["beacon_no_factory"] = "A tool whose input schema factory is missing."
	descriptions["beacon_no_schema"] = "A tool whose factory returns no schema."
	factories["beacon_no_schema"] = func() (*jsonschema.Schema, error) { return nil, nil }

	problems := remoteCatalogProblems([]string{
		"beacon_watch_pr",
		"beacon_no_description",
		"beacon_no_factory",
		"beacon_no_schema",
	}, descriptions, factories)
	joined := strings.Join(problems, " | ")

	for _, want := range []string{
		"beacon_no_description: empty remote description",
		"beacon_no_factory: missing remote input-schema factory",
		"beacon_no_schema: empty remote input schema",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("validator missed %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "beacon_watch_pr") {
		t.Errorf("complete name was rejected: %q", joined)
	}
}
