package domain

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fanoutClock struct {
	at time.Time
}

func (c fanoutClock) Now() time.Time { return c.at }

type memStore struct {
	mu           sync.Mutex
	watches      map[string]Watch
	obligations  map[string]Obligation
	activeErr    error
	allErr       error
	saveWatchErr error
	obligErr     error
}

func newMemStore() *memStore {
	return &memStore{
		watches:     make(map[string]Watch),
		obligations: make(map[string]Obligation),
	}
}

func (m *memStore) SaveWatch(w Watch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveWatchErr != nil {
		return m.saveWatchErr
	}
	m.watches[w.ID] = w
	return nil
}

func (m *memStore) Watch(id string) (Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.watches[id]
	if !ok {
		return Watch{}, NewAppError(CodeWatchNotFound, id)
	}
	return w, nil
}

func (m *memStore) ActiveWatches() ([]Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeErr != nil {
		return nil, m.activeErr
	}
	var out []Watch
	for _, w := range m.watches {
		if w.Status != StatusClosed {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *memStore) AllWatches() ([]Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.allErr != nil {
		return nil, m.allErr
	}
	out := make([]Watch, 0, len(m.watches))
	for _, w := range m.watches {
		out = append(out, w)
	}
	return out, nil
}

func (m *memStore) SaveObligation(o Obligation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.obligErr != nil {
		return m.obligErr
	}
	m.obligations[o.ID] = o
	return nil
}

func (m *memStore) Obligation(id string) (Obligation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.obligations[id]
	if !ok {
		return Obligation{}, errors.New("obligation not found")
	}
	return o, nil
}

func (m *memStore) PendingObligations() ([]Obligation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Obligation
	for _, o := range m.obligations {
		if o.DeliveredAt.IsZero() {
			out = append(out, o)
		}
	}
	return out, nil
}

var (
	_ stateStore = (*memStore)(nil)
	_ Clock      = fanoutClock{}
)

func newTestFanout(t *testing.T) (*Fanout, *memStore) {
	t.Helper()
	store := newMemStore()
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	return NewFanout(store, fanoutClock{at: at}), store
}

func assertLimitExceeded(t *testing.T, err error) {
	t.Helper()
	var app *AppError
	if !errors.As(err, &app) {
		t.Fatalf("error %v is not *AppError", err)
	}
	if app.Code != CodeLimitExceeded {
		t.Fatalf("error code = %s, want %s", app.Code, CodeLimitExceeded)
	}
}

func TestSubscribeFanoutExecutorAndOrchestrator(t *testing.T) {
	f, store := newTestFanout(t)

	w := Watch{ID: "w1", Repo: "org/repo", PR: 7, HeadSHA: "sha1"}
	exec, err := f.Subscribe(w, Subscriber{ID: "executor", Role: RoleExecutor})
	if err != nil {
		t.Fatalf("subscribe executor: %v", err)
	}

	// The orchestrator arrives with a different watch ID but the same alias.
	// It must still land on the one active Watch for that PR head.
	orch, err := f.Subscribe(
		Watch{ID: "w-duplicate", Repo: "org/repo", PR: 7, HeadSHA: "sha1"},
		Subscriber{ID: "orchestrator", Role: RoleOrchestrator},
	)
	if err != nil {
		t.Fatalf("subscribe orchestrator: %v", err)
	}

	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active watches = %d, want 1", len(active))
	}
	if active[0].ID != "w1" {
		t.Fatalf("watch ID = %q, want w1", active[0].ID)
	}
	if orch.Watch.ID != exec.Watch.ID {
		t.Fatalf("orchestrator watch %q != executor watch %q", orch.Watch.ID, exec.Watch.ID)
	}

	if exec.Obligation.ID == orch.Obligation.ID {
		t.Fatalf("subscribers share obligation ID %q", exec.Obligation.ID)
	}
	wantExec := ObligationKey("w1", w.Revision, "executor")
	wantOrch := ObligationKey("w1", w.Revision, "orchestrator")
	if exec.Obligation.ID != wantExec {
		t.Fatalf("executor obligation = %q, want %q", exec.Obligation.ID, wantExec)
	}
	if orch.Obligation.ID != wantOrch {
		t.Fatalf("orchestrator obligation = %q, want %q", orch.Obligation.ID, wantOrch)
	}
	if exec.Obligation.SubscriberID != "executor" || orch.Obligation.SubscriberID != "orchestrator" {
		t.Fatalf("obligation subscriber IDs are swapped: %+v / %+v", exec.Obligation, orch.Obligation)
	}

	if len(orch.Watch.Subscribers) != 2 {
		t.Fatalf("subscribers = %d, want 2", len(orch.Watch.Subscribers))
	}
	roles := map[Role]bool{}
	for _, s := range orch.Watch.Subscribers {
		roles[s.Role] = true
	}
	if !roles[RoleExecutor] || !roles[RoleOrchestrator] {
		t.Fatalf("subscriber roles = %v, want executor and orchestrator", roles)
	}

	pending, err := store.PendingObligations()
	if err != nil {
		t.Fatalf("pending obligations: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending obligations = %d, want 2", len(pending))
	}
}

func TestSubscribeRenewKeepsObligation(t *testing.T) {
	f, store := newTestFanout(t)

	w := Watch{ID: "w1", Repo: "org/repo", PR: 7, HeadSHA: "sha1"}
	exec, err := f.Subscribe(w, Subscriber{ID: "executor", Role: RoleExecutor})
	if err != nil {
		t.Fatalf("subscribe executor: %v", err)
	}
	if _, err := f.Subscribe(exec.Watch, Subscriber{ID: "orchestrator", Role: RoleOrchestrator}); err != nil {
		t.Fatalf("subscribe orchestrator: %v", err)
	}

	renewed, err := f.Subscribe(exec.Watch, Subscriber{ID: "executor", Role: RoleExecutor})
	if err != nil {
		t.Fatalf("renew executor: %v", err)
	}
	if !renewed.Renewed {
		t.Fatal("renewing an existing subscriber must report Renewed")
	}
	if renewed.Obligation.ID != exec.Obligation.ID {
		t.Fatalf("renew changed obligation: %q, want %q", renewed.Obligation.ID, exec.Obligation.ID)
	}
	if len(renewed.Watch.Subscribers) != 2 {
		t.Fatalf("subscribers = %d after renew, want 2", len(renewed.Watch.Subscribers))
	}
	if renewed.Watch.Revision != exec.Watch.Revision {
		t.Fatalf("renew changed revision: %d, want %d", renewed.Watch.Revision, exec.Watch.Revision)
	}

	pending, err := store.PendingObligations()
	if err != nil {
		t.Fatalf("pending obligations: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending obligations = %d after renew, want 2", len(pending))
	}
}

func TestSubscribeFiftyFirstSubscriberLimitExceeded(t *testing.T) {
	f, store := newTestFanout(t)

	w := Watch{ID: "w1", Repo: "org/repo", PR: 7, HeadSHA: "sha1"}
	current := w
	for i := 1; i <= maxSubscribersPerWatch; i++ {
		res, err := f.Subscribe(current, Subscriber{ID: fmt.Sprintf("s%d", i), Role: RoleExecutor})
		if err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		current = res.Watch
	}
	if len(current.Subscribers) != maxSubscribersPerWatch {
		t.Fatalf("subscribers = %d, want %d", len(current.Subscribers), maxSubscribersPerWatch)
	}

	_, err := f.Subscribe(current, Subscriber{ID: "s51", Role: RoleExecutor})
	assertLimitExceeded(t, err)

	after, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(after) != 1 || len(after[0].Subscribers) != maxSubscribersPerWatch {
		t.Fatalf("rejected subscriber changed state: %+v", after)
	}
}

func TestSubscribePerSubscriberWatchLimit(t *testing.T) {
	f, store := newTestFanout(t)

	for i := 1; i <= maxWatchesPerSubscriber; i++ {
		w := Watch{ID: fmt.Sprintf("w%d", i), Repo: "org/repo", PR: i, HeadSHA: fmt.Sprintf("sha%d", i)}
		if _, err := f.Subscribe(w, Subscriber{ID: "busy", Role: RoleExecutor}); err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
	}

	w := Watch{ID: "w51", Repo: "org/repo", PR: 51, HeadSHA: "sha51"}
	_, err := f.Subscribe(w, Subscriber{ID: "busy", Role: RoleExecutor})
	assertLimitExceeded(t, err)

	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(active) != maxWatchesPerSubscriber {
		t.Fatalf("active watches = %d, want %d", len(active), maxWatchesPerSubscriber)
	}
}

func TestSubscribeGlobalWatchLimit(t *testing.T) {
	f, store := newTestFanout(t)

	for i := 1; i <= maxActiveWatches; i++ {
		w := Watch{ID: fmt.Sprintf("w%d", i), Repo: "org/repo", PR: i, HeadSHA: fmt.Sprintf("sha%d", i)}
		s := Subscriber{ID: fmt.Sprintf("s%d", i), Role: RoleExecutor}
		if _, err := f.Subscribe(w, s); err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
	}

	w := Watch{ID: "w201", Repo: "org/repo", PR: 201, HeadSHA: "sha201"}
	_, err := f.Subscribe(w, Subscriber{ID: "s201", Role: RoleExecutor})
	assertLimitExceeded(t, err)

	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(active) != maxActiveWatches {
		t.Fatalf("active watches = %d, want %d", len(active), maxActiveWatches)
	}
}

func TestSubscribeClosedWatchDoesNotBlockAlias(t *testing.T) {
	f, store := newTestFanout(t)

	w := Watch{ID: "old", Repo: "org/repo", PR: 7, HeadSHA: "sha1"}
	res, err := f.Subscribe(w, Subscriber{ID: "executor", Role: RoleExecutor})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	closed := res.Watch
	closed.Status = StatusClosed
	closed.CloseReason = "head_changed"
	if err := store.SaveWatch(closed); err != nil {
		t.Fatalf("close watch: %v", err)
	}

	// A closed watch no longer owns the alias; a fresh Watch may claim it.
	fresh, err := f.Subscribe(
		Watch{ID: "new", Repo: "org/repo", PR: 7, HeadSHA: "sha1"},
		Subscriber{ID: "orchestrator", Role: RoleOrchestrator},
	)
	if err != nil {
		t.Fatalf("subscribe new watch: %v", err)
	}
	if fresh.Watch.ID != "new" {
		t.Fatalf("watch ID = %q, want new", fresh.Watch.ID)
	}

	active, err := store.ActiveWatches()
	if err != nil {
		t.Fatalf("active watches: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active watches = %d, want 1", len(active))
	}
}

func TestSubscribeDerivesIDAndFillsDefaults(t *testing.T) {
	f, _ := newTestFanout(t)

	res, err := f.Subscribe(
		Watch{Repo: "org/repo", PR: 7, HeadSHA: "sha1"},
		Subscriber{ID: "executor", Role: RoleExecutor},
	)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if res.Watch.ID != watchID("org/repo", 7, "sha1", res.Watch.CreatedAt) {
		t.Fatalf("derived ID = %q", res.Watch.ID)
	}
	if res.Watch.Status != StatusWatching || res.Watch.Verdict != VerdictWatching {
		t.Fatalf("defaults not filled: %+v", res.Watch)
	}
	if res.Watch.CreatedAt.IsZero() {
		t.Fatal("CreatedAt was not filled from the clock")
	}
	if res.Obligation.StagedAt.IsZero() {
		t.Fatal("StagedAt was not filled from the clock")
	}
}

func TestEmitStreamEventMonotonicSeq(t *testing.T) {
	f, _ := newTestFanout(t)

	for i := 0; i < 5; i++ {
		ev := f.EmitStreamEvent("w1", []byte(fmt.Sprintf("chunk %d", i)))
		if ev.Seq != uint64(i+1) {
			t.Fatalf("chunk %d seq = %d, want %d", i, ev.Seq, i+1)
		}
		if ev.WatchID != "w1" {
			t.Errorf("chunk %d watchID = %q, want w1", i, ev.WatchID)
		}
		if string(ev.Payload) != fmt.Sprintf("chunk %d", i) {
			t.Errorf("chunk %d payload = %q", i, ev.Payload)
		}
	}
}

func TestEmitStreamEventSeqIsUniqueUnderConcurrency(t *testing.T) {
	f, _ := newTestFanout(t)

	const goroutines = 20
	seqs := make(chan uint64, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seqs <- f.EmitStreamEvent("w1", []byte("x")).Seq
		}()
	}
	wg.Wait()
	close(seqs)

	seen := make(map[uint64]bool, goroutines)
	for seq := range seqs {
		if seq == 0 || seq > goroutines {
			t.Errorf("seq = %d, want within 1..%d", seq, goroutines)
		}
		if seen[seq] {
			t.Errorf("duplicate seq %d emitted", seq)
		}
		seen[seq] = true
	}
	if len(seen) != goroutines {
		t.Errorf("unique seqs = %d, want %d", len(seen), goroutines)
	}
}

