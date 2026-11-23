package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPlanWaveWithoutGiants covers a wave where every change stays below the
// threshold. All cards run together in one parallel step.
func TestPlanWaveWithoutGiants(t *testing.T) {
	plan := planWave([]giantWork{
		{Card: "HD-001", LoC: 40, Files: []string{"a.go"}},
		{Card: "HD-002", LoC: 900, Files: []string{"b.go"}},
	})
	if !plan.Valid || plan.Reason != "" {
		t.Fatalf("ordinary wave rejected: %+v", plan)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %+v, want one parallel step", plan.Steps)
	}
	step := plan.Steps[0]
	if step.Sequential || len(step.Cards) != 2 || step.Cards[0] != "HD-001" || step.Cards[1] != "HD-002" {
		t.Fatalf("step = %+v, want both cards in one parallel step", step)
	}
}

// TestPlanWaveThresholdBoundary pins the size rule to the card's threshold.
// Exactly 1000 lines is ordinary work; 1001 lines is a giant.
func TestPlanWaveThresholdBoundary(t *testing.T) {
	if isGiant(giantWork{LoC: 1000}) {
		t.Fatal("1000 changed lines must stay below the giant threshold")
	}
	if !isGiant(giantWork{LoC: giantLoCThreshold + 1}) {
		t.Fatal("1001 changed lines must be a giant")
	}
}

// TestPlanWaveGiantRunsAlone covers a wave with a single giant. The plan puts
// that card in a sequential step by itself.
func TestPlanWaveGiantRunsAlone(t *testing.T) {
	plan := planWave([]giantWork{
		{Card: "HD-079", LoC: 1800, Files: []string{"orchestrate.go"}},
	})
	if !plan.Valid || len(plan.Steps) != 1 {
		t.Fatalf("single giant plan = %+v, want one valid step", plan)
	}
	step := plan.Steps[0]
	if !step.Sequential || len(step.Cards) != 1 || step.Cards[0] != "HD-079" {
		t.Fatalf("step = %+v, want HD-079 alone in a sequential step", step)
	}
}

// TestPlanWaveSeparatesGiantFromParallelWork covers a giant with a seam that
// no other card touches. The ordinary cards run first, then the giant runs in
// its own step.
func TestPlanWaveSeparatesGiantFromParallelWork(t *testing.T) {
	plan := planWave([]giantWork{
		{Card: "HD-010", LoC: 120, Files: []string{"small.go"}},
		{Card: "HD-079", LoC: 1500, Files: []string{"orchestrate.go"}},
	})
	if !plan.Valid || len(plan.Steps) != 2 {
		t.Fatalf("mixed wave plan = %+v, want two steps", plan)
	}
	if plan.Steps[0].Sequential || len(plan.Steps[0].Cards) != 1 || plan.Steps[0].Cards[0] != "HD-010" {
		t.Fatalf("first step = %+v, want HD-010 in the parallel step", plan.Steps[0])
	}
	if !plan.Steps[1].Sequential || len(plan.Steps[1].Cards) != 1 || plan.Steps[1].Cards[0] != "HD-079" {
		t.Fatalf("second step = %+v, want HD-079 alone in a sequential step", plan.Steps[1])
	}
}

// TestPlanWaveGivesEachGiantItsOwnStep covers two disjoint giants. They are
// serialized instead of sharing a parallel step.
func TestPlanWaveGivesEachGiantItsOwnStep(t *testing.T) {
	plan := planWave([]giantWork{
		{Card: "HD-A", LoC: 1200, Files: []string{"a.go"}},
		{Card: "HD-B", LoC: 1400, Files: []string{"b.go"}},
	})
	if !plan.Valid || len(plan.Steps) != 2 {
		t.Fatalf("two-giant plan = %+v, want two steps", plan)
	}
	for i, want := range []string{"HD-A", "HD-B"} {
		step := plan.Steps[i]
		if !step.Sequential || len(step.Cards) != 1 || step.Cards[0] != want {
			t.Fatalf("step %d = %+v, want %s alone in a sequential step", i, step, want)
		}
	}
}

// TestPlanWaveRejectsGiantSharingSeam covers the failure in the card: a giant
// that would run beside another card on the same file. The plan rejects it and
// names the shared seam.
func TestPlanWaveRejectsGiantSharingSeam(t *testing.T) {
	plan := planWave([]giantWork{
		{Card: "HD-079", LoC: 1600, Files: []string{"cmd/beacon/orchestrate.go", "x.go"}},
		{Card: "HD-002", LoC: 30, Files: []string{"cmd/beacon/orchestrate.go"}},
	})
	if plan.Valid || len(plan.Steps) != 0 {
		t.Fatalf("parallel giant accepted: %+v", plan)
	}
	if !strings.Contains(plan.Reason, "HD-079") || !strings.Contains(plan.Reason, "cmd/beacon/orchestrate.go") {
		t.Fatalf("reason = %q, want the giant and the shared file", plan.Reason)
	}
}

