# Beacon

Beacon is a controller for parallel AI agent work. It picks non-overlapping tasks, runs each in a worktree, watches the resulting pull requests, and either merges them, rebases them, or wakes the agent with a short error.

## Quick start

Beacon works in any git project. These steps install the binary, write the config, and check the setup.

1. Install the binary with `bash scripts/install-beacon.sh`. The script builds `beacon` into `$BEACON_INSTALL_DIR/beacon`. `BEACON_INSTALL_DIR` defaults to `~/.local/bin`. When the repo has `go.mod` and `cmd/beacon`, the script also refreshes `.opencode/bin/beacon` and writes its build hash. Add `~/.local/bin` to PATH if the script warns about it.
2. Run `beacon init`. The wizard reads the origin remote and base branch, detects the stack, and writes `.beacon/config.yaml`. It creates `docs/tasks/.gitkeep`, appends safe `.beacon` entries to `.gitignore`, generates or extends `opencode.json`, and deploys `.opencode/plugins/beacon-project/`. Pass `--yes` to accept the defaults without a prompt.
3. Export `GITHUB_TOKEN` or `GITLAB_TOKEN`, and `BEACON_OPENCODE_TOKEN` if the harness needs it.
4. Run `beacon doctor`. It prints an `[OK]`, `[WARN]`, or `[FAIL]` line per check and exits 1 if any check fails. Fix every FAIL line before dispatching.
5. Restart the OpenCode TUI and the MCP daemon once. They load the new `opencode.json` and plugin at startup.
6. Dispatch the first task with `beacon_dispatch`.

After Go code lands on `main`, run `bash scripts/build-beacon.sh` and restart the MCP daemon and the OpenCode TUI once. Go code is not hot-reloaded. The plugin does not watch HEAD and does not call `ctx.mcp.reload()` on its own.

### Init flags

`beacon init` accepts these flags. Every flag is optional.

| Flag | Default | Effect |
| --- | --- | --- |
| `--max-in-flight` | `10` | Maximum unfinished runs. |
| `--max-active-agents` | `2` | Maximum active agent sessions. |
| `--task-source` | `markdown` | One of `markdown`, `github_issues`, `gitlab_issues`. |
| `--scm` | detected from the remote | `github` or `gitlab`. |
| `--harness` | `opencode` | `opencode` or `cli`. |
| `--harness-endpoint` | `OPENCODE_ENDPOINT`, then `BEACON_OPENCODE_ENDPOINT`, then `http://127.0.0.1:4096` | OpenCode HTTP endpoint. |
| `--plugin-dir` | `BEACON_PLUGIN_DIR`, then the plugin bundled with the binary | Source directory for the project plugin. |
| `--force` | `false` | Overwrite an existing config. |
| `--non-interactive` | `false` | Use defaults without prompting. |
| `--yes` | `false` | Same as `--non-interactive`. |
| `--weak-pc` | `true` | Write targeted test instructions. Set to `false` for full verification. |
| `--skip-plugin` | `false` | Do not deploy the project plugin. |

The wizard writes `harness.command: worker`, `merge.max_attempts: 12`, and `on_base_red.mode: spawn_fixer` with `on_base_red.max_fix_attempts: 2`. It sets concurrency from `--max-in-flight` and `--max-active-agents`. It reads job names from `.github/workflows/*.yml` into `merge.required_checks` and prints them for review.

Stack detection checks `go.mod`, `package.json`, `pyproject.toml` or `requirements.txt`, `Cargo.toml`, then `*.sln` or `*.csproj`, in that order. Detected stacks get example test commands for that language. Other projects get the language-agnostic worker instructions described below.

Doctor accepts the beacon MCP entry at `mcp.beacon` or `mcp.servers.beacon` in the root `opencode.json`. If neither is there, it checks the parent directory's `opencode.json` and marks the location as `(workspace parent)`. It also requires `orchestrator` and `worker` agent objects in the root file.

## Layout

The repository is a Go workspace with three modules.

```text
pkg/core                 pure domain types and port interfaces, stdlib only
internal/app             dispatch, reconcile, control, status use cases
internal/infra           config, sqlite, planner, task sources, workspaces,
                         SCM adapters, policies, MCP server, notifications
cmd/beacon               composition root and cobra CLI
harnesses/opencode       separate AgentHarness module for OpenCode HTTP/SSE
harnesses/acp            separate AgentHarness module for ACP over stdio
ui/tui                   separate Bubble Tea monitor over loopback HTTP/SSE
```

