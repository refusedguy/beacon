package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// casRunStore adds a real compare-and-swap save to the shared fake store.
// Reads return copies, so a reconcile cannot change a stored version through
// the pointer it was given.
type casRunStore struct {
	*fakeStore
	saves int
}

func (s *casRunStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *casRunStore) Save(_ context.Context, r *core.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.runs[r.ID] = &cp
	return nil
}

func (s *casRunStore) SaveCAS(_ context.Context, r *core.Run, expectedAt int64, expectedSHA string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.runs[r.ID]
	if stored == nil {
		return core.Errorf(core.CodeNotFound, "run %s no longer exists", r.ID)
	}
	if stored.UpdatedAt.UnixNano() != expectedAt || stored.HeadSHA != expectedSHA {
		return core.Errorf(core.CodeConflict, "run %s changed since it was read", r.ID)
	}
	cp := *r
	s.runs[r.ID] = &cp
	s.saves++
	return nil
}

func (s *casRunStore) FindByPR(_ context.Context, pr int) (*core.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.PRNumber == pr && !r.State.Terminal() {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *casRunStore) Get(_ context.Context, id core.RunID) (*core.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func newCASRunStore(run *core.Run) *casRunStore {
	store := &casRunStore{fakeStore: newFakeStore()}
	if run != nil {
		if err := store.Save(context.Background(), run); err != nil {
			panic(err)
		}
	}
	return store
}

func casObservation() core.Observation {
	return core.Observation{PRNumber: 42, PRState: "open", HeadSHA: "sha2"}
}

func TestReconcileRejectsUnboundPR(t *testing.T) {
	r := &Reconciler{Store: newCASRunStore(nil)}

	decision, err := r.Reconcile(context.Background(), 42)

	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("Reconcile err = %v, want coded not_found", err)
	}
	if decision.Verdict != "" || decision.Reason != "" {
		t.Fatalf("decision = %+v, want an empty decision with an explicit error", decision)
	}
}

// overwritingSCM writes a newer run version during Observe. It models a daemon
// transition that lands after a forced reconcile read the run but before it
// saved its own decision.
type overwritingSCM struct {
	*fakeSCM
	store *casRunStore
	once  sync.Once
}

func (s *overwritingSCM) Observe(ctx context.Context, pr int) (core.Observation, error) {
	s.once.Do(func() {
		run, err := s.store.Get(ctx, "r1")
		if err != nil || run == nil {
			return
		}
		if err := run.Apply(core.Event{Kind: core.EvBehind, HeadSHA: "concurrent"}); err != nil {
			return
		}
		_ = s.store.Save(ctx, run)
	})
	return s.fakeSCM.Observe(ctx, pr)
}

func TestReconcileRefusesToOverwriteConcurrentTransition(t *testing.T) {
	ctx := context.Background()
	store := newCASRunStore(makeRun("r1", 42, core.RunWaitingCI))
	scm := &overwritingSCM{fakeSCM: &fakeSCM{obs: casObservation()}, store: store}
	r := &Reconciler{
		Store:  store,
		SCM:    scm,
		Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictBlocked, Reason: "ci failed"}},
	}

	_, err := r.Reconcile(ctx, 42)

	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("Reconcile err = %v, want coded conflict for the stale write", err)
	}
	stored, err := store.Get(ctx, "r1")
	if err != nil || stored == nil {
		t.Fatalf("store.Get: run = %v, err = %v", stored, err)
	}
	if stored.State != core.RunNeedsRebase || stored.HeadSHA != "concurrent" {
		t.Fatalf("stored run = %s/%s, want needs_rebase/concurrent", stored.State, stored.HeadSHA)
	}
	if got := store.saveCount(); got != 0 {
		t.Fatalf("compare-and-swap saves = %d, want none to land on a stale version", got)
	}
}

// gatedSCM lets two reconciles read the same run version before either saves.
type gatedSCM struct {
	*fakeSCM
	entered chan struct{}
	release chan struct{}
}

func (s *gatedSCM) Observe(ctx context.Context, pr int) (core.Observation, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.fakeSCM.Observe(ctx, pr)
}

type reconcileOutcome struct {
	decision core.MergeDecision
	err      error
}

func TestTwoReconcilersLeaveOneTransition(t *testing.T) {
	ctx := context.Background()
	store := newCASRunStore(makeRun("r1", 42, core.RunWaitingCI))
	scm := &gatedSCM{
		fakeSCM: &fakeSCM{obs: casObservation()},
		entered: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	r := &Reconciler{
		Store:  store,
		SCM:    scm,
		Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictBlocked, Reason: "ci failed"}},
	}

	out := make(chan reconcileOutcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			decision, err := r.Reconcile(ctx, 42)
			out <- reconcileOutcome{decision: decision, err: err}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-scm.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for both reconciles to read the run")
		}
	}
	close(scm.release)

	conflicts, clean := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case got := <-out:
			switch {
			case got.err == nil:
				clean++
			case core.CodeOf(got.err) == core.CodeConflict:
				conflicts++
			default:
				t.Fatalf("unexpected reconcile error: %v", got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for both reconciles to finish")
		}
	}
	if clean != 1 || conflicts != 1 {
		t.Fatalf("outcomes = %d clean, %d conflicts, want 1 of each", clean, conflicts)
	}
	if got := store.saveCount(); got != 1 {
		t.Fatalf("compare-and-swap saves = %d, want 1", got)
	}
	stored, err := store.Get(ctx, "r1")
	if err != nil || stored == nil {
		t.Fatalf("store.Get: run = %v, err = %v", stored, err)
	}
	if stored.State != core.RunBlocked {
		t.Fatalf("stored state = %s, want blocked", stored.State)
	}
}
