package bench

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

// This benchmark covers the needs-rebase branch update path that the other
// reconcile benchmarks miss. dedupPolicy returns needs-agent-fix and
// BenchmarkE2EOperatorPaths holds runs in wait, so neither reaches an automatic
// UpdateBranch. Here a policy returns VerdictNeedsRebase with one failed check
// and no pending check, which drives a fresh observation through UpdateBranch.
// A second variant reuses the recorded head and base pair and must skip the
// branch update through sameObservation.
const (
	needsRebasePRs     = e2eActivePRs
	needsRebasePrefix  = "needs-rebase"
	needsRebaseLimiter = "scm-updatebranch-roundtrips-plus-store-save-writes"
	needsRebaseCheck   = "go test"

	needsRebaseHead = "reuse-head-000000000000000000000000000000000000"
	needsRebaseBase = "reuse-base-000000000000000000000000000000000000"
)

// rebasePolicy returns needs-rebase for every observation. The failed check in
// the fixture keeps handleNeedsRebase out of greenButBehind.
type rebasePolicy struct{}

func (rebasePolicy) Evaluate(core.Observation) core.MergeDecision {
	return core.MergeDecision{Verdict: core.VerdictNeedsRebase, Reason: "branch is behind base", Criterion: "invariant"}
}

func (rebasePolicy) ReviewRequired(core.ReviewMode, core.Observation) bool { return false }

// rebaseSCM returns one observation per reconcile. Fresh mode advances the head
// SHA on every poll so sameObservation is false and UpdateBranch runs. Reuse
// mode returns the pair already recorded on the run, so rebase work is skipped.
type rebaseSCM struct {
	*e2eSCM
	reuse   bool
	updates atomic.Int64
}

func (s *rebaseSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	head, base := needsRebaseHead, needsRebaseBase
	if !s.reuse {
		n := s.seq.Add(1)
		head = fmt.Sprintf("%040x", n)
		base = fmt.Sprintf("%040x", n/2+1)
	}
	return core.Observation{
		PRNumber: pr,
		HeadSHA:  head,
		BaseSHA:  base,
		PRState:  "open",
		Behind:   true,
		Checks:   []core.CheckResult{{Name: needsRebaseCheck, State: core.CheckFailure}},
	}, nil
}

func (s *rebaseSCM) UpdateBranch(context.Context, int, string) (string, error) {
	s.updates.Add(1)
	return fmt.Sprintf("%040x", s.seq.Add(1)), nil
}

// rebaseStore counts the transitions and writes that bound the path. The save
// right after EvBehind sees state needs-rebase; the save after EvRebased sees
// an incremented attempt count, so each counter moves once per rebase.
type rebaseStore struct {
	*sqlite.Store
	saves   atomic.Int64
	behinds atomic.Int64
	rebased atomic.Int64
}

func (s *rebaseStore) Save(ctx context.Context, run *core.Run) error {
	s.saves.Add(1)
	if run.State == core.RunNeedsRebase {
		s.behinds.Add(1)
	}
	if run.RebaseAttempts > 0 {
		s.rebased.Add(1)
	}
	return s.Store.Save(ctx, run)
}

type rebaseEnv struct {
	ctx   context.Context
	store *rebaseStore
	scm   *rebaseSCM
	recon *app.Reconciler
	prs   []int
}

