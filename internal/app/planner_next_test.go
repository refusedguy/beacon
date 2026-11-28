package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestPlannerSkipsCarryNextAction(t *testing.T) {
	candidates := []core.Task{
		{ID: "bad-status", Status: core.TaskDone, Scope: []core.ScopePath{core.ScopeFile("bad.go")}},
		{ID: "busy", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("busy.go")}},
		{ID: "no-scope", Status: core.TaskAvailable},
		{ID: "oversized", Status: core.TaskAvailable, Scope: []core.ScopePath{
			core.ScopeFile("o1.go"), core.ScopeFile("o2.go"),
			core.ScopeFile("o3.go"), core.ScopeFile("o4.go"),
		}},
		{ID: "active-overlap", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("active.go")}},
		{ID: "selected-first", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("pair.go")}},
		{ID: "selected-second", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("pair.go")}},
	}
	active := []*core.Run{
		{ID: "r-busy", TaskID: "busy", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("busy.go")}},
		{ID: "r-active", TaskID: "other", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("active.go")}},
	}

	_, skipped, err := NewPlanner(nil).Select(candidates, active, len(candidates), true)
	if err != nil {
		t.Fatalf(`NewPlanner(nil).Select(candidates, active, %d, true): %v`, len(candidates), err)
	}

	byCode := make(map[SkipCode]string, len(skipped))
	for _, s := range skipped {
		if strings.TrimSpace(s.Next) == "" {
			t.Errorf("skip %s with code %s has no next action: %+v", s.TaskID, s.Code, s)
		}
		byCode[s.Code] = s.Next
	}
	for _, code := range []SkipCode{
		SkipBadStatus, SkipActiveRun, SkipNoScope, SkipOversizedScope,
		SkipActiveScopeOverlap, SkipSelectedScopeOverlap,
	} {
		if byCode[code] == "" {
			t.Errorf("planner produced no skip with code %s: %+v", code, skipped)
		}
	}
}

func TestSkippedTaskJSONAlwaysCarriesNext(t *testing.T) {
	// A caller outside the planner can build a skip without setting Next.
	// The wire form still has to tell the caller what to do.
	for _, s := range []SkippedTask{
		{TaskID: "T-1", Code: SkipBadStatus, Reason: "task status is done"},
		{TaskID: "T-2", Code: SkipCode("cap_full"), Reason: "no free slot"},
	} {
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("json.Marshal(%+v): %v", s, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("json.Unmarshal(%s): %v", data, err)
		}
		next, ok := decoded["next"].(string)
		if !ok || strings.TrimSpace(next) == "" {
			t.Errorf("JSON %s carries no next action", data)
		}
	}
}
