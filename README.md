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

Dependencies point from `cmd` to `internal/infra` to `internal/app` to `pkg/core`. The external modules import only `pkg/core`. `arch_test.go` and `.golangci.yml` enforce these boundaries. The 350-line production-file limit is advisory: `arch_test.go` logs an `ADVISORY` line instead of failing. Treat that as a prompt to decompose the file. `arch_test.go` enforces a cyclomatic complexity limit of 20 per function, with two functions grandfathered pending decomposition.

## Commands

```sh
beacon init                         # write .beacon/config.yaml from the git remote
beacon dispatch 20                  # start up to 20 non-overlapping tasks
beacon serve                        # reconciler, queue, webhook and loopback API
beacon mcp                          # nine MCP tools over stdio
beacon status                       # print active runs
beacon status --snapshot --recent-limit 5 --decisions-limit 20
beacon gc                           # remove worktrees that no active run uses
```

`beacon mcp` exposes `beacon_dispatch`, `beacon_watch_pr`, `beacon_status`, `beacon_recent`, `beacon_wait` and `beacon_control` for run work, plus `beacon_task_create`, `beacon_task_delete` and `beacon_task_update` for the orchestrator. If `beacon serve` is running, MCP forwards control calls to its loopback API. Otherwise it starts a reconciler in the same process under a lock.

`beacon status --snapshot` bounds history by default: 20 recent runs, 20 decisions per run and 100 run-less decisions. Metric totals come from SQL aggregates, so limiting the payload does not change the counters. The `--recent-limit` and `--decisions-limit` flags tighten the snapshot without a rebuild.

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

## OpenCode plugin options

Set these in the beacon plugin's `userConfig` (or its config block on older OpenCode versions). Every option is optional.

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `statusPanelEnabled` | boolean | `true` | When false, neither the server RPC nor the TUI slots start. |
| `watcherEnabled` | boolean | `true` | When false, no background run-change watcher starts. |
| `statusPollMs` | number | `10000` | Poll interval for the watcher and TUI panel. `<= 0` disables background polling. |
| `snapshotRecentLimit` | number | `5` | Recent runs requested from `beacon status --snapshot`. |
| `snapshotDecisionsLimit` | number | `20` | Decisions per run requested from the snapshot. |
| `survivalRulesEnabled` | boolean | `true` | When false, no role instructions are injected into sessions. |
| `orchestratorRules` | string | built in | Replaces the orchestrator instruction block. |
| `workerRules` | string | built in | Replaces the worker instruction block. |
| `shellGuardRules` | array | built in | Extra shell guard rules, appended to the defaults. |
| `shellGuardAgents` | array or string | `["worker"]` | Agents subject to the shell guard. |
| `shellGuard` | string | unset | `warn` logs a blocked command instead of throwing. |

Worker sessions get the worker rules, which list the six run tools and the three orchestrator-only tools. Orchestrator sessions get rules about narrow task scope, board checks before dispatch and explicit binary rebuilds after Go changes on `main`.

## Verify

```sh
go test ./...
go vet ./...
go build ./...
(cd harnesses/opencode && go test ./...)
(cd ui/tui && go test ./...)
```

CI runs the same checks for every module.
