package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

const (
	controlBenchName    = "control-no-daemon"
	controlBenchLimiter = "two config loads + one Control.Apply per action"
	controlBenchCalls   = 100
)

// wakeRecordingHarness records the Wake calls made by Control.Nudge. Every
// other harness method is inert, so no control action leaves the local store.
type wakeRecordingHarness struct {
	excerpts []string
}

func (h *wakeRecordingHarness) Spawn(context.Context, core.SpawnRequest) (core.Session, error) {
	return core.Session{}, nil
}
func (h *wakeRecordingHarness) Wake(_ context.Context, _ string, wake core.WakeContext) error {
	h.excerpts = append(h.excerpts, wake.Excerpt)
	return nil
}
func (h *wakeRecordingHarness) Stop(context.Context, string) error                 { return nil }
func (h *wakeRecordingHarness) IsAlive(context.Context, string) (bool, error)      { return false, nil }
func (h *wakeRecordingHarness) NotifyParent(context.Context, string, string) error { return nil }
func (h *wakeRecordingHarness) Events() <-chan core.HarnessEvent                   { return nil }

// seedControlRun writes one run in the state the next verb needs. Cancel and
// nudge start running, unblock starts blocked and retry starts closed. Each
// verb then has one legal transition and reaches the store or the harness.
func seedControlRun(store *sqlite.Store, action string) error {
	state := core.RunRunning
	session := ""
	switch action {
	case "unblock":
		state = core.RunBlocked
	case "retry":
		state = core.RunClosed
	case "nudge":
		session = "ses-bench"
	}
	run := core.NewRun("r1", "T-1", time.Now().UTC())
	run.State = state
	run.SessionID = session
	run.HeadSHA = "abc123"
	return store.Save(context.Background(), run)
}

// controlFixture pins a temp project root with a config and no connection
// file, so dispatchControlWithPR takes the local path. Its wireFn opens a
// fresh SQLite store for each call, seeds the run for that call and returns
// a wiring whose close releases that same store.
type controlFixture struct {
	configPath    string
	dbPath        string
	harness       *wakeRecordingHarness
	ctrl          *app.Control
	fn            wireFn
	wireCalls     int64
	currentAction string
	message       string
}

func newControlFixture(b *testing.B) *controlFixture {
	b.Helper()
	root := b.TempDir()
	b.Setenv("BEACON_PROJECT_ROOT", root)
	configPath := filepath.Join(root, "config.yaml")
	body := "state_db: state.db\nworkspace_dir: worktrees\nbase_branch: main\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		b.Fatalf("write config: %v", err)
	}

	f := &controlFixture{
		configPath: configPath,
		dbPath:     filepath.Join(root, "state.db"),
		harness:    &wakeRecordingHarness{},
	}
	f.ctrl = &app.Control{Harness: f.harness}
	f.fn = wireFn(func(path, parentSession string) (*wiring, error) {
		atomic.AddInt64(&f.wireCalls, 1)
		store, err := sqlite.Open(f.dbPath)
		if err != nil {
			return nil, err
		}
		if err := seedControlRun(store, f.action()); err != nil {
			store.Close()
			return nil, err
		}
		f.ctrl.Store = store
		return &wiring{store: store, control: f.ctrl}, nil
	})
	return f
}

// action returns the verb of the call in flight. The benchmark is single
// threaded, so this field is set immediately before each dispatch.
func (f *controlFixture) action() string { return f.currentAction }

// verify checks that the requested verb reached the store or the harness.
// Cancel closes the run, unblock returns it to waiting_ci, retry returns it
// to pending, and nudge records one Wake with the operator message.
func (f *controlFixture) verify(verb string, wakesBefore int) bool {
	if verb == "nudge" {
		return len(f.harness.excerpts) == wakesBefore+1 &&
			f.harness.excerpts[len(f.harness.excerpts)-1] == f.message
	}
	store, err := sqlite.Open(f.dbPath)
	if err != nil {
		return false
	}
	defer store.Close()
	run, err := store.Get(context.Background(), "r1")
	if err != nil || run == nil {
		return false
	}
	want := map[string]core.RunState{
		"cancel":  core.RunClosed,
		"unblock": core.RunWaitingCI,
		"retry":   core.RunPending,
	}[verb]
	return run.State == want
}

type controlPass struct {
	work, errs, wires int64
}

// pass drives 100 local control calls, cycling cancel, unblock, retry and
// nudge over one seeded run. Work is the number of applied actions. Errs
// counts dispatch errors and action records that do not match the verb.
func (f *controlFixture) pass(b *testing.B) controlPass {
	b.Helper()
	ctx := context.Background()
	verbs := [...]string{"cancel", "unblock", "retry", "nudge"}
	var res controlPass
	wiresBefore := atomic.LoadInt64(&f.wireCalls)
	for i := 0; i < controlBenchCalls; i++ {
		verb := verbs[i%len(verbs)]
		f.currentAction = verb
		f.message = fmt.Sprintf("guidance-%d", i)
		wakesBefore := len(f.harness.excerpts)

		err := dispatchControlWithPR(ctx, f.fn, f.configPath, verb, "r1", "", 0, f.message)
		if err != nil {
			res.errs++
		} else {
			res.work++
		}
		if !f.verify(verb, wakesBefore) {
			res.errs++
		}
	}
	res.wires = atomic.LoadInt64(&f.wireCalls) - wiresBefore
	return res
}

func printControlReport(w io.Writer, r controlPass, passes int) {
	fmt.Fprintf(w, "%s: calls=%d passes=%d applied=%d wires=%d\n",
		controlBenchName, int64(controlBenchCalls)*int64(passes), passes, r.work, r.wires)
	fmt.Fprintf(w, "%s: work=%d errs=%d limiter=%s\n",
		controlBenchName, r.work, r.errs, controlBenchLimiter)
}

// BenchmarkControlNoDaemon measures the local control path that loads the
// config twice before applying one action. A temp config has no connection
// file, so dispatchControlWithPR wires locally instead of posting to a daemon.
// Every pass applies 100 actions and checks the resulting state or Wake.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkControlNoDaemon$' -benchtime=1x -count=1 -benchmem ./cmd/beacon/
func BenchmarkControlNoDaemon(b *testing.B) {
	f := newControlFixture(b)
	b.ReportAllocs()
	b.ResetTimer()

	var sums controlPass
	durations := make([]time.Duration, 0, b.N)
	for i := 0; i < b.N; i++ {
		start := time.Now()
		r := f.pass(b)
		durations = append(durations, time.Since(start))
		sums.work += r.work
		sums.errs += r.errs
		sums.wires += r.wires
		if r.work != controlBenchCalls {
			b.Fatalf("applied actions = %d, want %d", r.work, controlBenchCalls)
		}
		if r.wires != controlBenchCalls {
			b.Fatalf("wireFn calls = %d, want %d", r.wires, controlBenchCalls)
		}
		if r.errs != 0 {
			b.Fatalf("control errors and mismatched action records = %d, want zero", r.errs)
		}
	}

	b.ReportMetric(float64(sums.work), controlBenchName+"-work")
	b.ReportMetric(float64(sums.errs), controlBenchName+"-errs")
	b.ReportMetric(float64(sums.wires), controlBenchName+"-wire-calls")
	b.ReportMetric(1, controlBenchLimiter)
	printControlReport(os.Stdout, sums, b.N)
	printPerfRow(os.Stdout, "control-no-daemon", durations, sums.work, sums.errs, controlBenchLimiter)
}
