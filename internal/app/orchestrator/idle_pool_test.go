package orchestrator

import (
	"testing"
	"time"
)

var idleNow = time.Date(2026, time.November, 22, 12, 0, 0, 0, time.UTC)

func TestSelectIdleByOwnerFiles(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"internal/app/orchestrator/idle_pool.go"}}
	agents := []IdleAgent{
		{
			SessionID:    "ses-warm",
			OwnerFiles:   []string{"internal/app/orchestrator/idle_pool.go"},
			LastSeen:     idleNow.Add(-time.Minute),
			WorktreePath: "/worktrees/warm",
		},
		{
			SessionID: "ses-cold",
			OwnerFiles: []string{
				"internal/app/orchestrator/claim.go",
			},
			LastSeen: idleNow.Add(-time.Minute),
		},
	}

	got := SelectIdle(candidate, agents, idleNow)
	if got == nil {
		t.Fatal("SelectIdle returned nil for an agent with the candidate's file")
	}
	if got.SessionID != "ses-warm" {
		t.Errorf("SessionID = %q, want ses-warm", got.SessionID)
	}
	if got.WorktreePath != "/worktrees/warm" {
		t.Errorf("WorktreePath = %q, want /worktrees/warm", got.WorktreePath)
	}
}

func TestSelectIdleByKnownCardIDs(t *testing.T) {
	candidate := IdleAgent{KnownCardIDs: []string{"CO-04"}}
	agents := []IdleAgent{
		{SessionID: "ses-card", KnownCardIDs: []string{"CO-01", "CO-04"}, LastSeen: idleNow},
		{SessionID: "ses-other", KnownCardIDs: []string{"CO-05"}, LastSeen: idleNow},
	}

	got := SelectIdle(candidate, agents, idleNow)
	if got == nil || got.SessionID != "ses-card" {
		t.Fatalf("SelectIdle = %+v, want the agent that knows CO-04", got)
	}
}

func TestSelectIdleRejectsStaleLastSeen(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}
	agents := []IdleAgent{
		{SessionID: "ses-stale", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow.Add(-time.Hour)},
	}

	if got := SelectIdle(candidate, agents, idleNow); got != nil {
		t.Errorf("SelectIdle = %+v, want nil for an hour-old agent", got)
	}
}

func TestSelectIdleUsesPassedFreshness(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}
	agent := IdleAgent{SessionID: "ses-agent", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow.Add(-30 * time.Minute)}
	agents := []IdleAgent{agent}

	if got := SelectIdle(candidate, agents, idleNow, 45*time.Minute); got == nil || got.SessionID != "ses-agent" {
		t.Errorf("SelectIdle with a 45-minute bound = %+v, want ses-agent", got)
	}
	if got := SelectIdle(candidate, agents, idleNow, 15*time.Minute); got != nil {
		t.Errorf("SelectIdle with a 15-minute bound = %+v, want nil", got)
	}
}

func TestSelectIdleRejectsEmptyPool(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}

	if got := SelectIdle(candidate, nil, idleNow); got != nil {
		t.Errorf("SelectIdle with no agents = %+v, want nil", got)
	}
}

func TestSelectIdleRejectsUnrelatedFiles(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}, KnownCardIDs: []string{"CO-04"}}
	agents := []IdleAgent{
		{
			SessionID:    "ses-other",
			OwnerFiles:   []string{"internal/app/orchestrator/claim.go"},
			KnownCardIDs: []string{"CO-05"},
			LastSeen:     idleNow,
		},
	}

	if got := SelectIdle(candidate, agents, idleNow); got != nil {
		t.Errorf("SelectIdle = %+v, want nil when nothing matches", got)
	}
}

func TestSelectIdleRejectsFutureLastSeen(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}
	agents := []IdleAgent{
		{SessionID: "ses-future", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow.Add(time.Minute)},
	}

	if got := SelectIdle(candidate, agents, idleNow); got != nil {
		t.Errorf("SelectIdle = %+v, want nil when LastSeen is after now", got)
	}
}

func TestSelectIdleRejectsCandidateWithoutKeys(t *testing.T) {
	agents := []IdleAgent{
		{SessionID: "ses-agent", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow},
	}

	if got := SelectIdle(IdleAgent{}, agents, idleNow); got != nil {
		t.Errorf("SelectIdle with no candidate keys = %+v, want nil", got)
	}
}

func TestSelectIdleSkipsAgentWithoutSessionID(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}
	agents := []IdleAgent{
		{OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow},
	}

	if got := SelectIdle(candidate, agents, idleNow); got != nil {
		t.Errorf("SelectIdle = %+v, want nil for an agent with no SessionID", got)
	}
}

func TestSelectIdlePicksFreshestThenLowestSessionID(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}
	agents := []IdleAgent{
		{SessionID: "ses-b", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow.Add(-2 * time.Minute)},
		{SessionID: "ses-new", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow.Add(-time.Minute)},
		{SessionID: "ses-a", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow.Add(-2 * time.Minute)},
	}

	got := SelectIdle(candidate, agents, idleNow)
	if got == nil || got.SessionID != "ses-new" {
		t.Fatalf("SelectIdle = %+v, want the agent with the newest LastSeen", got)
	}

	agents[1].LastSeen = idleNow.Add(-2 * time.Minute)
	got = SelectIdle(candidate, agents, idleNow)
	if got == nil || got.SessionID != "ses-a" {
		t.Fatalf("SelectIdle with a tie = %+v, want ses-a by SessionID", got)
	}
}

func TestSelectIdleReturnsCopy(t *testing.T) {
	candidate := IdleAgent{OwnerFiles: []string{"idle_pool.go"}}
	agents := []IdleAgent{
		{SessionID: "ses-agent", OwnerFiles: []string{"idle_pool.go"}, LastSeen: idleNow},
	}

	got := SelectIdle(candidate, agents, idleNow)
	if got == nil {
		t.Fatal("SelectIdle returned nil for a fresh matching agent")
	}
	got.SessionID = "ses-changed"

	if agents[0].SessionID != "ses-agent" {
		t.Errorf("pool SessionID changed to %q, want ses-agent", agents[0].SessionID)
	}
}