Dependencies point from `cmd` to `internal/infra` to `internal/app` to `pkg/core`. The external modules import only `pkg/core`. `arch_test.go` and `.golangci.yml` enforce these boundaries. The 350-line production-file limit is advisory: `arch_test.go` logs an `ADVISORY` line instead of failing. Treat that as a prompt to decompose the file. `arch_test.go` enforces a cyclomatic complexity limit of 20 per function, with two functions grandfathered pending decomposition.

## Commands

```sh
beacon init                         # configure any git project
beacon doctor                       # check the setup, exit 1 on FAIL
beacon dispatch 20                  # start up to 20 non-overlapping tasks
beacon serve                        # reconciler, queue, webhook and loopback API
beacon mcp                          # ten MCP tools over stdio
beacon status                       # print active runs
beacon status --snapshot --recent-limit 5 --decisions-limit 20
beacon gc                           # remove worktrees that no active run uses
```

`beacon status --snapshot` bounds history by default: 20 recent runs, 20 decisions per run, and 100 run-less decisions. Metric totals come from SQL aggregates, so limiting the payload does not change the counters. The `--recent-limit` and `--decisions-limit` flags tighten the snapshot without a rebuild.

## MCP tools

`beacon mcp` serves ten tools over stdio. `ToolCatalog()` in `internal/infra/mcp/registry.go` declares them and marks the orchestrator-only ones. Six tools handle runs and are available to every agent. Four tools manage task cards or force reconciliation and are denied globally except for `orchestrator` and `build`.

| Tool | Role | What it does |
| --- | --- | --- |
| `beacon_dispatch` | worker | Pick up to N non-overlapping tasks and start runs. |
| `beacon_watch_pr` | worker | Bind a run ID to a pull request number. |
| `beacon_status` | worker | List active runs, pull requests and blockers. |
| `beacon_recent` | worker | List the most recently finished runs. |
| `beacon_wait` | worker | Wait for a run to reach a verdict or a terminal state. |
| `beacon_control` | worker | Approve, unblock, cancel or retry a run, or bind a PR. |
| `beacon_task_create` | orchestrator | Create one card or a batch of cards in one commit and push. |
| `beacon_task_delete` | orchestrator | Delete several cards in one commit and push. |
| `beacon_task_update` | orchestrator | Edit several cards in one commit and push. Omitted fields stay unchanged. |
| `beacon_reconcile` | orchestrator | Force one reconciler tick for a PR and report its decision. |

Permissions in `opencode.json` are generated, not hand-edited. Run `go generate ./...` after changing `ToolCatalog()`. `TestPermissionsUpToDate` regenerates the file into a temp copy and fails when the committed permissions differ. The MCP server runs as one pooled `beacon mcp` process started from the project `opencode.json`. If `beacon serve` is running, MCP forwards control calls to its loopback API. Otherwise it starts a reconciler in the same process under a lock.

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

The `harness` block in `.beacon/config.yaml` is provider neutral. `harness.provider` selects the adapter: `opencode`, `acp`, or a CLI provider such as `cli`, `claude`, `codex` and `exec`. The config loader stores the values and adds no provider name or endpoint of its own. An empty provider lets the composition root choose its default harness. Each adapter reads the fields it understands.

| Field | Used by |
| --- | --- |
| `provider` | Every adapter, to select one. |
| `command` | ACP and CLI adapters; the OpenCode adapter uses it as the session agent. |
| `endpoint` | HTTP adapters such as OpenCode. |
| `model` | Adapters that choose a worker model. |
| `token`, `token_env` | Adapters that authenticate to a service. |
| `parent_session_id` | Adapters that notify an orchestrator session. |
| `prompt_template` | CLI adapters. |
| `worker_instructions` | Every worker prompt, as a prefix added by the dispatcher. |

The OpenCode adapter talks to the running OpenCode background server. Set `harness.endpoint` for it. Authentication uses either a bearer token or `user:password` in `harness.token`, or the variable named by `harness.token_env`. The model resolves in this order: `BEACON_MODEL`, `harness.model`, then the model of the orchestrator session named in `harness.parent_session_id`. The ACP adapter launches the executable in `harness.command` over stdio and ignores `harness.endpoint`.

`beacon init` writes `harness.provider: opencode` and `harness.command: worker`. The OpenCode harness passes the command as the session agent, so workers inherit the global deny rules for the four orchestrator-only tools. A worker must never create, delete or update task cards, and must never force a reconcile. It reports new work to the orchestrator instead.

