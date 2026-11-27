package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

type terminalRunFinder struct {
	runs map[core.TaskID]*core.Run
}

func (f terminalRunFinder) FindActiveByTask(_ context.Context, id core.TaskID) (*core.Run, error) {
	return f.runs[id], nil
}

func TestActiveTaskStatesIgnoresTerminalRuns(t *testing.T) {
	closed := &core.Run{ID: "r1", TaskID: "T-1", State: core.RunClosed}
	merged := &core.Run{ID: "r2", TaskID: "T-2", State: core.RunMerged}
	active := &core.Run{ID: "r3", TaskID: "T-3", State: core.RunWaitingCI}
	finder := terminalRunFinder{runs: map[core.TaskID]*core.Run{
		"T-1": closed, "T-2": merged, "T-3": active,
	}}
	got, err := activeTaskStates(context.Background(), finder, []string{"T-1", "T-2", "T-3"})
	if err != nil {
		t.Fatalf("activeTaskStates: %v", err)
	}
	if len(got) != 1 || got["T-3"] != core.RunWaitingCI {
		t.Fatalf("active = %+v, want only T-3", got)
	}
}