func TestSubscribeErrors(t *testing.T) {
	t.Run("empty subscriber id", func(t *testing.T) {
		f, _ := newTestFanout(t)
		_, err := f.Subscribe(Watch{Repo: "org/repo", PR: 7, HeadSHA: "sha1"}, Subscriber{})
		var app *AppError
		if !errors.As(err, &app) || app.Code != CodeInvalidInput {
			t.Fatalf("error = %v, want INVALID_INPUT", err)
		}
	})

	t.Run("nil store", func(t *testing.T) {
		f := NewFanout(nil, fanoutClock{at: time.Now()})
		_, err := f.Subscribe(Watch{Repo: "org/repo", PR: 7, HeadSHA: "sha1"}, Subscriber{ID: "s"})
		var app *AppError
		if !errors.As(err, &app) || app.Code != CodePersistFailed {
			t.Fatalf("error = %v, want PERSIST_FAILED", err)
		}
	})

	t.Run("persist failure", func(t *testing.T) {
		f, store := newTestFanout(t)
		store.obligErr = errors.New("disk full")
		_, err := f.Subscribe(Watch{Repo: "org/repo", PR: 7, HeadSHA: "sha1"}, Subscriber{ID: "s"})
		var app *AppError
		if !errors.As(err, &app) || app.Code != CodePersistFailed {
			t.Fatalf("error = %v, want PERSIST_FAILED", err)
		}
	})
}

