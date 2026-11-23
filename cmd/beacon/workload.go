package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/registry"
	"gopkg.in/yaml.v3"
)

// workloadRunner holds the command implementation so CLI dispatch stays a
// one-line wiring change and tests can replace it like the other commands.
var workloadRunner = func(c *CLI, args []string) int { return runWorkload(c, args) }

// RegisterWorkload installs the workload command implementation.
func RegisterWorkload(fn func(*CLI, []string) int) { workloadRunner = fn }

// workloadDeps is the single port the command consumes. Production builds it
// from the state store and the watch_pr registration handler.
type workloadDeps struct {
	Store orchestrator.WorkloadStore
}

type workloadDepsFactory func(*CLI) (workloadDeps, func() error, error)

var makeWorkloadDeps workloadDepsFactory = defaultWorkloadDeps

// workloadStore adapts the state store and the watch_pr handler to the
// WorkloadStore port. The handler's Register reuses an existing Watch by its
// (repo, pr, headSHA) alias and renews an existing subscriber, so the
// orchestrator registration is idempotent without a second code path.
type workloadStore struct {
	activeFn func(context.Context) ([]orchestrator.WorkloadWatch, error)
	register func(context.Context, orchestrator.WorkloadWatch, orchestrator.WorkloadSubscriber) error
}

var _ orchestrator.WorkloadStore = (*workloadStore)(nil)

func (s *workloadStore) ActiveWorkloadWatches(ctx context.Context) ([]orchestrator.WorkloadWatch, error) {
	if s == nil || s.activeFn == nil {
		return nil, fmt.Errorf("beacon workload: watch reader is nil")
	}
	return s.activeFn(ctx)
}

func (s *workloadStore) Subscribe(ctx context.Context, w orchestrator.WorkloadWatch, sub orchestrator.WorkloadSubscriber) error {
	if s == nil || s.register == nil {
		return fmt.Errorf("beacon workload: registrar is nil")
	}
	return s.register(ctx, w, sub)
}

// budgetedWorkloadStore adds the optional slot-budget capability to the CLI's
// WorkloadStore. The daemon uses the plain workloadStore and keeps its earlier
// behaviour; only the workload command resolves the configured budget.
type budgetedWorkloadStore struct {
	orchestrator.WorkloadStore
	budgetFn func(context.Context) (int, int, error)
}

func (s budgetedWorkloadStore) SlotBudget(ctx context.Context) (int, int, error) {
	return s.budgetFn(ctx)
}

// slotBudgetFromConfig reads the optional limits.max_concurrent_agents field
// from one agents.yaml. A missing file, a missing limits block or a missing
// key returns nil so the caller applies the documented default. A malformed
// limits block is an error.
func slotBudgetFromConfig(path string) (*int, error) {
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("beacon workload: read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("beacon workload: parse %s: %w", path, err)
	}
	limitsMap, ok := doc["limits"].(map[string]any)
	if !ok {
		return nil, nil
	}
	raw, ok := limitsMap["max_concurrent_agents"]
	if !ok {
		return nil, nil
	}
	budget, err := registry.ParseSlotBudgetValue(raw)
	if err != nil {
		return nil, fmt.Errorf("beacon workload: %s: %w", path, err)
	}
	return &budget, nil
}