// TestPlanWaveRejectsGiantsSharingSeam covers two large changes that both own
// the same path. Neither can be isolated, so the wave fails.
func TestPlanWaveRejectsGiantsSharingSeam(t *testing.T) {
	plan := planWave([]giantWork{
		{Card: "HD-A", LoC: 1100, Files: []string{"shared.go"}},
		{Card: "HD-B", LoC: 1300, Files: []string{"shared.go", "other.go"}},
	})
	if plan.Valid || len(plan.Steps) != 0 {
		t.Fatalf("overlapping giants accepted: %+v", plan)
	}
	if !strings.Contains(plan.Reason, "HD-A") && !strings.Contains(plan.Reason, "HD-B") {
		t.Fatalf("reason = %q, want an offending giant", plan.Reason)
	}
}

// TestPlanWaveEmpty has no work to schedule and returns an empty valid plan.
func TestPlanWaveEmpty(t *testing.T) {
	plan := planWave(nil)
	if !plan.Valid || len(plan.Steps) != 0 || plan.Reason != "" {
		t.Fatalf("empty wave = %+v, want valid with no steps", plan)
	}
}

// TestAdmissionLockParallelShareSlotAndExclusiveWaits covers the admission
// contract. Two parallel actions run together, and the exclusive action waits
// for both of them before it starts.
func TestAdmissionLockParallelShareSlotAndExclusiveWaits(t *testing.T) {
	lock := newAdmissionLock()
	ctx := context.Background()
	hold := make(chan struct{})
	parallelStarted := make(chan string, 2)
	var started int32

	var parallelWG sync.WaitGroup
	for _, card := range []string{"P1", "P2"} {
		parallelWG.Add(1)
		go func(card string) {
			defer parallelWG.Done()
			release, err := lock.acquire(ctx, dispatchAction{Card: card, SupportsParallel: true})
			if err != nil {
				t.Errorf("parallel %s acquire: %v", card, err)
				return
			}
			defer release()
			atomic.AddInt32(&started, 1)
			parallelStarted <- card
			<-hold
		}(card)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-parallelStarted:
		case <-time.After(time.Second):
			t.Fatal("two parallel actions did not share the admission slot")
		}
	}

	exclusiveStarted := make(chan string, 1)
	exclusiveDone := make(chan struct{})
	go func() {
		defer close(exclusiveDone)
		release, err := lock.acquire(ctx, dispatchAction{Card: "E"})
		if err != nil {
			t.Errorf("exclusive acquire: %v", err)
			return
		}
		defer release()
		atomic.AddInt32(&started, 1)
		exclusiveStarted <- "E"
	}()

	select {
	case <-exclusiveStarted:
		t.Fatal("exclusive action started while parallel actions held the slot")
	case <-time.After(20 * time.Millisecond):
	}

	close(hold)
	parallelWG.Wait()

	select {
	case <-exclusiveStarted:
	case <-time.After(time.Second):
		t.Fatal("exclusive action did not start after parallel actions released")
	}
	<-exclusiveDone

	if got := atomic.LoadInt32(&started); got != 3 {
		t.Fatalf("started actions = %d, want 3", got)
	}
}

// TestAdmissionLockCanceledBeforeAcquireStartsNothing covers the first
// cancellation check. An action with an already canceled context never enters
// its body.
func TestAdmissionLockCanceledBeforeAcquireStartsNothing(t *testing.T) {
	lock := newAdmissionLock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := 0
	release, err := lock.acquire(ctx, dispatchAction{Card: "P1", SupportsParallel: true})
	if err == nil {
		release()
		started++
	}
	if started != 0 {
		t.Fatalf("started actions = %d, want 0 for a canceled context", started)
	}
}

// TestAdmissionLockCancelWhileWaitingStartsNothing covers cancellation while
// another action owns the slot. The waiter leaves without starting its body,
// and the parallel action still finishes after the slot is released.
func TestAdmissionLockCancelWhileWaitingStartsNothing(t *testing.T) {
	lock := newAdmissionLock()
	ctx := context.Background()
	hold := make(chan struct{})
	parallelStarted := make(chan string, 1)
	parallelDone := make(chan struct{})

	go func() {
		defer close(parallelDone)
		release, err := lock.acquire(ctx, dispatchAction{Card: "P1", SupportsParallel: true})
		if err != nil {
			t.Errorf("parallel acquire: %v", err)
			return
		}
		defer release()
		parallelStarted <- "P1"
		<-hold
	}()
	select {
	case <-parallelStarted:
	case <-time.After(time.Second):
		t.Fatal("parallel action did not enter its slot")
	}

	waitCtx, cancel := context.WithCancel(ctx)
	exclusiveDone := make(chan struct{})
	enteredWait := make(chan struct{})
	var started int32
	go func() {
		defer close(exclusiveDone)
		release, err := lock.acquire(waitCtx, dispatchAction{
			Card: "E",
			WaitReady: func(context.Context) error {
				close(enteredWait)
				return nil
			},
		})
		if err != nil {
			return
		}
		defer release()
		atomic.AddInt32(&started, 1)
	}()

	// The waiter signals from WaitReady, which acquire runs before it touches
	// the slot. Cancel after that signal instead of guessing at a delay.
	select {
	case <-enteredWait:
	case <-time.After(time.Second):
		t.Fatal("exclusive action did not enter the admission wait")
	}
	cancel()
	select {
	case <-exclusiveDone:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not leave the admission wait")
	}

	close(hold)
	<-parallelDone
	if got := atomic.LoadInt32(&started); got != 0 {
		t.Fatalf("started actions = %d, want 0 after cancellation", got)
	}
}
