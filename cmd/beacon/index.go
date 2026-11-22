package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
	"github.com/refusedguy/beacon/internal/infra/worktree"
)

// indexRunner is the dispatch seam for the index commands. The switch in
// cmd/beacon/main.go belongs to another card's OwnerFiles, so wiring installs
// the final dispatch case without editing this file.
var indexRunner = func(c *CLI, args []string) int { return runIndex(c, args) }

// RegisterIndex installs the index command implementation.
func RegisterIndex(fn func(*CLI, []string) int) { indexRunner = fn }

func runIndex(c *CLI, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.errOut, "beacon index: подкоманда не задана")
		return 2
	}
	switch args[0] {
	case "build":
		return runIndexBuild(c, args[1:])
	case "ignore-check":
		return runIndexIgnoreCheck(c, args[1:])
	default:
		fmt.Fprintf(c.errOut, "beacon index: неизвестная подкоманда %q\n", args[0])
		return 2
	}
}

// runIndexBuild пересобирает SQLite-индекс из карточек. Чтение и запись файлов
// остаются в taskstore: cmd только выбирает каталоги и печатает итог.
func runIndexBuild(c *CLI, args []string) int {
	fs := flag.NewFlagSet("index build", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	root := fs.String("root", c.root, "корень проекта с docs/tasks")
	if !parseFlags("index build", args, c.errOut, fs) {
		return 2
	}

	projectRoot := *root
	if projectRoot == "" {
		projectRoot = c.root
	}

	dir, err := stateDir(projectRoot)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}

	idx, err := taskstore.OpenIndex(taskstore.IndexPath(dir, projectRoot))
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}
	defer func() { _ = idx.Close() }()

	cardsDir := filepath.Join(projectRoot, "docs", "tasks")

	// CA-12: consult the ignore spec before the rebuild. The pre-pass never
	// opens file contents, so the rebuild keeps its own single read per card.
	spec := worktree.NewIgnoreSpec(filepath.Join(projectRoot, ".beaconignore"))
	ignored := spec.CountIgnored(cardsDir)

	stats, err := idx.RebuildDirTimed(cardsDir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}

	fmt.Fprintf(c.out, "beacon index build: cards=%d deps=%d owners=%d duration_ms=%d ignored=%d\n",
		stats.Cards, stats.Deps, stats.Owners, stats.Duration.Milliseconds(), ignored)
	return 0
}

// runIndexIgnoreCheck implements the CA-12 check: walk a directory more than
// once and print the counters. The first pass fills the cache, so every later
// pass over an unchanged tree must leave content_reads at its first-pass
// value. A file above IgnoreMaxContentBytes never becomes a content read.
func runIndexIgnoreCheck(c *CLI, args []string) int {
	fs := flag.NewFlagSet("index ignore-check", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	root := fs.String("root", c.root, "каталог для обхода")
	rules := fs.String("rules", "", "файл правил игнорирования")
	passes := fs.Int("passes", 2, "число обходов")
	if !parseFlags("index ignore-check", args, c.errOut, fs) {
		return 2
	}
	if *passes < 1 {
		fmt.Fprintln(c.errOut, "beacon index ignore-check: passes должно быть >= 1")
		return 2
	}

	spec := worktree.NewIgnoreSpec(*rules)
	previous := int64(0)
	for i := 0; i < *passes; i++ {
		kept, err := spec.Walk(*root)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon index ignore-check: обход %s: %v\n", *root, err)
			return 2
		}
		st := spec.Stats()
		fmt.Fprintf(c.out, "pass=%d kept=%d new_reads=%d content_reads=%d cache_hits=%d rule_loads=%d\n",
			i+1, len(kept), st.ContentReads-previous, st.ContentReads, st.CacheHits, st.RuleLoads)
		previous = st.ContentReads
	}
	return 0
}
