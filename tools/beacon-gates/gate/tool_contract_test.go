package gate

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

const closedParameterless = `{"type":"object","properties":{},"additionalProperties":false}`

const closedWithParam = `{"type":"object","properties":{"pr":{"type":"integer","description":"Pull request number to watch."}},"required":["pr"],"additionalProperties":false}`

const paramWithoutDescription = `{"type":"object","properties":{"pr":{"type":"integer"}},"required":["pr"],"additionalProperties":false}`

var goodToolNames = []string{
	"beacon_watch_pr",
	"beacon_wait_result",
	"beacon_get_result",
	"beacon_list_running",
	"beacon_ack",
	"beacon_cancel_watch",
	"beacon_stats",
	"beacon_defer_wiring",
}

var goodDescriptions = []string{
	"Watch a pull request and report the verdict when its checks finish.",
	"Poll a running watch until its checks reach a final verdict.",
	"Fetch the stored result and explain the next action to take.",
	"List every open watch with its current revision and owner.",
	"Record an intention to claim one obligation on a live watch.",
	"Stop a watch and cancel all of its pending subscriptions now.",
	"Read the running watch statistics and the current budget.",
	"Defer a wiring card for work that another agent must finish.",
}

func toolJSON(name, description, schema string) string {
	return fmt.Sprintf(`{"name":%q,"description":%q,"inputSchema":%s}`, name, description, schema)
}

func toolListJSON(tools ...string) []byte {
	return []byte(`{"tools":[` + strings.Join(tools, ",") + `]}`)
}

func goodFixture() []byte {
	tools := make([]string, 0, len(goodToolNames))
	for i, name := range goodToolNames {
		schema := closedParameterless
		if i == 0 {
			schema = closedWithParam
		}
		tools = append(tools, toolJSON(name, goodDescriptions[i], schema))
	}
	return toolListJSON(tools...)
}

func TestCheckToolContractGoodFixture(t *testing.T) {
	data := goodFixture()
	res := CheckToolContract(data)

	if res.ByteSize != len(data) {
		t.Fatalf("ByteSize = %d, want %d", res.ByteSize, len(data))
	}
	if len(res.Findings) != 0 {
		t.Fatalf("good fixture produced findings: %v", res.Findings)
	}
	want := fmt.Sprintf("tools/list: %d bytes, 0 finding(s)", len(data))
	if got := res.Report(); got != want {
		t.Fatalf("Report() = %q, want %q", got, want)
	}
}

func TestCheckToolContractNameFormat(t *testing.T) {
	for _, name := range []string{"beacon watch pr", "", strings.Repeat("a", 65)} {
		data := toolListJSON(toolJSON(name, goodDescriptions[0], closedParameterless))
		res := CheckToolContract(data)
		if !HasRule(res.Findings, ToolNameFormatRule) {
			t.Errorf("name %q was not rejected: %v", name, res.Findings)
		}
		if HasRule(res.Findings, ToolDescShortRule) || HasRule(res.Findings, ToolDescNearDupRule) {
			t.Errorf("name check also reported unrelated rules for %q: %v", name, res.Findings)
		}
	}
}

func TestCheckToolContractDuplicateName(t *testing.T) {
	data := toolListJSON(
		toolJSON("beacon_ack", goodDescriptions[0], closedParameterless),
		toolJSON("beacon_ack", goodDescriptions[1], closedParameterless),
	)
	res := CheckToolContract(data)
	if !HasRule(res.Findings, ToolNameDuplicateRule) {
		t.Fatalf("duplicate name not reported: %v", res.Findings)
	}
	if HasRule(res.Findings, ToolDescNearDupRule) {
		t.Fatalf("distinct descriptions flagged as near duplicates: %v", res.Findings)
	}
}

