package bench

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

// The frontmatter benchmark measures the validation and alias normalization a
// markdown task source performs when it lists a mixed card directory. Every
// pass lists the whole directory through ListAvailable. Work is accepted cards
// plus expected rejects plus resolved alias fields. Errs is any card whose
// accept or reject result differs from its fixture label, plus any alias field
// that fails to normalize.
const (
	nameCardFrontmatter      = "card-frontmatter-validation"
	cardFrontmatterCanonical = 100
	cardFrontmatterAlias     = 100
	cardFrontmatterMalformed = 100
	cardFrontmatterLimiter   = "yaml.Unmarshal + line split"
)

type cardFrontmatterFixture struct {
	ctx      context.Context
	store    *tasksource.Markdown
	accepted map[string]bool
	rejected map[string]bool
	alias    map[string]core.TaskID
}

// newCardFrontmatterFixture writes a bounded mixed corpus once outside the
// timed loop. Canonical cards use native field names. Alias-heavy cards use
// task_id, owner_files, depends_on and a Russian status that normalizes to
// available. Malformed cards use an unterminated header, invalid YAML and an
// unknown kind with a YAML type that cannot decode into a string.
func newCardFrontmatterFixture(tb testing.TB) *cardFrontmatterFixture {
	tb.Helper()
	dir := filepath.Join(tb.TempDir(), "docs", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	f := &cardFrontmatterFixture{
		ctx:      context.Background(),
		store:    tasksource.NewMarkdown(dir),
		accepted: make(map[string]bool, cardFrontmatterCanonical+cardFrontmatterAlias),
		rejected: make(map[string]bool, cardFrontmatterMalformed),
		alias:    make(map[string]core.TaskID, cardFrontmatterAlias),
	}
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
		return path
	}
	for i := 0; i < cardFrontmatterCanonical; i++ {
		id := fmt.Sprintf("T-C-%03d", i)
		content := fmt.Sprintf("---\nid: %s\ntitle: canonical %d\nstatus: available\npriority: %d\nkind: task\nscope:\n  - pkg/core/run.go\n---\n\n# %s. canonical\n", id, i, i%10, id)
		f.accepted[write(id+".md", content)] = true
	}
	for i := 0; i < cardFrontmatterAlias; i++ {
		id := fmt.Sprintf("T-A-%03d", i)
		content := fmt.Sprintf("---\ntask_id: %s\ntitle: alias %d\nstatus: доступно\nowner_files:\n  - pkg/core/run.go\ndepends_on:\n  - T-C-000\n---\n\n# %s. alias\n", id, i, id)
		path := write(id+".md", content)
		f.accepted[path] = true
		f.alias[path] = core.TaskID(id)
	}
	for i := 0; i < cardFrontmatterMalformed; i++ {
		var name, content string
		switch i % 3 {
		case 0:
			name = fmt.Sprintf("T-M-U-%03d.md", i)
			content = fmt.Sprintf("---\nid: T-M-U-%03d\ntitle: unterminated\nstatus: available\n", i)
		case 1:
			name = fmt.Sprintf("T-M-B-%03d.md", i)
			content = fmt.Sprintf("---\nid: T-M-B-%03d\ntitle: [\nstatus: available\n---\n", i)
		default:
			name = fmt.Sprintf("T-M-K-%03d.md", i)
			content = fmt.Sprintf("---\nid: T-M-K-%03d\ntitle: unknown kind\nstatus: available\nkind: [bogus]\n---\n", i)
		}
		f.rejected[write(name, content)] = true
	}
	return f
}

type cardFrontmatterPass struct {
	work     int64
	errs     int64
	aliases  int64
	accepted int64
	rejected int64
}

// runCardFrontmatterPass lists the directory once and checks every card
// against its fixture label. A returned accepted card adds one work; an absent
// rejected card adds one work. Each alias field that resolves to the value the
// fixture wrote adds one work and one alias count. A wrong accept, a wrong
// reject or a failed alias resolution adds one err.
func runCardFrontmatterPass(f *cardFrontmatterFixture) cardFrontmatterPass {
	var p cardFrontmatterPass
	cards, err := f.store.ListAvailable(f.ctx)
	if err != nil {
		p.errs++
		return p
	}
	seen := make(map[string]bool, len(cards))
	for _, task := range cards {
		path := task.SourceRef
		seen[path] = true
		switch {
		case f.accepted[path]:
			p.accepted++
			p.work++
			if wantID, ok := f.alias[path]; ok {
				resolved := 0
				if task.ID == wantID {
					resolved++
				} else {
					p.errs++
				}
				if task.Status == core.TaskAvailable {
					resolved++
				} else {
					p.errs++
				}
				if slices.Contains(task.Scope, core.ScopeFile("pkg/core/run.go")) {
					resolved++
				} else {
					p.errs++
				}
				if slices.Contains(task.DependsOn, core.TaskID("T-C-000")) {
					resolved++
				} else {
					p.errs++
				}
				p.aliases += int64(resolved)
				p.work += int64(resolved)
			}
		case f.rejected[path]:
			p.errs++
		default:
			p.errs++
		}
	}
	for path := range f.accepted {
		if !seen[path] {
			p.errs++
		}
	}
	for path := range f.rejected {
		if !seen[path] {
			p.rejected++
			p.work++
		}
	}
	return p
}

