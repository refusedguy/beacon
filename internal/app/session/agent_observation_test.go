package session

import (
	"context"
	"reflect"
	"testing"
	"time"
)

const testSessionID = "1f3a9c2e-7b4d-4a8e-9c11-2d3e4f5a6b7c"

func TestParseProcessSnapshot(t *testing.T) {
	lines := []string{
		"100 1 /usr/bin/bash",
		"101 100 claude --resume abc",
		"102 101 bash -c ls",
		"PID PPID CMD",
		"oops 100 bad",
		"103",
	}
	want := []Process{
		{PID: 100, Parent: 1, Cmd: "/usr/bin/bash"},
		{PID: 101, Parent: 100, Cmd: "claude --resume abc"},
		{PID: 102, Parent: 101, Cmd: "bash -c ls"},
	}
	got := ParseProcessSnapshot(lines)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseProcessSnapshot = %+v, want %+v", got, want)
	}
}

func TestParsePaneTree(t *testing.T) {
	lines := []string{"pane-a 100", "pane-b 200", "bad", "pane-c 0", "pane-d nope"}
	want := PaneTree{"pane-a": 100, "pane-b": 200}
	got := ParsePaneTree(lines)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePaneTree = %+v, want %+v", got, want)
	}
}

func TestFindAgentNestedProcess(t *testing.T) {
	tree := PaneTree{"pane-a": 100}
	procs := []Process{
		{PID: 100, Parent: 1, Cmd: "bash"},
		{PID: 101, Parent: 100, Cmd: "bash -c run"},
		{PID: 102, Parent: 101, Cmd: "claude --resume"},
	}
	got, ok := FindAgent(tree, procs, IsAgentProcess)
	if !ok {
		t.Fatal("FindAgent did not find the nested agent")
	}
	want := &Agent{PID: 102, Pane: "pane-a", Depth: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindAgent = %+v, want %+v", got, want)
	}
}

func TestFindAgentMinimumDepth(t *testing.T) {
	tree := PaneTree{"pane-a": 100, "pane-b": 200}
	procs := []Process{
		{PID: 100, Parent: 1, Cmd: "bash"},
		{PID: 101, Parent: 100, Cmd: "bash"},
		{PID: 102, Parent: 101, Cmd: "claude deep"},
		{PID: 200, Parent: 1, Cmd: "bash"},
		{PID: 201, Parent: 200, Cmd: "codex shallow"},
	}
	got, ok := FindAgent(tree, procs, IsAgentProcess)
	if !ok {
		t.Fatal("FindAgent did not find an agent")
	}
	if got.PID != 201 || got.Depth != 1 || got.Pane != "pane-b" {
		t.Fatalf("FindAgent = %+v, want PID 201 depth 1 in pane-b", got)
	}
}

func TestFindAgentNoMatch(t *testing.T) {
	tree := PaneTree{"pane-a": 100}
	procs := []Process{{PID: 100, Parent: 1, Cmd: "bash"}}
	if got, ok := FindAgent(tree, procs, IsAgentProcess); ok || got != nil {
		t.Fatalf("FindAgent = (%+v, %v), want (nil, false)", got, ok)
	}
}

func TestFindAgentStopsOnCycle(t *testing.T) {
	tree := PaneTree{"pane-a": 100}
	procs := []Process{
		{PID: 100, Parent: 100, Cmd: "bash"},
		{PID: 101, Parent: 100, Cmd: "aider"},
	}
	got, ok := FindAgent(tree, procs, IsAgentProcess)
	if !ok {
		t.Fatal("a parent cycle hid the agent")
	}
	if got.PID != 101 || got.Depth != 1 {
		t.Fatalf("FindAgent = %+v, want PID 101 depth 1", got)
	}
}

func TestExtractSessionID(t *testing.T) {
	paths := []string{
		"/home/u/notes-" + testSessionID + ".txt",
		"/home/u/.claude/projects/x/" + testSessionID + ".jsonl",
	}
	got, ok := ExtractSessionID(paths)
	if !ok || got != testSessionID {
		t.Fatalf("ExtractSessionID = (%q, %v), want (%q, true)", got, ok, testSessionID)
	}
}

