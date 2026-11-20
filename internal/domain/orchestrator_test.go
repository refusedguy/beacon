package domain

import (
	"errors"
	"testing"
	"time"
)

// orchestratorClock is a fixed Clock so RegisteredAt is an exact instant.
type orchestratorClock struct{ at time.Time }

func (c orchestratorClock) Now() time.Time { return c.at }

func newTestOrchestrator(t *testing.T, id, project string, slots int) Orchestrator {
	t.Helper()
	o, err := NewOrchestrator(id, "", project, slots, orchestratorClock{at: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	return o
}

func TestNewOrchestrator(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	o, err := NewOrchestrator("ses_1", "", "project-a", DefaultSlotsTotal, orchestratorClock{at: at})
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	if o.ID != "ses_1" || o.ProjectKey != "project-a" {
		t.Fatalf("identity = %q/%q, want ses_1/project-a", o.ID, o.ProjectKey)
	}
	if o.Role != RoleOrchestrator {
		t.Fatalf("Role = %q, want %q", o.Role, RoleOrchestrator)
	}
	if o.SlotsTotal != DefaultSlotsTotal || o.SlotsUsed != 0 {
		t.Fatalf("slots = %d/%d, want %d/0", o.SlotsUsed, o.SlotsTotal, DefaultSlotsTotal)
	}
	if !o.RegisteredAt.Equal(at) {
		t.Fatalf("RegisteredAt = %v, want %v", o.RegisteredAt, at)
	}
	if o.Subscriptions == nil || len(o.Subscriptions) != 0 {
		t.Fatalf("Subscriptions = %#v, want empty non-nil", o.Subscriptions)
	}
	if !o.CanAcceptTask() {
		t.Fatal("fresh orchestrator must accept a task")
	}
}

func TestNewOrchestratorRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name         string
		id, prj      string
		role         Role
		slots        int
	}{
		{"empty id", "", "project-a", "", 2},
		{"blank id", "  ", "project-a", "", 2},
		{"empty project", "ses_1", "", "", 2},
		{"executor role", "ses_1", "project-a", RoleExecutor, 2},
		{"negative slots", "ses_1", "project-a", "", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewOrchestrator(tc.id, tc.role, tc.prj, tc.slots, nil); err == nil {
				t.Fatal("want INVALID_INPUT")
			} else {
				var app *AppError
				if !errors.As(err, &app) || app.Code != CodeInvalidInput {
					t.Fatalf("error = %v, want INVALID_INPUT", err)
				}
			}
		})
	}
}

func TestNewOrchestratorZeroSlots(t *testing.T) {
	o, err := NewOrchestrator("ses_0", "", "project-a", 0, nil)
	if err != nil {
		t.Fatalf("zero slots must be valid: %v", err)
	}
	if o.CanAcceptTask() {
		t.Fatal("zero slots must not accept a task")
	}
	if o.RegisteredAt.IsZero() {
		t.Fatal("nil clock must fall back to the wall clock")
	}
}

func TestOrchestratorRegisterIdempotent(t *testing.T) {
	o := newTestOrchestrator(t, "ses_1", "project-a", 2)
	if err := o.Register("w1"); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	if o.SlotsUsed != 1 || len(o.Subscriptions) != 1 || o.Subscriptions[0] != "w1" {
		t.Fatalf("after first Register: slots=%d subs=%#v", o.SlotsUsed, o.Subscriptions)
	}
	if err := o.Register("w1"); err != nil {
		t.Fatalf("repeat Register w1: %v", err)
	}
	if o.SlotsUsed != 1 || len(o.Subscriptions) != 1 {
		t.Fatalf("idempotent Register changed state: slots=%d subs=%#v", o.SlotsUsed, o.Subscriptions)
	}
	if !o.HasSubscription("w1") || o.HasSubscription("w2") {
		t.Fatal("HasSubscription incorrect after Register")
	}
	if err := o.Register(" "); err == nil {
		t.Fatal("blank watchID must be rejected")
	}
	var nilOrch *Orchestrator
	if err := nilOrch.Register("w1"); err == nil {
		t.Fatal("nil orchestrator must reject Register")
	}
}

