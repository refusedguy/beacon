package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/pkg/core"
)

const (
	statusCLILimiter = "json-reflection-plus-sqlite-read"
	statusCLIRuns    = 8

	// cliPerfPrefix marks one canonical advisory row in a benchmark log.
	cliPerfPrefix = "beacon-perf: "
)

// cliPerfRow is the fixed payload of a canonical row. The advisory comment
// parser rejects a row with a different field set, so every CLI harness prints
// exactly these eight fields.
type cliPerfRow struct {
	Key      string  `json:"key"`
	MedianMS float64 `json:"median_ms"`
	MinMS    float64 `json:"min_ms"`
	MaxMS    float64 `json:"max_ms"`
	Work     int64   `json:"work"`
	Errs     int64   `json:"errs"`
	Limiter  string  `json:"limiter"`
	Cache    string  `json:"cache"`
}

// cliBenchCache mirrors the advisory cache stamp. CI exports
// BEACON_BENCH_CACHE=warm for every measured pass, so two rows from different
// cache policies can never be read as the same sample.
func cliBenchCache() string {
	if value := os.Getenv("BEACON_BENCH_CACHE"); value != "" {
		return value
	}
	return "unset"
}

// printPerfRow writes one canonical row for a whole benchmark run. Durations
// are the per-iteration timings the harness already collects, so the row and
// the human-readable report describe the same measurement. This helper belongs
// to package main; the bench package has its own of the same name.
func printPerfRow(w io.Writer, key string, durations []time.Duration, work, errs int64, limiter string) {
	if len(durations) == 0 {
		return
	}
	min, max := durations[0], durations[0]
	for _, d := range durations[1:] {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	median := statusCLIMedian(durations)
	data, err := json.Marshal(cliPerfRow{
		Key:      key,
		MedianMS: float64(median) / float64(time.Millisecond),
		MinMS:    float64(min) / float64(time.Millisecond),
		MaxMS:    float64(max) / float64(time.Millisecond),
		Work:     work,
		Errs:     errs,
		Limiter:  limiter,
		Cache:    cliBenchCache(),
	})
	if err != nil {
		return
	}
	fmt.Fprintln(w, cliPerfPrefix+string(data))
}

// TestCLICanonicalRow pins the eight-field payload every CLI benchmark prints.
// The advisory parser rejects a row with a different field set, so a shape
// change fails here instead of silently dropping the row in CI.
func TestCLICanonicalRow(t *testing.T) {
	t.Setenv("BEACON_BENCH_CACHE", "warm")
	durations := []time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond}
	var out bytes.Buffer
	printPerfRow(&out, "status-cli-paths", durations, 7, 0, statusCLILimiter)

	line := strings.TrimSuffix(out.String(), "\n")
	if !strings.HasPrefix(line, cliPerfPrefix) {
		t.Fatalf("row has no canonical prefix: %q", line)
	}
	data := strings.TrimPrefix(line, cliPerfPrefix)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	want := []string{"key", "median_ms", "min_ms", "max_ms", "work", "errs", "limiter", "cache"}
	if len(fields) != len(want) {
		t.Fatalf("row has %d fields, want %d: %s", len(fields), len(want), data)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("row is missing %q: %s", name, data)
		}
	}
	var row cliPerfRow
	if err := json.Unmarshal([]byte(data), &row); err != nil {
		t.Fatalf("row does not decode: %v", err)
	}
	if row.Key != "status-cli-paths" || row.MedianMS != 2 || row.MinMS != 1 || row.MaxMS != 3 ||
		row.Work != 7 || row.Errs != 0 || row.Cache != "warm" || row.Limiter != statusCLILimiter {
		t.Fatalf("row = %+v, want status-cli-paths 2/1/3 7/0 warm with the CLI limiter", row)
	}
}

// statusCLICountWriter collects the bytes printJSON writes to stdout. The
// status command prints through the package-level os.Stdout, so
// runStatusCLIPath redirects that handle to a pipe drained by this writer.
type statusCLICountWriter struct{ buf bytes.Buffer }

