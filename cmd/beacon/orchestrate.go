package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// orchestratorRunner holds the command implementation so CLI dispatch can
// call it without depending on the rendering details. RegisterOrchestrator
// replaces it in tests, mirroring RegisterWatch and RegisterGC.
var orchestratorRunner = func(c *CLI, args []string) int { return runOrchestrate(c, args) }

// RegisterOrchestrator installs the orchestrate command implementation.
func RegisterOrchestrator(fn func(*CLI, []string) int) {
	orchestratorRunner = fn
}

// orchestratorDeps is the three-port bundle the loop consumes. Tests install
// fakes with SetOrchestratorDeps; production builds it from the state store
// and the CLI child runner.
type orchestratorDeps struct {
	Store  orchestrator.Store
	Merger orchestrator.Merger
	Waker  orchestrator.Waker
}

type orchestratorDepsFactory func(*CLI) (orchestratorDeps, func() error, error)

var makeOrchestratorDeps orchestratorDepsFactory = defaultOrchestratorDeps

// SetOrchestratorDeps replaces the dependency factory. Passing nil restores
// the production factory. Wake delivery lands in another card, so a nil Waker
// is a valid injection: retry/fail Watches are still reported.
func SetOrchestratorDeps(fn orchestratorDepsFactory) {
	if fn == nil {
		fn = defaultOrchestratorDeps
	}
	makeOrchestratorDeps = fn
}

// ghMerger is the production Merger. It runs exactly
// `gh pr merge <pr> --squash --delete-branch` through the CLI runner, so the
// call is injectable in tests and no HTTP or shell is hidden here. Auto-merge
// is not used: this repository has enablePullRequestAutoMerge off, so --auto
// fails with a GraphQL error instead of queuing anything.
type ghMerger struct {
	c *CLI
}

var _ orchestrator.CheckedMerger = ghMerger{}

// firstLine keeps only gh's first diagnostic line. The following lines are
// CLI hints that would otherwise be logged by the daemon every three minutes.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return oneLine(s)
}

// ghError builds a single-line error from a failed child process. oneLine is
// the package helper in task.go; firstLine drops the multi-line gh hints.
func ghError(what string, res runResult) error {
	reason := firstLine(string(res.stderr))
	if reason == "" {
		reason = firstLine(string(res.stdout))
	}
	if reason == "" {
		reason = fmt.Sprintf("exit %d", res.code)
	}
	return fmt.Errorf("%s: %s", what, reason)
}

// prViewFields are the only fields the merge gate and the confirmation read.
type prViewFields struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	State            string `json:"state"`
}

// prView runs one read-only gh call. fields is either the full gate tuple or
// just "state" for the post-merge confirmation.
func (m ghMerger) prView(pr int, fields string) (prViewFields, error) {
	res := m.c.run(m.c.root, "gh", []string{
		"pr", "view", strconv.Itoa(pr), "--json", fields,
	}, nil)
	if res.code != 0 {
		return prViewFields{}, ghError(fmt.Sprintf("gh pr view %d", pr), res)
	}
	var out prViewFields
	if err := json.Unmarshal(res.stdout, &out); err != nil {
		return prViewFields{}, fmt.Errorf("gh pr view %d decode: %w", pr, err)
	}
	return out, nil
}

// MergeChecked gates the merge on GitHub's own view of the PR. A PR whose
// mergeStateStatus is not CLEAN (for example a dirty/conflicted PR) is refused
// before `gh pr merge` runs, with a one-line diagnostic instead of gh's long
// "add the --auto flag" hint. After a plain merge succeeds, the PR state is
// confirmed before the orchestrator writes its ledger or closes the Watch.
func (m ghMerger) MergeChecked(ctx context.Context, repo string, pr int) (orchestrator.MergeOutcome, error) {
	_ = ctx
	before, err := m.prView(pr, "mergeable,mergeStateStatus,state")
	if err != nil {
		return orchestrator.MergeOutcome{}, err
	}
	if strings.EqualFold(strings.TrimSpace(before.State), "MERGED") {
		return orchestrator.MergeOutcome{Merged: true}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(before.Mergeable), "MERGEABLE") ||
		!strings.EqualFold(strings.TrimSpace(before.MergeStateStatus), "CLEAN") {
		mergeable := before.Mergeable
		if strings.TrimSpace(mergeable) == "" {
			mergeable = "UNKNOWN"
		}
		status := before.MergeStateStatus
		if strings.TrimSpace(status) == "" {
			status = "UNKNOWN"
		}
		return orchestrator.MergeOutcome{
			Reason: fmt.Sprintf("mergeable=%s mergeStateStatus=%s", mergeable, status),
		}, nil
	}
	if err := m.Merge(ctx, repo, pr); err != nil {
		return orchestrator.MergeOutcome{}, err
	}
	after, err := m.prView(pr, "state")
	if err != nil {
		return orchestrator.MergeOutcome{}, err
	}
	if strings.EqualFold(strings.TrimSpace(after.State), "MERGED") {
		return orchestrator.MergeOutcome{Merged: true}, nil
	}
	return orchestrator.MergeOutcome{Pending: true}, nil
}