func TestCheckToolContractShortDescription(t *testing.T) {
	data := toolListJSON(toolJSON("beacon_ack", "Too short.", closedParameterless))
	res := CheckToolContract(data)
	if !HasRule(res.Findings, ToolDescShortRule) {
		t.Fatalf("short description not reported: %v", res.Findings)
	}
	if HasRule(res.Findings, ToolDescNearDupRule) {
		t.Fatalf("single short description reported as near duplicate: %v", res.Findings)
	}
}

func TestCheckToolContractParameterDescriptions(t *testing.T) {
	data := toolListJSON(
		toolJSON("beacon_watch_pr", goodDescriptions[0], paramWithoutDescription),
		toolJSON("beacon_wait_result", goodDescriptions[1], closedWithParam),
	)
	res := CheckToolContract(data)

	var paramFindings []Finding
	for _, f := range res.Findings {
		if f.Rule == ToolParamDescRule {
			paramFindings = append(paramFindings, f)
		}
	}
	if len(paramFindings) != 1 {
		t.Fatalf("got %d parameter findings, want 1: %v", len(paramFindings), res.Findings)
	}
	if !strings.Contains(paramFindings[0].Message, "pr") {
		t.Fatalf("finding does not name the parameter: %v", paramFindings[0])
	}
}

func TestCheckToolContractClosedParameterless(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		rule   string
	}{
		{
			name:   "missing additionalProperties",
			schema: `{"type":"object","properties":{}}`,
			rule:   ToolAdditionalPropsRule,
		},
		{
			name:   "additionalProperties true",
			schema: `{"type":"object","properties":{},"additionalProperties":true}`,
			rule:   ToolAdditionalPropsRule,
		},
		{
			name:   "required without declared parameter",
			schema: `{"type":"object","properties":{},"additionalProperties":false,"required":["pr"]}`,
			rule:   ToolRequiredRule,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := toolListJSON(toolJSON("beacon_stats", goodDescriptions[6], tt.schema))
			res := CheckToolContract(data)
			if !HasRule(res.Findings, tt.rule) {
				t.Fatalf("rule %s not reported: %v", tt.rule, res.Findings)
			}
		})
	}
}

func TestCheckToolContractNearDuplicateDescriptions(t *testing.T) {
	near := "Watch a pull request and report the verdict when its checks complete."
	data := toolListJSON(
		toolJSON("beacon_watch_pr", goodDescriptions[0], closedParameterless),
		toolJSON("beacon_wait_result", near, closedParameterless),
	)
	res := CheckToolContract(data)
	if !HasRule(res.Findings, ToolDescNearDupRule) {
		t.Fatalf("near-duplicate descriptions not reported: %v", res.Findings)
	}

	distinct := toolListJSON(
		toolJSON("beacon_watch_pr", goodDescriptions[0], closedParameterless),
		toolJSON("beacon_wait_result", goodDescriptions[1], closedParameterless),
	)
	res = CheckToolContract(distinct)
	if HasRule(res.Findings, ToolDescNearDupRule) {
		t.Fatalf("distinct descriptions reported as near duplicates: %v", res.Findings)
	}
}

func TestCheckToolContractMalformedAndEnvelope(t *testing.T) {
	bad := []byte(`{"tools":[`)
	res := CheckToolContract(bad)
	if res.ByteSize != len(bad) || !HasRule(res.Findings, ToolListInvalidRule) {
		t.Fatalf("malformed JSON: byteSize=%d findings=%v", res.ByteSize, res.Findings)
	}

	wrapped := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
		toolJSON("beacon_stats", goodDescriptions[6], closedParameterless) + `]}}`)
	res = CheckToolContract(wrapped)
	if res.ByteSize != len(wrapped) || len(res.Findings) != 0 {
		t.Fatalf("result envelope: byteSize=%d findings=%v", res.ByteSize, res.Findings)
	}
}

func TestCheckToolContractNoStdout(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	res := CheckToolContract(goodFixture())
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = old

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("CheckToolContract wrote %q to stdout", out)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("good fixture produced findings: %v", res.Findings)
	}
}