func (w *statusCLICountWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

// generatedAtField is the one volatile value in the snapshot path. time.Now
// formats it as RFC3339Nano, which trims trailing zeros, so its width changes
// between passes. Normalizing it keeps the reported byte count equal across
// passes without changing what the command prints or measures.
var generatedAtField = regexp.MustCompile(`"generated_at": "[^"]+"`)

func statusCLIWork(out []byte) int64 {
	return int64(len(generatedAtField.ReplaceAll(out, []byte(`"generated_at": "fixed"`))))
}

// statusCLIFixture owns a temp project root, config and state database. The
// benchmark never reads the operator's checkout or its state files.
type statusCLIFixture struct {
	root       string
	configPath string
}

func newStatusCLIFixture(b *testing.B) *statusCLIFixture {
	b.Helper()
	root := b.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		b.Fatalf("git init: %v: %s", err, out)
	}
	stateDir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		b.Fatalf("mkdir state dir: %v", err)
	}
	dbPath := filepath.Join(stateDir, "state.db")
	configPath := filepath.Join(stateDir, "config.yaml")
	config := fmt.Sprintf("state_db: %s\nworkspace_dir: %s\nbase_branch: main\n",
		dbPath, filepath.Join(stateDir, "worktrees"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		b.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	store, err := sqlite.Open(dbPath)
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < statusCLIRuns; i++ {
		state := core.RunRunning
		if i%2 == 1 {
			state = core.RunMerged
		}
		run := core.NewRun(core.RunID(fmt.Sprintf("run-status-cli-%02d", i)),
			core.TaskID(fmt.Sprintf("T-CLI-%02d", i)), now)
		run.State = state
		run.PRNumber = 7000 + i
		run.HeadSHA = fmt.Sprintf("%040x", i+1)
		run.Branch = fmt.Sprintf("beacon/T-CLI-%02d", i)
		run.Reason = "queued for review"
		run.WorkspacePath = filepath.Join(stateDir, "worktrees", string(run.TaskID))
		run.UpdatedAt = now.Add(time.Duration(i) * time.Minute)
		if err := store.Save(ctx, run); err != nil {
			b.Fatalf("save run: %v", err)
		}
		if err := store.RecordDecision(ctx, app.Decision{
			CycleID:  fmt.Sprintf("cycle-%02d", i),
			Kind:     "verdict",
			RunID:    string(run.ID),
			TaskID:   string(run.TaskID),
			PRNumber: run.PRNumber,
			Verdict:  "approve",
			Action:   "dispatch",
			Reason:   "Checks passed",
			Fields:   map[string]any{"sha": run.HeadSHA},
			At:       run.UpdatedAt,
		}); err != nil {
			b.Fatalf("record decision: %v", err)
		}
	}
	return &statusCLIFixture{root: root, configPath: configPath}
}

// runStatusCLIPath drives one status read path through the real Cobra command
// and returns the JSON bytes it prints plus any command error. The caller
// turns those bytes into a stable work count for CI.
func runStatusCLIPath(b *testing.B, configPath string, args []string) ([]byte, error) {
	b.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		b.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	count := &statusCLICountWriter{}
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(count, r)
		copied <- err
	}()
	cmd := newStatusCommand(&configPath)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	runErr := cmd.Execute()
	os.Stdout = old
	_ = w.Close()
	copyErr := <-copied
	_ = r.Close()
	if copyErr != nil {
		b.Fatalf("copy stdout: %v", copyErr)
	}
	return count.buf.Bytes(), runErr
}

func statusCLIMedian(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func printStatusCLIReport(w io.Writer, name string, d time.Duration, work, errs int64) {
	fmt.Fprintf(w, "%s: median=%s work=%d errs=%d limiter=%s\n",
		name, d, work, errs, statusCLILimiter)
}

// statusCLIPass accumulates the three read paths of one benchmark invocation
// into the single canonical row BenchmarkStatusCLIPaths publishes.
type statusCLIPass struct {
	durations []time.Duration
	work      int64
	errs      int64
}

func runStatusCLIPathBench(b *testing.B, pass *statusCLIPass, name string, args []string) {
	b.Helper()
	fx := newStatusCLIFixture(b)
	b.Setenv("BEACON_PROJECT_ROOT", fx.root)
	_, _ = runStatusCLIPath(b, fx.configPath, args)

	durations := make([]time.Duration, 0, b.N)
	var work, errs int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		out, err := runStatusCLIPath(b, fx.configPath, args)
		durations = append(durations, time.Since(start))
		if err != nil {
			errs++
			continue
		}
		work += statusCLIWork(out)
	}
	b.StopTimer()
	if errs != 0 || work <= 0 {
		b.Fatalf("%s: work=%d errs=%d, want positive work and no errors", name, work, errs)
	}
	pass.durations = append(pass.durations, durations...)
	pass.work += work
	pass.errs += errs
	median := statusCLIMedian(durations)
	printStatusCLIReport(os.Stdout, name, median, work, errs)
	b.ReportMetric(float64(median.Nanoseconds()), name+"-median-ns")
	b.ReportMetric(float64(work), name+"-work")
	b.ReportMetric(float64(errs), name+"-errs")
	b.ReportMetric(1, name+"-limiter-"+statusCLILimiter)
}

// BenchmarkStatusCLIPaths times the three read paths of beacon status. Work is
// the JSON bytes each path writes to a counting writer. Errs counts command
// errors. The fixture, config and state database live in a temp git root, so
// the paths read no operator files.
//
// CI runs:
//
//	go test -vet=off -run '^$' -bench '^BenchmarkStatusCLIPaths$' -benchtime=1x -count=10 -benchmem ./cmd/beacon/
func BenchmarkStatusCLIPaths(b *testing.B) {
	paths := []struct {
		name string
		args []string
	}{
		{"status-cli-active", []string{}},
		{"status-cli-recent", []string{"--recent"}},
		{"status-cli-snapshot", []string{"--snapshot"}},
	}
	pass := &statusCLIPass{}
	for _, p := range paths {
		b.Run(p.name, func(b *testing.B) {
			runStatusCLIPathBench(b, pass, p.name, p.args)
		})
	}
	if pass.errs != 0 || pass.work <= 0 {
		b.Fatalf("status-cli-paths: work=%d errs=%d, want positive work and no errors", pass.work, pass.errs)
	}
	printPerfRow(os.Stdout, "status-cli-paths", pass.durations, pass.work, pass.errs, statusCLILimiter)
}