func TestExtractSessionIDRejectsInvalid(t *testing.T) {
	upper := "1F3A9C2E-7B4D-4A8E-9C11-2D3E4F5A6B7C"
	short := "1f3a9c2e-7b4d-4a8e-9c11"
	cases := [][]string{
		{"/home/u/notes.txt"},
		{"/home/u/" + upper + ".jsonl"},
		{"/home/u/" + short + ".jsonl"},
	}
	for _, paths := range cases {
		if got, ok := ExtractSessionID(paths); ok {
			t.Errorf("ExtractSessionID(%q) = %q, want no id", paths, got)
		}
	}
}

func TestIsSessionID(t *testing.T) {
	if !IsSessionID(" " + testSessionID + " ") {
		t.Fatal("IsSessionID rejected a valid id with surrounding spaces")
	}
	if IsSessionID("guess") || IsSessionID("prefix-"+testSessionID) {
		t.Fatal("IsSessionID accepted an invalid id")
	}
}

func TestObserveThreeStates(t *testing.T) {
	if got := Observe(nil).State; got != ObservationUnknown {
		t.Errorf("Observe(nil).State = %v, want ObservationUnknown", got)
	}
	if got := Observe(Snapshot{}).State; got != ObservationUnknown {
		t.Errorf("Observe(empty).State = %v, want ObservationUnknown", got)
	}
	if got := Observe(Snapshot{"pane-a": nil}).State; got != ObservationIdle {
		t.Errorf("Observe(all nil).State = %v, want ObservationIdle", got)
	}
	obs := Observe(Snapshot{
		"pane-a": &Agent{PID: 9, Depth: 3},
		"pane-b": &Agent{PID: 7, Depth: 1},
	})
	if obs.State != ObservationActive || obs.Agent.PID != 7 {
		t.Fatalf("Observe(active) = %+v, want active agent PID 7", obs)
	}
}

func TestMergeObservationUnknownPreserves(t *testing.T) {
	saved := map[string]string{"ai.beacon/taskID": "CD-12", MetaSessionID: testSessionID}
	got := MergeObservation(saved, Observation{State: ObservationUnknown})
	want := map[string]string{"ai.beacon/taskID": "CD-12", MetaSessionID: testSessionID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeObservation(unknown) = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("MergeObservation mutated saved: %#v", saved)
	}
}

func TestMergeObservationIdleResets(t *testing.T) {
	saved := map[string]string{"ai.beacon/taskID": "CD-12", MetaSessionID: testSessionID}
	got := MergeObservation(saved, Observation{State: ObservationIdle})
	want := map[string]string{"ai.beacon/taskID": "CD-12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeObservation(idle) = %#v, want %#v", got, want)
	}
}

func TestMergeObservationActiveConfirms(t *testing.T) {
	const fresh = "aabbccdd-1122-4334-5566-778899aabbcc"
	saved := map[string]string{MetaSessionID: testSessionID}

	got := MergeObservation(saved, Observation{
		State: ObservationActive,
		Agent: Agent{SessionID: fresh},
	})
	if got[MetaSessionID] != fresh {
		t.Fatalf("MergeObservation(active) = %#v, want fresh id", got)
	}

	kept := MergeObservation(saved, Observation{
		State: ObservationActive,
		Agent: Agent{SessionID: "guess"},
	})
	if kept[MetaSessionID] != testSessionID {
		t.Fatalf("MergeObservation(guess) = %#v, want saved id preserved", kept)
	}
}

func TestCommandTimeoutContract(t *testing.T) {
	if CommandTimeout != 1500*time.Millisecond {
		t.Fatalf("CommandTimeout = %v, want 1500ms", CommandTimeout)
	}
	ctx, cancel := CommandContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("CommandContext returned no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > CommandTimeout {
		t.Fatalf("CommandContext deadline in %v, want within (0, %v]", remaining, CommandTimeout)
	}
}