// printCardFrontmatterReport writes one summary line to stdout so a benchmark
// run emits work, errs and the named limiter without -v.
func printCardFrontmatterReport(w io.Writer, p cardFrontmatterPass, passes int) {
	fmt.Fprintf(w, "%s: cards=%d accepted=%d rejected=%d alias-fields=%d passes=%d work=%d errs=%d limiter=%s\n",
		nameCardFrontmatter,
		cardFrontmatterCanonical+cardFrontmatterAlias+cardFrontmatterMalformed,
		p.accepted, p.rejected, p.aliases, passes, p.work, p.errs, cardFrontmatterLimiter)
}

// TestCardFrontmatterValidationWorkStable pins the counts the benchmark
// reports. Two passes over the same fixture produce identical work, error and
// alias counts, so a parser change that drops a card or an alias cannot pass
// unnoticed.
func TestCardFrontmatterValidationWorkStable(t *testing.T) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)

	f := newCardFrontmatterFixture(t)
	first := runCardFrontmatterPass(f)
	second := runCardFrontmatterPass(f)
	if first != second {
		t.Fatalf("frontmatter counts are not stable: first=%+v second=%+v", first, second)
	}
	if first.errs != 0 {
		t.Fatalf("frontmatter errs=%d, want zero: %+v", first.errs, first)
	}
	if first.work <= 0 {
		t.Fatalf("frontmatter work=%d, want positive", first.work)
	}
	if want := int64(cardFrontmatterCanonical + cardFrontmatterAlias); first.accepted != want {
		t.Fatalf("accepted=%d, want %d", first.accepted, want)
	}
	if want := int64(cardFrontmatterMalformed); first.rejected != want {
		t.Fatalf("rejected=%d, want %d", first.rejected, want)
	}
	if want := int64(4 * cardFrontmatterAlias); first.aliases != want {
		t.Fatalf("alias-fields=%d, want %d", first.aliases, want)
	}
}

// BenchmarkCardFrontmatterValidation times one ListAvailable call per pass over
// the mixed corpus. The first pass builds the card index and parses every
// card; that is the frontmatter validation the row measures under CI. Smoke
// mode runs the same fixture and reports the same counts as full mode.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkCardFrontmatterValidation$' -benchtime=1x -count=1 -benchmem ./internal/bench/
func BenchmarkCardFrontmatterValidation(b *testing.B) {
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLog)
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	f := newCardFrontmatterFixture(b)
	b.ReportAllocs()
	ds := make([]time.Duration, 0, b.N)
	var total cardFrontmatterPass
	for i := 0; i < b.N; i++ {
		start := time.Now()
		p := runCardFrontmatterPass(f)
		ds = append(ds, time.Since(start))
		total.work += p.work
		total.errs += p.errs
		total.aliases += p.aliases
		total.accepted += p.accepted
		total.rejected += p.rejected
	}
	if b.N == 0 {
		b.Fatal("benchmark ran no passes")
	}
	passes := int64(b.N)
	avg := cardFrontmatterPass{
		work:     total.work / passes,
		errs:     total.errs / passes,
		aliases:  total.aliases / passes,
		accepted: total.accepted / passes,
		rejected: total.rejected / passes,
	}
	if avg.errs != 0 {
		b.Fatalf("frontmatter errs=%d, want zero: %+v", avg.errs, avg)
	}
	if avg.work <= 0 {
		b.Fatalf("frontmatter work=%d, want positive", avg.work)
	}

	median := medianDuration(ds)
	printCardFrontmatterReport(os.Stdout, avg, int(passes))
	b.ReportMetric(float64(median.Nanoseconds()), nameCardFrontmatter+"-median-ns")
	b.ReportMetric(float64(avg.work), nameCardFrontmatter+"-work")
	b.ReportMetric(float64(avg.errs), nameCardFrontmatter+"-errs")
	b.ReportMetric(float64(avg.aliases), nameCardFrontmatter+"-alias-fields")
}
