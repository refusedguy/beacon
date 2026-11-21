package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
)

func TestRenderWorkloadJSON(t *testing.T) {
	report := orchestrator.Workload{
		Total:     2,
		Ready:     1,
		Watching:  1,
		ByVerdict: map[string]int{"ready": 1, "watching": 1},
	}
	var out bytes.Buffer
	if err := renderWorkloadJSON(&out, report); err != nil {
		t.Fatalf("encode workload: %v", err)
	}
	var got orchestrator.Workload
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode workload JSON: %v; output: %q", err, out.String())
	}
	if got.Total != 2 || got.Ready != 1 || got.Watching != 1 {
		t.Fatalf("workload = %+v, want total=2 ready=1 watching=1", got)
	}
	if got.ByVerdict["ready"] != 1 || got.ByVerdict["watching"] != 1 {
		t.Fatalf("byVerdict = %v, want ready and watching buckets", got.ByVerdict)
	}
}
