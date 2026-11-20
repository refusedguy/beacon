package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLastGateResultSelectsFinalFence(t *testing.T) {
	out := "Human-readable summary first.\n\n" +
		"```json\n" +
		`{"note":"illustrative example, not the result"}` + "\n" +
		"```\n\n" +
		"```aif-gate-result\n" +
		`{"schema":1,"status":"pass"}` + "\n" +
		"```\n\n" +
		"Gate result:\n\n" +
		"```aif-gate-result\n" +
		`{"schema":1,"status":"fail","blockers":[{"id":"HD-048"}]}` + "\n" +
		"```\n"

	got, ok := LastGateResult(out)
	if !ok {
		t.Fatal("LastGateResult did not find a fence")
	}
	if !strings.Contains(got, `"status":"fail"`) {
		t.Errorf("selected an earlier fence: %s", got)
	}
}

func TestLastGateResultRejectsIncorrectFinalFence(t *testing.T) {
	out := "```aif-gate-result\n" +
		`{"schema":1,"status":"pass"}` + "\n" +
		"```\n\n" +
		"```aif-gate-result\n" +
		`{ not valid json` + "\n" +
		"```\n"

	if got, ok := LastGateResult(out); ok {
		t.Errorf("accepted malformed final fence: %s", got)
	}
}

func TestLastGateResultFinalFenceMustCarryMarker(t *testing.T) {
	out := "```aif-gate-result\n" +
		`{"schema":1,"status":"pass"}` + "\n" +
		"```\n\n" +
		"```text\n" +
		"an illustrative fence after the result\n" +
		"```\n"

	if got, ok := LastGateResult(out); ok {
		t.Errorf("selected an earlier fence when the final fence is illustrative: %s", got)
	}
}

func TestLastGateResultEmptyAndUnclosedOutput(t *testing.T) {
	for _, out := range []string{
		"",
		"no fences here\n",
		"```aif-gate-result\n" + `{"schema":1,"status":"pass"}` + "\n",
	} {
		if got, ok := LastGateResult(out); ok {
			t.Errorf("unexpected result %q for output %q", got, out)
		}
	}
}

func TestLastGateResultSingleFence(t *testing.T) {
	out := "Report\n\n```aif-gate-result\n" +
		`{"schema":1,"status":"pass"}` + "\n```\n"

	got, ok := LastGateResult(out)
	if !ok {
		t.Fatal("LastGateResult did not find the fence")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("result is not a JSON object: %v", err)
	}
	if obj["status"] != "pass" {
		t.Errorf("status = %v, want pass", obj["status"])
	}
}

func TestLastFencedBlockTakesLastClosedBlock(t *testing.T) {
	out := "```text\nfirst\n```\nkeep\n~~~json\n{\"a\":1}\n~~~\n"
	block, ok := lastFencedBlock(out)
	if !ok {
		t.Fatal("lastFencedBlock found no block")
	}
	if block.info != "json" || strings.TrimSpace(block.body) != `{"a":1}` {
		t.Errorf("block = %+v, want the tilde fence with JSON body", block)
	}
}