The default worker instructions are language agnostic: "Worker environment: weak PC. Run targeted tests only for the files or packages you changed; leave full test suites, full builds and full repo linters to CI." `beacon init` replaces this default with stack-specific instructions when it detects a supported stack. Set `harness.worker_instructions` to override both. With `--weak-pc=false`, `beacon init` writes instructions that run the full suite, the full build, and the repository linters.

## OpenCode plugin options

Set these in the beacon plugin's `userConfig` (or its config block on older OpenCode versions). Every option is optional.

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `statusPanelEnabled` | boolean | `true` | When false, neither the server RPC nor the TUI slots start. |
| `watcherEnabled` | boolean | `true` | When false, no background run-change watcher starts. |
| `statusPollMs` | number | `10000` | Poll interval in milliseconds. `0` or less disables background polling. |
| `snapshotRecentLimit` | positive integer | `5` | Recent runs requested from `beacon status --snapshot`. |
| `snapshotDecisionsLimit` | positive integer | `20` | Decisions per run requested from the snapshot. |
| `survivalRulesEnabled` | boolean | `true` | When false, no role instructions are injected into sessions. |
| `orchestratorRules` | string | built in | Replaces the orchestrator instruction block. |
| `workerRules` | string | built in | Replaces the worker instruction block. |
| `shellGuardRules` | array | nine built-in rules | Entries of `{id, pattern, replacement}` appended after the built-ins. |
| `shellGuardAgents` | array of strings | `["worker"]` | Agents subject to the shell guard. |
| `shellGuardAgent` | string or array | `["worker"]` | Singular alias for `shellGuardAgents`. |
| `shellGuard` | string | unset | `warn` logs a blocked command instead of throwing. `BEACON_SHELL_GUARD` overrides it. |
| `mcpDeps` | object | `{}` | Helpers passed to MCP registration for locating and rebuilding the binary. |
| `noopMcpRegistration` | function | built in | Replaces the no-op registration used in worker sessions. |

The built-in shell guard blocks recursive grep, root find, direct pushes to `main`, and full builds, test suites, vet runs, or lint runs. It applies to the `bash`, `shell`, and `execute` tools for agents in `shellGuardAgents`, and to events with no agent. Worker sessions receive the worker rules. Orchestrator sessions receive rules about narrow task scope, board checks before dispatch, and explicit binary rebuilds after Go changes on `main`.

## Loopback API

The monitor reaches the daemon on loopback only. The endpoint defaults to `http://127.0.0.1:7432` and is set with `--endpoint`. The token comes from `--token` or `BEACON_API_TOKEN` and is sent as `Authorization: Bearer <token>`.

| Route | Wire shape |
| --- | --- |
| `GET /api/v1/runs` | `{"runs":[Run]}` or a bare `[Run]` array. |
| `GET /api/v1/events` | `text/event-stream` frames named `harness`, `pr_changed`, and `ping`, each with a JSON data payload. |
| `POST /api/v1/control` | `{"action":"approve\|unblock\|cancel\|retry","run_id":"...","sha":"..."}`. |

A `Run` carries `id`, `task_id`, `state`, `pr`, `head_sha`, `branch`, `reason` and `updated_at`. The TUI reads each frame into an `Event` with `id`, `type`, `run_id` and `at`, using the frame name when the payload has no `type`. Treat events as notifications. After any event, re-read `/api/v1/runs` instead of treating the event payload as state.

## Verify

CI runs these checks for every module on each push and pull request.

```sh
go build ./...
go vet ./...
go test ./...
(cd harnesses/opencode && go build ./... && go vet ./... && go test ./...)
(cd ui/tui && go build ./... && go vet ./... && go test ./...)
```

The plugin entry modules import `Plugin` from `@opencode/plugin` and `@opencode/plugin/tui` so the host supplies one OpenTUI and Solid instance. Installing those peers into the plugin directory creates a second Solid context and fails with `No renderer found`. Plugin tests resolve the two imports to local doubles instead:

```sh
node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js --test .opencode/plugins/beacon-project
```

The lint job checks `gofmt -l cmd internal pkg harnesses ui arch_test.go` and runs golangci-lint v2.15. The advisory job runs `bash scripts/ci-advisory.sh`.

## Notes and decisions

Some Beacon behavior is surprising until you know the reason. `docs/decisions/` records those cases, what caused them, and the options that were considered. Start with [parent session notification delivery](docs/decisions/0001-parent-notify-delivery.md) if a merge notice reaches the chat late. Read [the first-render empty queue](docs/decisions/0003-plugin-first-render.md) if the sidebar shows no cards right after an OpenCode restart. See [second harness support](docs/decisions/0002-second-harness.md) for the capability ports and the ACP adapter that made a second harness possible.
