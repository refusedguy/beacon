package bench

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

// This benchmark covers the epic-plan path that the card parse and card store
// benchmarks miss. An epic card carries a plan array, and reading its children
// expands that plan into numbered tasks with dependency edges. Issue bodies
// take the other route: ParseTaskFields normalizes bare numeric depends with a
// non-empty depPrefix. Both routes pay the plan YAML decode and the child
// allocation that the named limiter describes.
//
// Work is children returned plus dependency edges resolved. Errs counts failed
// Children calls, dependencies that kept a bare id, and a pass whose child or
// edge count fell below the 2,000 the fixture promises. Counts come from the
// fixture, so base and head report the same numbers.
//
// CI command:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkCardDependencyGraph$' -benchtime=1x -count=1 -benchmem ./internal/bench/
const (
	nameCardDependency = "card-dependency-graph"

	cardDepLimiter = "plan yaml.Unmarshal + ExpandChildren allocation"

	cardDepEpics    = 100
	cardDepChildren = 20
	cardDepIssues   = 100
	cardDepMinCount = cardDepEpics * cardDepChildren
	cardDepPrefix   = "T-DEP"
)

type cardDependencyEnv struct {
	ctx    context.Context
	dir    string
	store  *tasksource.Markdown
	epics  []core.TaskID
	issues []string
}

func newCardDependencyEnv(tb testing.TB) *cardDependencyEnv {
	tb.Helper()
	dir := tb.TempDir()
	e := &cardDependencyEnv{
		ctx:   context.Background(),
		dir:   dir,
		store: tasksource.NewMarkdown(dir),
	}
	for i := 0; i < cardDepEpics; i++ {
		id := core.TaskID(fmt.Sprintf("T-DEP-EPIC-%03d", i))
		plan := make([]core.ChildSpec, 0, cardDepChildren)
		for j := 0; j < cardDepChildren; j++ {
			plan = append(plan, core.ChildSpec{
				Title:    fmt.Sprintf("Child %02d of epic %03d", j, i),
				Body:     "Split off one dependency graph node.",
				Scope:    []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/bench/dep/%03d/%02d.go", i, j))},
				Depends:  []core.TaskID{core.TaskID(fmt.Sprintf("T-DEP-BASE-%03d-%02d", i, j))},
				Priority: j + 1,
			})
		}
		epic := core.Task{
			ID:            id,
			Title:         fmt.Sprintf("Epic %03d", i),
			Body:          "Epic fixture with a fixed plan.",
			Kind:          core.TaskKindEpic,
			BaseBranch:    "main",
			RequireReview: core.ReviewModeLLM,
			Scope:         []core.ScopePath{core.ScopeDir(fmt.Sprintf("internal/bench/dep/%03d/", i))},
			Plan:          plan,
		}
		created, err := e.store.Create(e.ctx, epic)
		if err != nil {
			tb.Fatalf("store.Create(%s): %v", id, err)
		}
		if created.ID != id {
			tb.Fatalf("created.ID = %s, want %s", created.ID, id)
		}
		e.epics = append(e.epics, id)
	}
	e.issues = cardDependencyIssueBodies()
	return e
}

// cardDependencyIssueBodies builds distinct issue bodies so every parse pays a
// fresh YAML decode instead of reusing the shared field cache. Bare numeric
// depends exercise normalizeDep, which prepends cardDepPrefix.
func cardDependencyIssueBodies() []string {
	out := make([]string, 0, cardDepIssues)
	for i := 0; i < cardDepIssues; i++ {
		out = append(out, fmt.Sprintf("Issue %03d body text.\n\n```yaml\nscope:\n  - internal/bench/dep/%03d/issue.go\ndepends:\n  - \"%d\"\n  - \"%d\"\npriority: %d\n```\n",
			i, i, i+1, i+101, 50+i%50))
	}
	return out
}

type cardDependencyPass struct {
	children int64
	edges    int64
	parses   int64
	work     int64
	errs     int64
}

func (e *cardDependencyEnv) pass() cardDependencyPass {
	var p cardDependencyPass
	for _, id := range e.epics {
		children, err := e.store.Children(e.ctx, id)
		if err != nil {
			p.errs++
			continue
		}
		p.children += int64(len(children))
		for _, child := range children {
			p.edges += int64(len(child.DependsOn))
		}
	}
	for _, body := range e.issues {
		fields := tasksource.ParseTaskFields(body, e.dir, cardDepPrefix)
		deps := fields.Dependencies()
		p.parses++
		p.edges += int64(len(deps))
		for _, dep := range deps {
			if !strings.HasPrefix(string(dep), cardDepPrefix+"-") {
				p.errs++
			}
		}
	}
	if p.children < cardDepMinCount || p.edges < cardDepMinCount {
		p.errs++
	}
	p.work = p.children + p.edges
	return p
}

// TestCardDependencyGraphWorkStable pins the counts the CI parsing compares
// between the base and head sides. Two passes over the same fixture must give
// the same work and no errors.
func TestCardDependencyGraphWorkStable(t *testing.T) {
	e := newCardDependencyEnv(t)
	first := e.pass()
	second := e.pass()
	if first != second {
		t.Fatalf("card dependency work is not stable: first=%+v second=%+v", first, second)
	}
	if first.work <= 0 || first.errs != 0 {
		t.Fatalf("work=%d errs=%d, want positive work and no errors", first.work, first.errs)
	}
}

// BenchmarkCardDependencyGraph times the epic plan expansion and issue-body
// normalization in one pass. Smoke mode skips the warm pass; the fixture,
// work and errs stay the same. Work and the named limiter are reported on
// stdout and as metrics so CI can read them.
func BenchmarkCardDependencyGraph(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	e := newCardDependencyEnv(b)
	if !smokeMode() {
		_ = e.pass()
	}

	durations := make([]time.Duration, 0, b.N)
	var p cardDependencyPass
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		p = e.pass()
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	if p.work <= 0 || p.errs != 0 {
		b.Fatalf("card dependency graph work=%d errs=%d, want positive work and no errors", p.work, p.errs)
	}
	median := medianDuration(durations)
	fmt.Fprintf(os.Stdout, "%s: median=%s work=%d errs=%d children=%d edges=%d limiter=%s\n",
		nameCardDependency, median, p.work, p.errs, p.children, p.edges, cardDepLimiter)
	b.ReportMetric(float64(median.Nanoseconds()), nameCardDependency+"-median-ns")
	b.ReportMetric(float64(p.work), nameCardDependency+"-work")
	b.ReportMetric(float64(p.errs), nameCardDependency+"-errs")
}