func newRebaseEnv(b *testing.B, reuse bool) *rebaseEnv {
	b.Helper()
	raw, err := sqlite.Open(filepath.Join(b.TempDir(), "needs-rebase.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = raw.Close() })

	ctx := context.Background()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	e := &rebaseEnv{ctx: ctx, store: &rebaseStore{Store: raw},
		scm: &rebaseSCM{e2eSCM: &e2eSCM{}, reuse: reuse}}
	for i := 0; i < needsRebasePRs; i++ {
		pr := 6000 + i
		run := core.NewRun(core.RunID(fmt.Sprintf("run-rebase-%04d", i)),
			core.TaskID(fmt.Sprintf("T-REBASE-%04d", i)), now)
		run.State = core.RunWaitingCI
		run.PRNumber = pr
		run.HeadSHA = fmt.Sprintf("%040x", i+1)
		run.SessionID = fmt.Sprintf("sess-rebase-%04d", i)
		run.WorkspacePath = fmt.Sprintf("/ws/T-REBASE-%04d", i)
		run.Branch = fmt.Sprintf("beacon/T-REBASE-%04d", i)
		run.Scope = []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/bench/rebase_%02d.go", i))}
		if reuse {
			run.ObservedSHA = needsRebaseHead
			run.ObservedBaseSHA = needsRebaseBase
		}
		if err := raw.Save(ctx, run); err != nil {
			b.Fatal(err)
		}
		e.prs = append(e.prs, pr)
	}
	e.recon = &app.Reconciler{
		Store:      e.store,
		SCM:        e.scm,
		Policy:     rebasePolicy{},
		Harness:    newE2EHarness(),
		Source:     &e2eSource{},
		Workspace:  e2eWorkspace{},
		Decisions:  app.DecisionLogger{Sink: &e2eSink{store: raw}},
		Repo:       "example/beacon",
		BaseBranch: "main",
		Interval:   time.Hour,
		AutoRebase: true,
	}
	return e
}

func (e *rebaseEnv) tick(b *testing.B) (done, errs int64) {
	b.Helper()
	for _, pr := range e.prs {
		if _, err := e.recon.Reconcile(e.ctx, pr); err != nil {
			errs++
			continue
		}
		done++
	}
	return done, errs
}

func printNeedsRebaseReport(w io.Writer, prs, passes, freshTicks, freshUpdates, freshBehinds,
	freshRebases, reuseUpdates, skips, errs int64) {
	work := freshUpdates + freshBehinds + freshRebases + skips
	fmt.Fprintf(w, "%s: prs=%d passes=%d ticks=%d work=%d errs=%d limiter=%s\n",
		needsRebasePrefix, prs, passes, freshTicks, work, errs, needsRebaseLimiter)
	fmt.Fprintf(w, "fresh head: updates=%d evbehind=%d rebased=%d sha-guard-skips=0\n",
		freshUpdates, freshBehinds, freshRebases)
	fmt.Fprintf(w, "reused observation: updates=%d evbehind=0 rebased=0 sha-guard-skips=%d\n",
		reuseUpdates, skips)
}

// BenchmarkNeedsRebaseBranchUpdate measures one reconcile tick over PRs whose
// policy verdict is needs-rebase. It runs both the fresh-observation path and
// the reused-observation path in one invocation so CI gets work, errs and the
// named limiter from a single -benchtime=1x command. Smoke mode skips the
// warm-up ticks; fixtures, counts and assertions stay the same.
func BenchmarkNeedsRebaseBranchUpdate(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	if !smokeMode() {
		warm := newRebaseEnv(b, false)
		_, _ = warm.tick(b)
		warm = newRebaseEnv(b, true)
		_, _ = warm.tick(b)
	}

	var done, errs int64
	var freshUpdates, freshBehinds, freshRebases, reuseUpdates int64
	var skips, saves int64
	var ds []time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		fresh := newRebaseEnv(b, false)
		reuse := newRebaseEnv(b, true)
		b.StartTimer()
		start := time.Now()
		freshDone, freshErrs := fresh.tick(b)
		reuseDone, reuseErrs := reuse.tick(b)
		b.StopTimer()
		ds = append(ds, time.Since(start))

		done += freshDone + reuseDone
		errs += freshErrs + reuseErrs
		freshUpdates += fresh.scm.updates.Load()
		freshBehinds += fresh.store.behinds.Load()
		freshRebases += fresh.store.rebased.Load()
		reuseUpdates += reuse.scm.updates.Load()
		skips += int64(needsRebasePRs) - reuse.scm.updates.Load()
		saves += fresh.store.saves.Load() + reuse.store.saves.Load()
	}

	freshTicks := int64(needsRebasePRs) * int64(b.N)
	if errs != 0 || done != 2*freshTicks {
		b.Fatalf("errs=%d done=%d want errs=0 done=%d", errs, done, 2*freshTicks)
	}
	if freshUpdates < freshTicks {
		b.Fatalf("updates=%d fewer than ticks=%d", freshUpdates, freshTicks)
	}
	if freshBehinds != freshTicks || freshRebases != freshTicks {
		b.Fatalf("evbehind=%d rebased=%d want %d of each", freshBehinds, freshRebases, freshTicks)
	}
	if reuseUpdates != 0 {
		b.Fatalf("reused observation updated the branch %d times, want zero", reuseUpdates)
	}
	if skips != freshTicks {
		b.Fatalf("sha-guard skips=%d want %d", skips, freshTicks)
	}

	work := freshUpdates + freshBehinds + freshRebases + skips
	b.ReportMetric(float64(work), needsRebasePrefix+"-work")
	b.ReportMetric(float64(errs), needsRebasePrefix+"-errs")
	b.ReportMetric(float64(freshUpdates)/float64(b.N), "updates-per-tick")
	b.ReportMetric(float64(freshBehinds)/float64(b.N), "evbehind-per-tick")
	b.ReportMetric(float64(freshRebases)/float64(b.N), "rebased-per-tick")
	b.ReportMetric(float64(skips)/float64(b.N), "sha-guard-skips-per-tick")
	b.ReportMetric(float64(saves)/float64(b.N), "saves-per-tick")
	b.ReportMetric(float64(medianDuration(ds).Nanoseconds()), needsRebasePrefix+"-median-ns")
	b.ReportMetric(float64(freshUpdates), needsRebasePrefix+"-limiter-scm-updatebranch")
	b.ReportMetric(float64(saves), needsRebasePrefix+"-limiter-store-save")
	printNeedsRebaseReport(os.Stdout, needsRebasePRs, int64(b.N), freshTicks,
		freshUpdates, freshBehinds, freshRebases, reuseUpdates, skips, errs)
}