// defaultWorkloadDeps opens the same state store the MCP server uses. The
// projection from the stored Watch happens here in the composition root, so
// neither cmd nor the app package needs to grow a domain dependency.
func defaultWorkloadDeps(c *CLI) (workloadDeps, func() error, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return workloadDeps{}, nil, err
	}
	handler := mcp.NewWatchPRHandler(deps.Store, deps.CI, deps.Clock)
	ws := &workloadStore{
		activeFn: func(context.Context) ([]orchestrator.WorkloadWatch, error) {
			all, err := deps.Store.ActiveWatches()
			if err != nil {
				return nil, err
			}
			out := make([]orchestrator.WorkloadWatch, 0, len(all))
			for _, w := range all {
				view := orchestrator.WorkloadWatch{
					ID:      w.ID,
					Repo:    w.Repo,
					PR:      w.PR,
					HeadSHA: w.HeadSHA,
					Project: w.Repo,
					Verdict: string(w.Verdict),
				}
				for _, s := range w.Subscribers {
					view.Subscribers = append(view.Subscribers, orchestrator.WorkloadSubscriber{
						ID: s.ID, Role: string(s.Role),
					})
				}
				out = append(out, view)
			}
			return out, nil
		},
		register: func(ctx context.Context, w orchestrator.WorkloadWatch, sub orchestrator.WorkloadSubscriber) error {
			// RunWorkload only ever enrolls the orchestrator, so the role is a
			// constant here and cmd needs no domain import (layer matrix).
			if sub.Role != orchestrator.RoleOrchestrator {
				return fmt.Errorf("beacon workload: unsupported subscriber role %q", sub.Role)
			}
			_, err := handler.Register(ctx, nil, mcp.WatchPRInput{
				Repo:    w.Repo,
				PR:      w.PR,
				HeadSHA: w.HeadSHA,
				Role:    "orchestrator",
				Notify:  &mcp.WatchPRNotify{Adapter: "opencode", Target: sub.ID},
			})
			return err
		},
	}
	rc, err := findRegistryConfig(c.root)
	if err != nil {
		_ = closeStore()
		return workloadDeps{}, nil, err
	}
	configured, err := slotBudgetFromConfig(rc.Path)
	if err != nil {
		_ = closeStore()
		return workloadDeps{}, nil, err
	}
	return workloadDeps{Store: budgetedWorkloadStore{
		WorkloadStore: ws,
		budgetFn: func(ctx context.Context) (int, int, error) {
			total, err := registry.ResolveSlotBudget(configured)
			if err != nil {
				return 0, 0, err
			}
			watches, err := ws.ActiveWorkloadWatches(ctx)
			if err != nil {
				return 0, 0, err
			}
			return total, len(watches), nil
		},
	}}, closeStore, nil
}

// runWorkload implements `beacon workload [--format table|json]`. It does not
// add an MCP tool: the contract stays at exactly seven tools and this is a CLI
// command over the same state store.
func runWorkload(c *CLI, args []string) int {
	fs := flag.NewFlagSet("workload", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("workload", args, c.errOut, fs) {
		return 2
	}
	if !parseFormatFlag(*format, "beacon workload: неизвестный формат %q, ожидается table или json\n", c.errOut, true) {
		return 2
	}

	deps, closeStore, err := makeWorkloadDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon workload: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	report, err := orchestrator.RunWorkload(context.Background(), deps.Store)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon workload: %v\n", err)
		return 2
	}
	if *format == "json" {
		if encErr := renderWorkloadJSON(c.out, report); encErr != nil {
			fmt.Fprintf(c.errOut, "beacon workload: %v\n", encErr)
			return 2
		}
	} else {
		renderWorkloadTable(c.out, report)
	}
	return 0
}

func renderWorkloadTable(w io.Writer, r orchestrator.Workload) {
	fmt.Fprintf(w, "ACTIVE: %d\n", r.Total)
	fmt.Fprintf(w, "READY: %d\n", r.Ready)
	fmt.Fprintf(w, "NEEDS ATTENTION: %d\n", r.NeedsAttention)
	fmt.Fprintf(w, "WATCHING: %d\n", r.Watching)
	fmt.Fprintf(w, "REGISTERED ORCHESTRATOR: %d\n", r.Registered)
	fmt.Fprintf(w, "SLOTS TOTAL: %d\n", r.SlotsTotal)
	fmt.Fprintf(w, "SLOTS USED: %d\n", r.SlotsUsed)
	for _, k := range sortedWorkloadKeys(r.ByVerdict) {
		fmt.Fprintf(w, "VERDICT %s: %d\n", k, r.ByVerdict[k])
	}
	for _, k := range sortedWorkloadKeys(r.ByRole) {
		fmt.Fprintf(w, "ROLE %s: %d\n", k, r.ByRole[k])
	}
	for _, k := range sortedWorkloadKeys(r.ByProject) {
		fmt.Fprintf(w, "PROJECT %s: %d\n", k, r.ByProject[k])
	}
}

func renderWorkloadJSON(w io.Writer, r orchestrator.Workload) error {
	return json.NewEncoder(w).Encode(r)
}

// sortedWorkloadKeys keeps the table output stable for a given set of buckets.
func sortedWorkloadKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
