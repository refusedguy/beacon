package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/registry"
)

func TestRenderWorkloadJSON(t *testing.T) {
	report := orchestrator.Workload{
		Total:      2,
		Ready:      1,
		Watching:   1,
		SlotsTotal: 17,
		SlotsUsed:  2,
		ByVerdict:  map[string]int{"ready": 1, "watching": 1},
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
	if got.SlotsTotal != 17 || got.SlotsUsed != 2 {
		t.Fatalf("slots = %d/%d, want 17/2", got.SlotsUsed, got.SlotsTotal)
	}
	if got.ByVerdict["ready"] != 1 || got.ByVerdict["watching"] != 1 {
		t.Fatalf("byVerdict = %v, want ready and watching buckets", got.ByVerdict)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"slotsTotal", "slotsUsed"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("json missing key %q in %s", key, out.String())
		}
	}
}

func TestSlotBudgetFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.yaml")

	got, err := slotBudgetFromConfig(path)
	if err != nil || got != nil {
		t.Fatalf("missing config = %v, %v; want nil, nil", got, err)
	}
	if budget, err := registry.ResolveSlotBudget(nil); err != nil || budget != registry.DefaultSlotBudget {
		t.Fatalf("missing config budget = %d, %v; want %d", budget, err, registry.DefaultSlotBudget)
	}

	cases := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{"lower boundary", "schema: beacon.agent-registry/v2\nlimits:\n  max_concurrent_agents: 12\n", 12, false},
		{"upper boundary", "schema: beacon.agent-registry/v2\nlimits:\n  max_concurrent_agents: 17\n", 17, false},
		{"zero budget", "schema: beacon.agent-registry/v2\nlimits:\n  max_concurrent_agents: 0\n", 0, false},
		{"outside range", "schema: beacon.agent-registry/v2\nlimits:\n  max_concurrent_agents: 18\n", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			configured, err := slotBudgetFromConfig(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("budget %d accepted, want rejection", *configured)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if configured == nil {
				t.Fatal("config key not read")
			}
			budget, err := registry.ResolveSlotBudget(configured)
			if err != nil {
				t.Fatal(err)
			}
			if budget != tc.want {
				t.Fatalf("budget = %d, want %d", budget, tc.want)
			}
		})
	}
}