func TestSubscribePreservesStoreErrorCause(t *testing.T) {
	noRows := sql.ErrNoRows
	disk := errors.New("disk full")

	cases := []struct {
		name    string
		breakAt func(*memStore)
		want    error
		other   error
	}{
		{"active watches", func(s *memStore) { s.activeErr = noRows }, noRows, disk},
		{"all watches", func(s *memStore) { s.allErr = disk }, disk, noRows},
		{"save watch", func(s *memStore) { s.saveWatchErr = noRows }, noRows, disk},
		{"save obligation", func(s *memStore) { s.obligErr = disk }, disk, noRows},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, store := newTestFanout(t)
			tc.breakAt(store)

			_, err := f.Subscribe(
				Watch{Repo: "org/repo", PR: 7, HeadSHA: "sha1"},
				Subscriber{ID: "s"},
			)
			var app *AppError
			if !errors.As(err, &app) || app.Code != CodePersistFailed {
				t.Fatalf("error = %v, want PERSIST_FAILED AppError", err)
			}
			if app.Message != tc.want.Error() {
				t.Fatalf("message = %q, want %q", app.Message, tc.want.Error())
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.want)
			}
			if errors.Is(err, tc.other) {
				t.Fatalf("errors.Is(%v, %v) = true, want false", err, tc.other)
			}
		})
	}
}