func TestOrchestratorSlotLimit(t *testing.T) {
	o := newTestOrchestrator(t, "ses_1", "project-a", 2)
	if err := o.Register("w1"); err != nil {
		t.Fatal(err)
	}
	if err := o.Register("w2"); err != nil {
		t.Fatal(err)
	}
	if o.CanAcceptTask() {
		t.Fatal("both slots used: CanAcceptTask must be false")
	}
	if err := o.Register("w3"); err == nil {
		t.Fatal("third Register must fail with LIMIT_EXCEEDED")
	} else {
		var app *AppError
		if !errors.As(err, &app) || app.Code != CodeLimitExceeded {
			t.Fatalf("error = %v, want LIMIT_EXCEEDED", err)
		}
	}
	if o.SlotsUsed != 2 || len(o.Subscriptions) != 2 || o.HasSubscription("w3") {
		t.Fatalf("failed Register changed state: slots=%d subs=%#v", o.SlotsUsed, o.Subscriptions)
	}

	// Unregister frees exactly one slot and is itself idempotent.
	if !o.Unregister("w1") {
		t.Fatal("Unregister w1 must report removed")
	}
	if o.Unregister("w1") {
		t.Fatal("second Unregister w1 must be a no-op")
	}
	if o.Unregister("missing") {
		t.Fatal("Unregister of an unknown ID must report not removed")
	}
	if o.SlotsUsed != 1 || !o.CanAcceptTask() {
		t.Fatalf("after unregister: slots=%d can=%v", o.SlotsUsed, o.CanAcceptTask())
	}
	if err := o.Register("w3"); err != nil {
		t.Fatalf("slot must be reusable: %v", err)
	}
	if o.SlotsUsed != 2 || len(o.Subscriptions) != 2 {
		t.Fatalf("after reuse: slots=%d subs=%#v", o.SlotsUsed, o.Subscriptions)
	}
}

func TestOrchestratorWorkloadBySubscriptions(t *testing.T) {
	o := newTestOrchestrator(t, "ses_1", "project-a", 8)
	watches := map[string]Watch{
		"ready":    {ID: "ready", Verdict: VerdictReady},
		"retry":    {ID: "retry", Verdict: VerdictRetry},
		"blocked":  {ID: "blocked", Status: StatusBlocked, Verdict: VerdictBlocked},
		"watching": {ID: "watching", Verdict: VerdictWatching},
		"pending":  {ID: "pending", Verdict: VerdictKeepWorking},
	}
	for id := range watches {
		if err := o.Register(id); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}
	// A subscription with no resolvable Watch is skipped, not guessed.
	if err := o.Register("gone"); err != nil {
		t.Fatal(err)
	}

	got := o.Workload(watches)
	want := Workload{Ready: 1, Retry: 1, Blocked: 1, Watching: 1}
	if got != want {
		t.Fatalf("Workload = %+v, want %+v", got, want)
	}
	if got.Total() != 4 {
		t.Fatalf("Total = %d, want 4", got.Total())
	}

	// Register does not substitute or mutate the Watches it points at.
	if len(watches) != 5 || watches["ready"].Verdict != VerdictReady {
		t.Fatalf("Register mutated the Watch lookup: %#v", watches)
	}
}

func TestOrchestratorWorkloadReactsToUnsubscribe(t *testing.T) {
	o := newTestOrchestrator(t, "ses_1", "project-a", 2)
	watches := map[string]Watch{
		"r1": {ID: "r1", Verdict: VerdictReady},
		"r2": {ID: "r2", Verdict: VerdictReady},
	}
	for _, id := range []string{"r1", "r2"} {
		if err := o.Register(id); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.Workload(watches).Ready; got != 2 {
		t.Fatalf("Ready = %d, want 2", got)
	}
	o.Unregister("r1")
	if got := o.Workload(watches).Ready; got != 1 {
		t.Fatalf("Ready after Unregister = %d, want 1", got)
	}
}

func TestOrchestratorProjectIsolation(t *testing.T) {
	alpha := newTestOrchestrator(t, "ses_shared", "project-alpha", 2)
	beta := newTestOrchestrator(t, "ses_shared", "project-beta", 2)
	if alpha.Key() == beta.Key() {
		t.Fatalf("same session ID in different projects collapsed to one key %q", alpha.Key())
	}

	watches := map[string]Watch{"w1": {ID: "w1", Verdict: VerdictReady}}
	if err := alpha.Register("w1"); err != nil {
		t.Fatal(err)
	}
	if beta.HasSubscription("w1") || beta.SlotsUsed != 0 || !beta.CanAcceptTask() {
		t.Fatalf("registration leaked across projects: slots=%d subs=%#v", beta.SlotsUsed, beta.Subscriptions)
	}
	if got := beta.Workload(watches); got != (Workload{}) {
		t.Fatalf("beta workload = %+v, want zero", got)
	}
	if got := alpha.Workload(watches).Ready; got != 1 {
		t.Fatalf("alpha workload Ready = %d, want 1", got)
	}

	// A repeated ID in the same project is the same orchestrator identity.
	alphaAgain := newTestOrchestrator(t, "ses_shared", "project-alpha", 2)
	if alphaAgain.Key() != alpha.Key() {
		t.Fatalf("same project and session must share a key: %q != %q", alphaAgain.Key(), alpha.Key())
	}
}

func TestOrchestratorKeyEscapesSeparators(t *testing.T) {
	a := newTestOrchestrator(t, "a:b", "p", 1)
	b := newTestOrchestrator(t, "a", "p:b", 1)
	if a.Key() == b.Key() {
		t.Fatalf("separator collision: %q == %q", a.Key(), b.Key())
	}
}
