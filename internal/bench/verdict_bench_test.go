package bench

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// The verdict benchmark covers the two paths the end-to-end harness misses.
// e2e_bench_test.go uses dedupPolicy, which returns a verdict directly and
// never calls Decide or ActionForVerdict. Here every observation goes through
// the invariant chain in Decide and every verdict goes through the
// decisionActions table, so both paths are measured.
//
// Work is the number of observations classified plus the number of criteria
// evaluated. Errs counts verdicts and action rows that differ from a fixed
// table. Both counts are fixed by the fixture and match in smoke and full mode.
// The limiter metrics count the operations the dossier names as the bound on
// each path: two strings.ToLower calls per DecideFinalState and one map lookup
// per action lookup.

const (
	nameVerdict        = "verdict"
	nameDecisionAction = "decision-action"

	limiterVerdict        = "verdict-limiter-lowercase"
	limiterDecisionAction = "decision-action-limiter-map"

	verdictCriteria = 2
)

// countingCriterion mirrors fakeCriterion in pkg/core/verdict_test.go and adds
// a call counter. Decide stops at the first non-noop verdict, so the counter
// also proves how far the invariant chain let each observation run.
type countingCriterion struct {
	name    string
	verdict core.Verdict
	reason  string
	calls   *int64
}

func (c countingCriterion) Name() string { return c.name }

func (c countingCriterion) Evaluate(core.Observation) (core.Verdict, string) {
	*c.calls = *c.calls + 1
	return c.verdict, c.reason
}

type verdictFixture struct {
	observations []core.Observation
	want         []core.Verdict
	criteria     []core.Criterion
	calls        *int64
}

// verdictObservations is a fixed table that reaches every invariant branch in
// Decide and the final merge branch. An empty or unknown PR state is not final,
// so those rows reach the criteria like an open state does.
func verdictObservations() []struct {
	obs  core.Observation
	want core.Verdict
} {
	return []struct {
		obs  core.Observation
		want core.Verdict
	}{
		{core.Observation{PRState: "closed"}, core.VerdictNoop},
		{core.Observation{PRState: "merged"}, core.VerdictNoop},
		{core.Observation{PRState: "locked"}, core.VerdictNoop},
		{core.Observation{PRState: "CLOSED"}, core.VerdictNoop},
		{core.Observation{PRState: "Merged"}, core.VerdictNoop},
		{core.Observation{PRState: "LOCKED"}, core.VerdictNoop},
		{core.Observation{PRState: "open"}, core.VerdictMerge},
		{core.Observation{PRState: "opened"}, core.VerdictMerge},
		{core.Observation{PRState: "unknown"}, core.VerdictMerge},
		{core.Observation{PRState: ""}, core.VerdictMerge},
		{core.Observation{PRState: "open", Incomplete: true}, core.VerdictWait},
		{core.Observation{PRState: "open", Incomplete: true, IncompleteReason: "jobs down"}, core.VerdictWait},
		{core.Observation{PRState: "open", Conflict: true}, core.VerdictNeedsAgentFix},
		{core.Observation{PRState: "open", UnresolvedThreads: []string{"please address"}}, core.VerdictNeedsAgentFix},
		{core.Observation{PRState: "open", Checks: []core.CheckResult{{Name: "ci", State: core.CheckFailure}}}, core.VerdictNeedsAgentFix},
		{core.Observation{PRState: "open", Behind: true}, core.VerdictNeedsRebase},
	}
}

// verdictFixtures repeats the fixed table to a seeded size and adds variation
// that cannot change a verdict, so the workload stays deterministic and larger
// than a single pass over the table.
func verdictFixtures() verdictFixture {
	r := seeded()
	cases := verdictObservations()
	observations := make([]core.Observation, 0, fixtureDecisions)
	want := make([]core.Verdict, 0, fixtureDecisions)
	for i := 0; i < fixtureDecisions; i++ {
		tc := cases[i%len(cases)]
		obs := tc.obs
		obs.PRNumber = 2000 + i
		obs.HeadSHA = hexString(r, 40)
		obs.BaseSHA = hexString(r, 40)
		observations = append(observations, obs)
		want = append(want, tc.want)
	}
	calls := new(int64)
	criteria := make([]core.Criterion, 0, verdictCriteria)
	for i := 0; i < verdictCriteria; i++ {
		criteria = append(criteria, countingCriterion{
			name:    fmt.Sprintf("gate-%d", i),
			verdict: core.VerdictNoop,
			calls:   calls,
		})
	}
	return verdictFixture{observations: observations, want: want, criteria: criteria, calls: calls}
}

// runDecideInvariants calls Decide once per observation and checks every
// verdict against the fixed table. It returns work, errs and the number of
// strings.ToLower calls made by DecideFinalState.
func runDecideInvariants(f verdictFixture) (int64, int64, int64) {
	*f.calls = 0
	var errs int64
	for i, obs := range f.observations {
		if got := core.Decide(obs, f.criteria).Verdict; got != f.want[i] {
			errs++
		}
	}
	work := int64(len(f.observations)) + *f.calls
	lowercases := 2 * int64(len(f.observations))
	return work, errs, lowercases
}

type actionCase struct {
	key  string
	want core.DecisionAction
}