func (m ghMerger) Merge(ctx context.Context, repo string, pr int) error {
	_ = ctx
	res := m.c.run(m.c.root, "gh", []string{
		"pr", "merge", strconv.Itoa(pr), "--squash", "--delete-branch",
	}, nil)
	if res.code == 0 {
		return nil
	}
	return ghError(fmt.Sprintf("gh pr merge %d", pr), res)
}

// orchWatchReaderFunc adapts a function to the orchestrator's WatchReader
// port. The projection from the domain store happens here in the composition
// root, so neither cmd nor the app package needs to name domain types.
type orchWatchReaderFunc func(context.Context) ([]orchestrator.Watch, error)

func (f orchWatchReaderFunc) ActiveWatches(ctx context.Context) ([]orchestrator.Watch, error) {
	return f(ctx)
}

// defaultOrchestratorDeps opens the real state store from the same directory
// the MCP server uses and combines it with the gh merger. The concrete *store.DB
// is needed for the merge ledger and for beacon_cancel_watch's event log.
func defaultOrchestratorDeps(c *CLI) (orchestratorDeps, func() error, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return orchestratorDeps{}, nil, err
	}
	db, ok := deps.Store.(*store.DB)
	if !ok {
		_ = closeStore()
		return orchestratorDeps{}, nil, fmt.Errorf("beacon orchestrate: store is not *store.DB")
	}
	reader := orchWatchReaderFunc(func(_ context.Context) ([]orchestrator.Watch, error) {
		all, err := deps.Store.ActiveWatches()
		if err != nil {
			return nil, err
		}
		out := make([]orchestrator.Watch, 0, len(all))
		for _, w := range all {
			var policy *orchestrator.AutoMergePolicy
			if raw, err := json.Marshal(w); err == nil {
				policy = orchestrator.PolicyFromPayload(raw)
			}
			out = append(out, orchestrator.Watch{
				ID:       w.ID,
				Repo:     w.Repo,
				PR:       w.PR,
				HeadSHA:  w.HeadSHA,
				Verdict:  string(w.Verdict),
				Revision: w.Revision,
				Policy:   policy,
			})
		}
		return out, nil
	})
	return orchestratorDeps{
		Store:  orchestrator.NewDBStore(reader, db),
		Merger: ghMerger{c: c},
	}, closeStore, nil
}

// runOrchestrate implements `beacon orchestrate [--dry-run] [--format table|json]`.
// It makes one pass over active Watches, prints the actions and returns the
// PRs that need a rebase in the NeedsRebase field of the report.
func runOrchestrate(c *CLI, args []string) int {
	fs := flag.NewFlagSet("orchestrate", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "печатать действия без вызова gh и мержа")
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("orchestrate", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon orchestrate: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	deps, closeStore, err := makeOrchestratorDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrate: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	result, err := orchestrator.Run(context.Background(), deps.Store, deps.Merger, deps.Waker,
		orchestrator.Options{DryRun: *dryRun})
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrate: %v\n", err)
	}
	if *format == "json" {
		if encErr := renderOrchestratorJSON(c.out, result); encErr != nil {
			fmt.Fprintf(c.errOut, "beacon orchestrate: %v\n", encErr)
			return 2
		}
	} else {
		renderOrchestratorTable(c.out, result)
	}
	if err != nil {
		return 2
	}
	return 0
}

func renderOrchestratorTable(w io.Writer, result orchestrator.Result) {
	if result.DryRun {
		fmt.Fprintln(w, "РЕЖИМ: просмотр без мержа")
	}
	for _, a := range result.Actions {
		line := fmt.Sprintf("%s watch=%s pr=%d revision=%d", a.Action, a.WatchID, a.PR, a.Revision)
		if a.Command != "" {
			line += " cmd=" + a.Command
		}
		if a.Message != "" {
			line += " msg=" + a.Message
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "MERGED: %d\n", len(result.Merged))
	fmt.Fprintf(w, "SKIPPED: %d\n", len(result.Skipped))
	fmt.Fprintf(w, "NEEDS REBASE: %d\n", len(result.NeedsRebase))
	for _, pr := range result.NeedsRebase {
		fmt.Fprintf(w, "REBASE pr=%d\n", pr)
	}
}

func renderOrchestratorJSON(w io.Writer, result orchestrator.Result) error {
	return json.NewEncoder(w).Encode(result)
}
