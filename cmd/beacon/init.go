package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/infra/github"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
)

// Q-D-02: beacon init prepares a project in three steps: install the git
// hooks, write the .beacon templates and prove that the MCP server answers
// tools/list over a real in-process connection. The command never edits the
// Beacon checkout it was built from; it edits the --root directory.
//
// Dispatch and the catalog entry live in main.go and catalog.go, which are
// outside this card's OwnerFiles. RegisterInit is the seam a wiring change
// calls from dispatch. The runner stays usable on its own in tests.
var initRunner = func(c *CLI, args []string) int { return runInit(c, args) }

// RegisterInit installs the init command implementation.
func RegisterInit(fn func(*CLI, []string) int) { initRunner = fn }

// The file seams keep direct os calls out of cmd/beacon, which the
// capabilities gate forbids (ADR 0005). Tests replace them to write to a
// temporary directory.
var (
	initMkdirAll  = os.MkdirAll
	initStat      = os.Stat
	initWriteFile = os.WriteFile
	initGetwd     = os.Getwd
)

const initPreCommitHook = `#!/usr/bin/env bash
# Beacon pre-commit: cheap, offline guards for every worktree.
set -euo pipefail

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
if { [ "$branch" = "main" ] || [ "$branch" = "master" ]; } && [ "${BEACON_ALLOW_MAIN_COMMIT:-0}" != "1" ]; then
  echo "FORBIDDEN: commit to $branch; use a feature branch or worktree." >&2
  exit 1
fi

staged_go=$(git diff --cached --name-only --diff-filter=ACMR -- '*.go')
if [ -n "$staged_go" ]; then
  bad=$(printf '%s\n' "$staged_go" | xargs -r gofmt -l)
  if [ -n "$bad" ]; then
    echo "FORBIDDEN: staged Go files are not gofmt'ed:" >&2
    printf '%s\n' "$bad" >&2
    exit 1
  fi
fi
`

const initPrePushHook = `#!/usr/bin/env bash
# Beacon pre-push: block a direct push to the default branch.
set -euo pipefail
while read -r _local_ref _local_sha remote_ref _remote_sha; do
  case "$remote_ref" in
    refs/heads/main|refs/heads/master)
      echo "FORBIDDEN: push to $remote_ref; open a pull request instead." >&2
      exit 1
      ;;
  esac
done
`

// initConfigContent fills the repo key from git remote detection when the
// project already has an origin. An empty slug stays a comment so Beacon
// never invents a repository name.
func initConfigContent(slug string) string {
	repoLine := "# repo: owner/name"
	if slug != "" {
		repoLine = "repo: " + slug
	}
	return fmt.Sprintf(`# Beacon project config, written by beacon init.
# Project keys override ~/.config/beacon/config.yaml.
%s
maxRebases: 3
maxWakesPerWatch: 6
slotsTotal: 4
flaky:
  names: []
  maxRetries: 2
`, repoLine)
}

const initFlakyContent = `{
  "checks": [],
  "maxRetries": 2
}
`

type initFile struct {
	path    string
	mode    os.FileMode
	content string
}

func initFiles(slug string) []initFile {
	return []initFile{
		{path: filepath.Join(".githooks", "pre-commit"), mode: 0o755, content: initPreCommitHook},
		{path: filepath.Join(".githooks", "pre-push"), mode: 0o755, content: initPrePushHook},
		{path: filepath.Join(".beacon", "config.yaml"), mode: 0o644, content: initConfigContent(slug)},
		{path: filepath.Join(".beacon", "flaky.json"), mode: 0o644, content: initFlakyContent},
	}
}

// initMCPSmoke opens a fresh Beacon MCP server and a client on an in-memory
// transport, then lists tools. A connection failure or a missing contracted
// tool fails init: a project that cannot answer tools/list cannot run a Watch.
func initMCPSmoke() (int, error) {
	ctx := context.Background()
	server := beaconmcp.NewServer()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "beacon-init-smoke", Version: "v0.0.0"}, nil)
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return 0, fmt.Errorf("connect MCP server: %w", err)
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		return 0, fmt.Errorf("connect MCP client: %w", err)
	}
	defer func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}()

	listed := make(map[string]bool)
	for tool, err := range clientSession.Tools(ctx, nil) {
		if err != nil {
			return 0, fmt.Errorf("tools/list: %w", err)
		}
		listed[tool.Name] = true
	}

	var missing []string
	for _, name := range beaconmcp.ToolNames {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return len(listed), fmt.Errorf("tools/list без инструментов: %s", strings.Join(missing, ", "))
	}
	return len(listed), nil
}

// runInit writes the template files into target and runs the MCP smoke check.
// Existing files are kept unless --force is set, so re-running init is safe.
func runInit(c *CLI, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	root := fs.String("root", "", "каталог проекта (по умолчанию текущий)")
	force := fs.Bool("force", false, "перезаписать существующие файлы Beacon")
	if !parseFlags("init", args, c.errOut, fs) {
		return 2
	}

	target := strings.TrimSpace(*root)
	if target == "" {
		wd, err := initGetwd()
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon init: текущий каталог: %v\n", err)
			return 2
		}
		target = wd
	}
	target = filepath.Clean(target)

	for _, f := range initFiles(github.DetectRepoSlug(target)) {
		path := filepath.Join(target, f.path)
		if !*force {
			if _, err := initStat(path); err == nil {
				fmt.Fprintf(c.out, "skip %s (уже есть; используйте --force)\n", f.path)
				continue
			} else if !os.IsNotExist(err) {
				fmt.Fprintf(c.errOut, "beacon init: %s: %v\n", f.path, err)
				return 2
			}
		}
		if err := initMkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(c.errOut, "beacon init: %s: не создан каталог: %v\n", f.path, err)
			return 2
		}
		if err := initWriteFile(path, []byte(f.content), f.mode); err != nil {
			fmt.Fprintf(c.errOut, "beacon init: %s: не записан файл: %v\n", f.path, err)
			return 2
		}
		fmt.Fprintf(c.out, "created %s\n", f.path)
	}

	res := c.run(target, "git", []string{"config", "core.hooksPath", ".githooks"}, nil)
	if res.code != 0 {
		detail := strings.TrimSpace(string(res.stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(res.stdout))
		}
		fmt.Fprintf(c.errOut, "beacon init: git config core.hooksPath: %s\n", detail)
		return 2
	}
	fmt.Fprintln(c.out, "git: core.hooksPath = .githooks")

	count, err := initMCPSmoke()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon init: MCP smoke: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.out, "MCP smoke: ok, tools/list вернул %d инструментов\n", count)
	return 0
}