// verdictActions covers every verdict row plus the unknown-verdict default in
// ActionForVerdict. Kind rows cover every counter in ActionForKind plus an
// unknown kind, which returns the zero row.
func verdictActions() []actionCase {
	return []actionCase{
		{string(core.VerdictMerge), core.DecisionAction{Label: "merge", Dispatch: core.DispatchMerge}},
		{string(core.VerdictNeedsRebase), core.DecisionAction{Label: "rebase", Dispatch: core.DispatchRebase}},
		{string(core.VerdictNeedsAgentFix), core.DecisionAction{Label: "wake", Dispatch: core.DispatchFix}},
		{string(core.VerdictBlocked), core.DecisionAction{Label: "block", Dispatch: core.DispatchBlock}},
		{string(core.VerdictNoop), core.DecisionAction{Label: "noop", Dispatch: core.DispatchNoop}},
		{string(core.VerdictWait), core.DecisionAction{Label: "wait", Dispatch: core.DispatchPending}},
		{"unknown", core.DecisionAction{Label: "wait", Dispatch: core.DispatchPending}},
	}
}

func kindActions() []actionCase {
	return []actionCase{
		{"dispatch.candidates", core.DecisionAction{Counter: core.CounterDispatchCycles}},
		{"dispatch.skip", core.DecisionAction{Counter: core.CounterSkip}},
		{"transition.spawn", core.DecisionAction{Counter: core.CounterDispatch}},
		{"transition.rebase", core.DecisionAction{Counter: core.CounterRebase, Qualifier: "rebased"}},
		{"transition.wake", core.DecisionAction{Counter: core.CounterFix, Qualifier: "woke"}},
		{"unknown.kind", core.DecisionAction{}},
	}
}

// runDecisionActions resolves every verdict and kind row against the fixed
// table. It returns work, errs and the number of decisionActions map lookups.
func runDecisionActions(verdicts, kinds []actionCase) (int64, int64, int64) {
	var errs int64
	for _, tc := range verdicts {
		if got := core.ActionForVerdict(core.Verdict(tc.key)); got != tc.want {
			errs++
		}
	}
	for _, tc := range kinds {
		if got := core.ActionForKind(tc.key); got != tc.want {
			errs++
		}
	}
	work := int64(len(verdicts) + len(kinds))
	return work, errs, work
}

func printVerdictReport(w io.Writer, name string, d time.Duration, work, errs int64, limiter string) {
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d limiter=%s\n", name, d, work, errs, limiter)
}

// TestDecideInvariantsWorkStable pins the work and error counts the CI parsing
// compares between the base and head sides. Two passes over the same seeded
// fixture must report identical counts, positive work and no errors.
func TestDecideInvariantsWorkStable(t *testing.T) {
	f := verdictFixtures()
	work, errs, lowercases := runDecideInvariants(f)
	againWork, againErrs, againLowercases := runDecideInvariants(f)
	if work != againWork || errs != againErrs || lowercases != againLowercases {
		t.Fatalf("decide work is not stable: first=%d/%d/%d second=%d/%d/%d",
			work, errs, lowercases, againWork, againErrs, againLowercases)
	}
	if work <= 0 || errs != 0 || lowercases != 2*int64(len(f.observations)) {
		t.Fatalf("decide work=%d errs=%d lowercases=%d, want positive work, no errors and two lowercases per observation",
			work, errs, lowercases)
	}
}

// TestDecisionActionsWorkStable pins the action table counts the same way.
func TestDecisionActionsWorkStable(t *testing.T) {
	verdicts, kinds := verdictActions(), kindActions()
	work, errs, lookups := runDecisionActions(verdicts, kinds)
	againWork, againErrs, againLookups := runDecisionActions(verdicts, kinds)
	if work != againWork || errs != againErrs || lookups != againLookups {
		t.Fatalf("action work is not stable: first=%d/%d/%d second=%d/%d/%d",
			work, errs, lookups, againWork, againErrs, againLookups)
	}
	if work != int64(len(verdicts)+len(kinds)) || errs != 0 || lookups != work {
		t.Fatalf("action work=%d errs=%d lookups=%d, want one lookup per row and no errors",
			work, errs, lookups)
	}
}

// BenchmarkDecideInvariants times the invariant chain and criteria in Decide.
// The fixture is built once outside the timed loop and the reported counts are
// fixed, so a smoke pass and a full pass print the same work and errs.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkDecideInvariants$' -benchtime=1x -count=1 ./internal/bench/
func BenchmarkDecideInvariants(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	f := verdictFixtures()
	if !smokeMode() {
		_, _, _ = runDecideInvariants(f)
	}

	durations := make([]time.Duration, 0, b.N)
	var work, errs, lowercases int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		work, errs, lowercases = runDecideInvariants(f)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	median := medianDuration(durations)
	printVerdictReport(os.Stdout, nameVerdict, median, work, errs, limiterVerdict)
	b.ReportMetric(float64(median.Nanoseconds()), nameVerdict+"-median-ns")
	b.ReportMetric(float64(work), nameVerdict+"-work")
	b.ReportMetric(float64(errs), nameVerdict+"-errs")
	b.ReportMetric(float64(lowercases), limiterVerdict)
}

// BenchmarkDecisionActions times the decisionActions map lookups in
// ActionForVerdict and ActionForKind. The fixture and the reported counts are
// fixed, so smoke and full passes agree.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkDecisionActions$' -benchtime=1x -count=1 ./internal/bench/
func BenchmarkDecisionActions(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	verdicts, kinds := verdictActions(), kindActions()
	if !smokeMode() {
		_, _, _ = runDecisionActions(verdicts, kinds)
	}

	durations := make([]time.Duration, 0, b.N)
	var work, errs, lookups int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		work, errs, lookups = runDecisionActions(verdicts, kinds)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	median := medianDuration(durations)
	printVerdictReport(os.Stdout, nameDecisionAction, median, work, errs, limiterDecisionAction)
	b.ReportMetric(float64(median.Nanoseconds()), nameDecisionAction+"-median-ns")
	b.ReportMetric(float64(work), nameDecisionAction+"-work")
	b.ReportMetric(float64(errs), nameDecisionAction+"-errs")
	b.ReportMetric(float64(lookups), limiterDecisionAction)
}
