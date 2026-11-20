package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// defaultCIRepo is used when --repo is absent. The project config hook will
// supply this value once the research card defines its schema; until then the
// flag and this constant are the only sources.
const defaultCIRepo = "refusedguy/beacon"

// ciRunner holds the command so dispatch stays a one-line wiring change and
// tests can replace the whole implementation, as with watch and task.
var ciRunner = func(c *CLI, args []string) int { return runCI(c, args) }

// RegisterCI installs the ci command implementation.
func RegisterCI(fn func(*CLI, []string) int) { ciRunner = fn }

func runCI(c *CLI, args []string) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintf(c.errOut, "beacon ci: ожидается подкоманда check\n")
		writeUsage(c.errOut)
		return 2
	}
	fs := flag.NewFlagSet("ci check", flag.ContinueOnError)
	all := fs.Bool("all", false, "проверить все открытые PR")
	dryRun := fs.Bool("dry-run", false, "только показать план, не менять Watch")
	repo := fs.String("repo", defaultCIRepo, "slug репозитория для Watch")
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("ci check", args[1:], c.errOut, fs) {
		return 2
	}
	if !*all {
		fmt.Fprintf(c.errOut, "beacon ci check: нужен флаг --all\n")
		writeUsage(c.errOut)
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon ci check: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon ci check: состояние: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	// gh is reached only through the CLI's replaceable runner. This keeps
	// os/exec out of the command file and lets tests fake every gh call.
	runGH := func(args ...string) ([]byte, error) {
		res := c.run(c.root, "gh", args, nil)
		if res.code == 0 {
			return res.stdout, nil
		}
		reason := strings.TrimSpace(string(res.stderr))
		if reason == "" {
			reason = strings.TrimSpace(string(res.stdout))
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), reason)
	}

	// The MCP watch_pr handler owns domain.Fanout. Reusing its Register keeps
	// the alias, obligation and subscriber invariants when a Watch is missing;
	// this command only passes the orchestrator role and a stable notify target.
	watchHandler := mcp.NewWatchPRHandler(deps.Store, deps.CI, deps.Clock)
	registry := orchestrator.FuncRegistry{
		FindFn: func(_ context.Context, pr orchestrator.PR) (orchestrator.WatchRef, bool, error) {
			watches, err := deps.Store.ActiveWatches()
			if err != nil {
				return orchestrator.WatchRef{}, false, err
			}
			for _, w := range watches {
				if w.Repo == pr.Repo && w.PR == pr.Number && w.HeadSHA == pr.HeadSHA {
					return orchestrator.WatchRef{
						ID: w.ID, Repo: w.Repo, PR: w.PR, HeadSHA: w.HeadSHA, Revision: w.Revision,
					}, true, nil
				}
			}
			return orchestrator.WatchRef{}, false, nil
		},
		RegisterFn: func(ctx context.Context, pr orchestrator.PR) (orchestrator.WatchRef, error) {
			out, err := watchHandler.Register(ctx, nil, mcp.WatchPRInput{
				Repo:    pr.Repo,
				PR:      pr.Number,
				HeadSHA: pr.HeadSHA,
				Role:    "orchestrator",
				Notify:  &mcp.WatchPRNotify{Adapter: "opencode", Target: "beacon-ci-checkall"},
			})
			if err != nil {
				return orchestrator.WatchRef{}, err
			}
			return orchestrator.WatchRef{
				ID: out.WatchID, Repo: pr.Repo, PR: pr.Number, HeadSHA: pr.HeadSHA, Revision: out.Revision,
			}, nil
		},
	}

	txWriter, ok := deps.Store.(orchestrator.TxWriter)
	if !ok {
		fmt.Fprintf(c.errOut, "beacon ci check: хранилище не поддерживает запись вердикта\n")
		return 2
	}

	summary, err := orchestrator.CheckAll(context.Background(), orchestrator.Deps{
		Source:    orchestrator.NewGHSource(*repo, runGH),
		Watches:   registry,
		Writer:    orchestrator.NewSQLVerdictWriter(txWriter),
		ConfigDir: c.root,
		DryRun:    *dryRun,
	})
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon ci check: %v\n", err)
		return 2
	}

	if *format == "json" {
		if err := json.NewEncoder(c.out).Encode(summary); err != nil {
			fmt.Fprintf(c.errOut, "beacon ci check: %v\n", err)
			return 2
		}
		return 0
	}
	renderCITable(c.out, summary)
	return 0
}

func renderCITable(w io.Writer, summary orchestrator.Summary) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PR\tREPO\tWATCH\tVERDICT\tFAILED CHECKS\tERROR")
	for _, row := range summary.Rows {
		failed := strings.Join(row.FailedChecks, ",")
		if failed == "" {
			failed = "-"
		}
		errText := row.Error
		if errText == "" {
			errText = "-"
		}
		watch := row.WatchID
		if watch == "" {
			watch = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			row.PR, row.Repo, watch, row.Verdict, failed, errText)
	}
	_ = tw.Flush()
	mode := "ПРИМЕНЕНО"
	if summary.DryRun {
		mode = "ПРОСМОТР (--dry-run, записи не было)"
	}
	fmt.Fprintf(w, "%s: зарегистрировано %d, вердиктов %d\n", mode, summary.Registered, summary.Published)
}