// unclosedStore models the real DB at this point: ActiveWatches is narrow
// (watching and notifying), LiveWatches adds blocked, and UnclosedWatches adds
// needs_input and parked. It lets the alias test fail when the Fanout looks
// only at the active or live set.
type unclosedStore struct {
	*memStore
}

func (s *unclosedStore) ActiveWatches() ([]Watch, error) {
	all, err := s.memStore.AllWatches()
	if err != nil {
		return nil, err
	}
	var out []Watch
	for _, w := range all {
		if w.Status == StatusWatching || w.Status == StatusNotifying {
			out = append(out, w)
		}
	}
	return out, nil
}

func (s *unclosedStore) LiveWatches() ([]Watch, error) {
	all, err := s.memStore.AllWatches()
	if err != nil {
		return nil, err
	}
	var out []Watch
	for _, w := range all {
		if w.Status == StatusWatching || w.Status == StatusNotifying || w.Status == StatusBlocked {
			out = append(out, w)
		}
	}
	return out, nil
}

func (s *unclosedStore) UnclosedWatches() ([]Watch, error) {
	return s.memStore.ActiveWatches()
}

var (
	_ liveWatchSource     = (*unclosedStore)(nil)
	_ unclosedWatchSource = (*unclosedStore)(nil)
)

func TestSubscribeRenewsUnclosedWatchOnSameAlias(t *testing.T) {
	cases := []struct {
		name    string
		status  Status
		verdict Verdict
	}{
		{"needs_input", StatusNeedsInput, VerdictNeedsInput},
		{"parked", StatusParked, VerdictParked},
		{"blocked", StatusBlocked, VerdictBlocked},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &unclosedStore{memStore: newMemStore()}
			at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			f := NewFanout(store, fanoutClock{at: at})

			existing := Watch{
				ID:          "watch:original",
				Repo:        "refusedguy/beacon",
				PR:          42,
				HeadSHA:     "deadbeef",
				Status:      tc.status,
				Verdict:     tc.verdict,
				Revision:    4,
				Subscribers: []Subscriber{{ID: "executor", Role: RoleExecutor}},
			}
			if err := store.SaveWatch(existing); err != nil {
				t.Fatalf("seed watch: %v", err)
			}

			res, err := f.Subscribe(
				Watch{Repo: existing.Repo, PR: existing.PR, HeadSHA: existing.HeadSHA,
					Status: StatusWatching, Verdict: VerdictWatching},
				Subscriber{ID: "executor", Role: RoleExecutor},
			)
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			if res.Watch.ID != existing.ID {
				t.Fatalf("watch ID = %q, want %q", res.Watch.ID, existing.ID)
			}
			if res.Watch.Status != tc.status {
				t.Errorf("status = %q, want %q", res.Watch.Status, tc.status)
			}
			if res.Watch.Verdict != tc.verdict {
				t.Errorf("verdict = %q, want %q", res.Watch.Verdict, tc.verdict)
			}
			if !res.Renewed {
				t.Error("renewed = false, want true")
			}

			all, err := store.AllWatches()
			if err != nil {
				t.Fatalf("all watches: %v", err)
			}
			if len(all) != 1 {
				t.Fatalf("watch count = %d, want 1", len(all))
			}
			if len(all[0].Subscribers) != 1 {
				t.Fatalf("subscriber count = %d, want 1", len(all[0].Subscribers))
			}
		})
	}
}
