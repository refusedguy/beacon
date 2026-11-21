package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
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

	if _, err := idx.RebuildDir(filepath.Join(projectRoot, "docs", "tasks")); err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}

	counts, err := idx.Counts()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon index build: %v\n", err)
		return 2
	}
	fmt.Fprintf(c.out, "beacon index build: cards=%d deps=%d owners=%d\n",
		counts.Cards, counts.Deps, counts.Owners)
	return 0
}
