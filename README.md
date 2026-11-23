# Beacon

Beacon is a small controller for parallel AI agent work. It picks non-overlapping tasks, runs each in an isolated git worktree, watches the resulting pull requests, and either merges them, rebases them, or wakes the agent with a short error.

## Layout

The repository is a Go workspace with three modules.

```text
pkg/core                 pure domain types and port interfaces, stdlib only
internal/app             dispatch, reconcile, control, status use cases
internal/infra           config, sqlite, planner, task sources, workspaces,
                         SCM adapters, policies, MCP server, notifications
cmd/beacon               composition root and cobra CLI
harnesses/opencode       separate AgentHarness module for OpenCode HTTP/SSE
ui/tui                   separate Bubble Tea monitor over loopback HTTP/SSE
```

Dependencies point from `cmd` to `internal/infra` to `internal/app` to `pkg/core`. The external modules import only `pkg/core`. `arch_test.go` and `.golangci.yml` enforce these boundaries and a 350-line limit per production file.

## Commands

```sh
beacon init                         # write .beacon/config.yaml from the git remote
beacon dispatch 20                  # start up to 20 non-overlapping tasks
beacon serve                        # reconciler, queue, webhook and loopback API
beacon mcp                          # five MCP tools over stdio
beacon status                       # print active runs
beacon gc                           # remove worktrees that no active run uses
```

`beacon mcp` exposes `beacon_dispatch`, `beacon_watch_pr`, `beacon_status`, `beacon_wait` and `beacon_control`. If `beacon serve` is running, MCP forwards control calls to its loopback API. Otherwise it starts a reconciler in the same process under a lock.

## Task cards

`docs/tasks/*.md` is the default task source. A card uses YAML frontmatter:

```yaml
---
id: TASK-01
title: Short title
status: available
scope:
  - internal/app/
  - internal/**/*.go
depends:
  - TASK-00
priority: 1
---
```

Legacy Russian fields such as `Статус:`, `Владелец файлов:` and `Зависимости:` are also accepted. GitHub issues and GitLab issues are supported as alternative task sources.

## Worker model

The OpenCode harness talks to the running OpenCode background server. Set `harness.endpoint` in `.beacon/config.yaml`. Authentication uses either a bearer token or `user:password` in `harness.token`, or in the variable named by `harness.token_env`. The model is resolved in this order: `BEACON_MODEL`, `harness.model`, then the model of the orchestrator session named in `harness.parent_session_id`. Nothing is hardcoded in the binary.

## Verify

```sh
go test ./...
go vet ./...
go build ./...
(cd harnesses/opencode && go test ./...)
(cd ui/tui && go test ./...)
```

CI runs the same checks for every module.
