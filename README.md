# Beacon

Beacon is a controller for parallel AI agent work. It picks non-overlapping tasks, runs each in a worktree, watches the resulting pull requests, and either merges them, rebases them, or wakes the agent with a short error.

## Quick start

Beacon works in any git project. These steps install the binary, write the config, and check the setup.

1. Install the binary with `bash scripts/install-beacon.sh`. The script builds `beacon` into `$BEACON_INSTALL_DIR/beacon`. `BEACON_INSTALL_DIR` defaults to `~/.local/bin`. When the repo has `go.mod` and `cmd/beacon`, the script also refreshes `.opencode/bin/beacon` and writes its build hash. Add `~/.local/bin` to PATH if the script warns about it.
2. Run `beacon init`. The wizard reads the origin remote and base branch, detects the stack, and writes `.beacon/config.yaml`. It creates `docs/tasks/.gitkeep`, appends safe `.beacon` entries to `.gitignore`, generates or extends `opencode.json`, and deploys `.opencode/plugins/beacon-project/`. Pass `--yes` to accept the defaults without a prompt.
3. Export `GITHUB_TOKEN` or `GITLAB_TOKEN` for SCM access. The config loader defaults `scm.token_env` to `GITHUB_TOKEN` (`internal/infra/config/config.go:358`, `internal/infra/config/config.go:249`). The OpenCode harness reads `harness.token_env`, then `harness.token` (`cmd/beacon/main.go:402`, `harnesses/opencode/opencode.go:34`). No production code reads `BEACON_OPENCODE_TOKEN`; `beacon init` only prints it as a hint (`cmd/beacon/init.go:136`).
4. Run `beacon doctor`. It prints an `[OK]`, `[WARN]`, or `[FAIL]` line per check and exits 1 if any check fails. Fix every FAIL line before dispatching. When the tools or the status panel do not appear, this one command names the missing prerequisite and its fix.
5. Restart the OpenCode TUI and the MCP daemon once. They load the new `opencode.json` and plugin at startup.
6. Dispatch the first task with `beacon_dispatch`.

After Go code lands on `main`, run `bash scripts/build-beacon.sh` and restart the MCP daemon and the OpenCode TUI once. Go code is not hot-reloaded. The plugin does not watch HEAD and does not call `ctx.mcp.reload()` on its own.

### Init flags

`beacon init` accepts these flags. Every flag is optional.

| Flag | Default | Effect |
| --- | --- | --- |
| `--max-in-flight` | `20` | Maximum unfinished runs. |
| `--max-active-agents` | `4` | Maximum active agent sessions. |
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

The wizard writes `harness.command: worker`, `merge.max_attempts: 12`, and `on_base_red.mode: spawn_fixer` with `on_base_red.max_fix_attempts: 2`. It sets concurrency from `--max-in-flight` and `--max-active-agents`. The flag defaults, `ApplyDefaults`, and the config loader's no-file fallback all read `DefaultMaxInFlight` and `DefaultMaxActiveAgents` in `internal/infra/config/config.go`, so a config gets the same concurrency whichever path created it. It reads job names from `.github/workflows/*.yml` into `merge.required_checks` and prints them for review.

Stack detection checks `go.mod`, `package.json`, `pyproject.toml` or `requirements.txt`, `Cargo.toml`, then `*.sln` or `*.csproj`, in that order. Detected stacks get example test commands for that language. Other projects get the language-agnostic worker instructions described below.

Doctor accepts the beacon MCP entry at `mcp.beacon` or `mcp.servers.beacon` in the root `opencode.json`. If neither is there, it checks the parent directory's `opencode.json` and marks the location as `(workspace parent)`. It also requires `orchestrator` and `worker` agent objects in the root file.

## Daemon ownership and restart

`beacon serve` is the long-lived daemon. It owns `.beacon/beacon.lock` through an exclusive file lock and refuses to start when another process holds it (`cmd/beacon/serve.go:28-33`, `cmd/beacon/lock.go:92-106`). While it runs it writes `.beacon/daemon.json` with its PID, start time, repo root and a heartbeat refreshed every five seconds (`cmd/beacon/lock.go:134-148`, `cmd/beacon/lock.go:177-231`). A fatal serve error is recorded in `.beacon/daemon-error.json` with a time, a kind and a message (`cmd/beacon/serve.go:81-86`, `cmd/beacon/lock.go:242-248`). Both files live beside the state database and match the `.beacon/daemon*` ignore entry from `beacon init`.

Start one daemon per state database:

```sh
beacon serve
```

`beacon status --daemon` prints the liveness record built from those files as JSON (`cmd/beacon/commands.go:138-150`, `cmd/beacon/lock.go:250-282`). The `state` field has six values:

| `state` | Meaning | Operator action |
| --- | --- | --- |
| `alive` | The PID exists and the heartbeat is at most 30 seconds old. | None. |
| `stale` | The PID exists, but the heartbeat is older than 30 seconds. | Restart the daemon. |
| `dead` | The PID no longer exists. | Restart the daemon. |
| `absent` | No `daemon.json` exists. | Start `beacon serve` when you need one owner for reconciliation, the loopback API and webhooks. |
| `invalid` | `daemon.json` exists but does not decode or carries no PID. | Inspect the file, then restart. |
| `unreadable` | `daemon.json` could not be read. | Check permissions, then restart. |

The record also carries `pid`, `started_at`, `root`, `heartbeat`, `heartbeat_age_seconds`, and an `error` object with `at`, `kind` and `message` when a fatal error was stored (`cmd/beacon/lock.go:157-166`). `beacon doctor` reports the same liveness as a `[OK]` or `[WARN]` line (`cmd/beacon/doctor.go:324-336`). A `lock`, `state` or `reconciler` kind in `daemon-error.json` means an MCP process tried to own the daemon role and failed (`cmd/beacon/mcp.go:39-58`).

To restart, stop the daemon, run `bash scripts/build-beacon.sh` after Go code changes, start `beacon serve`, then restart the MCP daemon and the OpenCode TUI once. Each start generates a fresh 48-character hex token and writes the loopback address, token and PID to `.beacon/connection.json` (`cmd/beacon/lock.go:108-120`, `cmd/beacon/serve.go:39-44`). Clients must read the current token after every restart; the old value is replaced. The file is gitignored (`cmd/beacon/init_config.go:182`).

## Layout

The repository is a Go workspace with four modules: the root module plus `harnesses/acp`, `harnesses/opencode` and `ui/tui` (`go.work:3-8`).

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

`beacon gc` calls `ForceRemove` on every worktree directory that no active run owns (`cmd/beacon/commands.go:189-227`). `ForceRemove` runs `git worktree remove --force` and deletes the branch without a clean check, so uncommitted work in an unused worktree is discarded (`internal/infra/workspace/git_worktree.go:468-482`, `pkg/core/errors.go:51-54`). Inspect those directories before running `gc`.

## MCP tools

`beacon mcp` serves ten tools over stdio. `ToolCatalog()` in `internal/infra/mcp/registry.go` declares them and marks the orchestrator-only ones. Six tools handle runs and are available to every agent. Four tools manage task cards or force reconciliation and are denied globally except for `orchestrator` and `build`.

| Tool | Role | What it does |
| --- | --- | --- |
| `beacon_dispatch` | worker | Pick up to N non-overlapping tasks and start runs. |
| `beacon_watch_pr` | worker | Bind a run ID to a pull request number. |
| `beacon_status` | worker | List active runs, pull requests and blockers. |
| `beacon_recent` | worker | List the most recently finished runs. |
| `beacon_wait` | worker | Wait for a run to reach a verdict or a terminal state. |
| `beacon_control` | worker | Approve, unblock, cancel, retry, nudge or watch a run. Pass `run_ids` to apply one verb to several runs. |
| `beacon_task_create` | orchestrator | Create one card or a batch of cards in one commit and push. |
| `beacon_task_delete` | orchestrator | Delete several cards in one commit and push. |
| `beacon_task_update` | orchestrator | Edit several cards in one commit and push. Omitted fields stay unchanged. |
| `beacon_reconcile` | orchestrator | Force one reconciler tick for a PR and report its decision. |

The control vocabulary is the six verbs in `pkg/core/control.go:8-26`; the CLI, the loopback API and the MCP tool all parse it through `ParseControlAction` (`internal/infra/mcp/tool_control.go:59-65`, `cmd/beacon/http.go:422-424`). A batch call sends `run_ids` instead of `run_id` and returns `{"action":"...","results":[{"id":"...","status":"ok|error","error":"..."}]}` (`internal/infra/mcp/tool_control.go:12-32`, `internal/infra/mcp/tool_control.go:67-86`).

Permissions in `opencode.json` are generated, not hand-edited. Run `go generate ./...` after changing `ToolCatalog()`. `TestPermissionsUpToDate` regenerates the file into a temp copy and fails when the committed permissions differ. The MCP server runs as one pooled `beacon mcp` process started from the project `opencode.json`. When a live daemon owns `.beacon/connection.json`, MCP forwards control calls to its loopback API instead of applying them locally (`cmd/beacon/mcp.go:35-38`, `cmd/beacon/commands.go:89-101`). Without a live daemon, `beacon mcp` tries to take `beacon.lock` and run a reconciler in the same process (`cmd/beacon/mcp.go:39-60`). If another process holds the lock, the failure is logged and written to `.beacon/daemon-error.json` with kind `lock`, and the server keeps serving tools without a reconciler (`cmd/beacon/mcp.go:39-42`, `cmd/beacon/lock.go:242-248`). Runs then sit in `waiting_ci` until the lock owner resumes reconciliation.

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

The OpenCode adapter talks to the running OpenCode background server. Set `harness.endpoint` for it. Authentication uses either a bearer token or `user:password` in `harness.token`, or the variable named by `harness.token_env`. The model resolves in this order: `BEACON_MODEL`, `harness.model`, then the model of the orchestrator session named in `harness.parent_session_id`. A model from the config or the environment carries `harness.provider`, or `opencode` when that field is empty. A model from the parent session carries the provider the server reports. The ACP adapter launches the executable in `harness.command` over stdio and ignores `harness.endpoint`.

`BEACON_MODEL` overrides `harness.model`. When both are set, config load logs a warning with both values so a worker does not silently use a model the config does not show. Do not remove the Beacon environment variables: the pooled MCP daemon uses `BEACON_PROJECT_ROOT` to pin the repository root.

### Timeouts

| Field | Default | Effect |
| --- | --- | --- |
| `orphan_timeout` | `1m30s` | How long a running run without a PR may go stale before its worker session is checked for liveness. Raise it when PR creation is slow. A dead session keeps its slot until the timeout expires. |
| `model_timeout` | `3s` | How long the parent-session model lookup may take. A timeout logs a warning with the provider and the parent session, then workers inherit the server default. |

Both fields accept any value `time.ParseDuration` understands. Config load rejects a malformed value instead of substituting a default.

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
| `mcpDeps` | object | `{}` | Helpers such as `env` and `existsSync` used to locate the binary; no build is started (`.opencode/plugins/beacon-project/mcp/mcp.js:12-30`, `.opencode/plugins/beacon-project/mcp/mcp.js:86-107`). |
| `noopMcpRegistration` | function | built in | Replaces the no-op registration used in worker sessions. |

The built-in shell guard blocks recursive grep, root find, direct pushes to `main`, and full builds, test suites, vet runs, or lint runs. It applies to the `bash`, `shell`, and `execute` tools for agents in `shellGuardAgents`, and to events with no agent. Worker sessions receive the worker rules. Orchestrator sessions receive rules about narrow task scope, board checks before dispatch, and explicit binary rebuilds after Go changes on `main`.

## Loopback API

The monitor reaches the daemon on loopback only. The endpoint defaults to `http://127.0.0.1:7432` and is set with `--endpoint`. The token comes from `--token` or `BEACON_API_TOKEN` and is sent as `Authorization: Bearer <token>` (`ui/tui/main.go:18`). `beacon serve` generates a new token on every start and writes it with the address and PID to `.beacon/connection.json` (`cmd/beacon/lock.go:108-120`, `cmd/beacon/serve.go:39-44`). Use the current value after each restart.

| Route | Wire shape |
| --- | --- |
| `GET /api/v1/runs` | `{"runs":[Run]}` or a bare `[Run]` array. |
| `GET /api/v1/events` | `text/event-stream` frames named `harness`, `pr_changed`, and `ping`, each with a JSON data payload. |
| `POST /api/v1/control` | `{"action":"approve\|unblock\|cancel\|retry\|nudge\|watch","run_id":"...","sha":"..."}`. The handler rejects any verb outside the six in `pkg/core/control.go:8-26` (`cmd/beacon/http.go:411-424`). |

A `Run` carries `id`, `task_id`, `state`, `pr`, `head_sha`, `branch`, `reason` and `updated_at`. The TUI reads each frame into an `Event` with `id`, `type`, `run_id` and `at`, using the frame name when the payload has no `type`. Treat events as notifications. After any event, re-read `/api/v1/runs` instead of treating the event payload as state.

## Stuck run triage

Work through these steps in order when a run stops moving. Stop at the first step that explains the pause.

1. `beacon status --snapshot --recent-limit 5 --decisions-limit 20` returns active runs, bounded history, metric totals and the daemon snapshot in one JSON payload (`cmd/beacon/commands.go:156-165`, `cmd/beacon/commands.go:70-81`). Read each run's `state` and `reason` and the decision records before acting.
2. `beacon doctor` checks the git repo, config, agents, MCP entry, configured command, binary freshness, daemon owner, TUI peers, operator agent and endpoints. It prints one `[OK]`, `[WARN]` or `[FAIL]` line per check and exits 1 on a `FAIL` (`cmd/beacon/doctor.go`, `GLOSSARY.md` term Doctor). Fix every `FAIL` before dispatching. The preflight lines carry the fix on the same line.
3. `beacon status --daemon` reports the daemon's `state`, PID, heartbeat age and any recorded fatal error (`cmd/beacon/commands.go:138-150`, `cmd/beacon/lock.go:250-282`). A `dead`, `stale`, `invalid` or `unreadable` state means no reconciler is advancing runs; follow the restart procedure in Daemon ownership and restart above.
4. `beacon_reconcile {"pr": 42}` forces one reconciler tick for that PR and returns the verdict, reason, criterion, state and error (`internal/infra/mcp/server.go:365-381`, `internal/infra/mcp/registry.go:49`). Errors and panics are captured in the result, so the daemon loop keeps running. This tool is orchestrator only.

After a stale worktree conflict, inspect the directory and use `beacon_control {"action":"retry","run_id":"..."}` to re-arm a blocked or closed run rather than dispatching the task again (`internal/app/control.go:288-315`, `pkg/core/run.go:334-342`).

### Preflight for tools and the panel

Run `beacon doctor` from the project root when the ten tools or the status panel do not appear. The preflight lines name the prerequisite and put the manual fix on the same line. A failed prerequisite that blocks tool or panel loading exits 1.

| Prerequisite | What doctor checks | Fix named on failure |
| --- | --- | --- |
| MCP config source | `mcp.beacon` or `mcp.servers.beacon` in the root or parent `opencode.json` | Run `beacon init` or add the pooled beacon server entry. The plugin does not register tools. |
| Configured command | The executable in the active MCP entry, including a `sh -c` wrapper, its `exec` token and its `cwd` | Run `bash scripts/build-beacon.sh`, set `command` to `.opencode/bin/beacon mcp`, then restart the MCP daemon and the TUI. |
| Binary freshness | `.opencode/bin/beacon.build-sha` against the current hash of the Go sources and module files | Run `bash scripts/build-beacon.sh`, then restart the MCP daemon and the TUI. |
| Daemon owner | The PID in `daemon.json` and the message in `daemon-error.json` | Read the recorded error, remove the stale record, then start `beacon serve`. |
| TUI peers | The `supported-cli.json` pin against plugin `peerDependencies`, local peer copies and the installed CLI version | Install the pinned OpenCode CLI, restore the pinned peers, and remove plugin `node_modules` peer copies. |
| Operator agent | The `build` and `orchestrator` agents and their generated `beacon_*` permissions | Run `beacon init`, then `go generate ./...`. |

Doctor reads these records. It does not rebuild the binary, start a daemon, or change plugin imports. The MCP entry can live in the parent workspace `opencode.json`, since the project plugin only records a config and never attaches or reloads the server.

## Verify

CI runs on pushes to `main` and `refactor/clean-core` and on pull requests that are opened, synchronized, reopened or labeled. Both triggers ignore `docs/**` and `*.md` except `AGENTS.md`; scheduled nightly and manual events enable every path (`.github/workflows/ci.yml:3-18`, `.github/workflows/ci.yml:47-93`). A shared path filter gates each job, so a docs-only change that does not touch `AGENTS.md` runs no build or test job. Ordinary PRs get the `ready-to-merge` label automatically unless they carry `perf` (`.github/workflows/ci.yml:95-116`).

`verify` builds every module, checks format and lint, and checks the plugin (`.github/workflows/ci.yml:118-189`):

```sh
go build ./...
(cd harnesses/acp && go build ./...)
(cd harnesses/opencode && go build ./...)
(cd ui/tui && go build ./...)
(cd harnesses/acp && go vet ./...)
(cd harnesses/opencode && go vet ./...)
(cd ui/tui && go vet ./...)
test -z "$(gofmt -l cmd internal pkg harnesses ui arch_test.go)"
```

Root `go vet` runs inside golangci-lint v2.15. `verify` also runs `node --check` on four plugin files and runs the plugin tests with `node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js --test ".opencode/plugins/beacon-project/**/*_test.js"` (`.github/workflows/ci.yml:159-189`).

`test-modules` runs each module suite with `-shuffle=on`, adding `-race` for the command, app, infra, bench, OpenCode harness and TUI legs (`.github/workflows/ci.yml:215-322`):

```sh
go test -race -shuffle=on ./cmd/... ./internal/app/... ./internal/infra/... ./internal/bench/...
go test -v -shuffle=on .
go test -shuffle=on ./pkg/...
(cd harnesses/acp && go test -shuffle=on ./...)
(cd harnesses/opencode && go test -race -shuffle=on ./...)
(cd ui/tui && go test -race -shuffle=on ./...)
```

The plugin entry modules import `Plugin` from `@opencode/plugin` and `@opencode/plugin/tui` so the host supplies one OpenTUI and Solid instance. Installing those peers into the plugin directory creates a second Solid context and fails with `No renderer found`. The doubled import command above resolves them to local doubles.

`ADVISORY:` lines from the root dot test leg become GitHub warnings in that same leg; no job runs `scripts/ci-advisory.sh` (`.github/workflows/ci.yml:296-315`). A `perf`-labeled PR gets an advisory two-tree comparison that never gates merge (`.github/workflows/ci.yml:366-375`).

## Notes and decisions

Some Beacon behavior is surprising until you know the reason. `docs/decisions/` records those cases, what caused them, and the options that were considered. Start with [parent session notification delivery](docs/decisions/0001-parent-notify-delivery.md) if a merge notice reaches the chat late. Read [the first-render empty queue](docs/decisions/0003-plugin-first-render.md) if the sidebar shows no cards right after an OpenCode restart. See [second harness support](docs/decisions/0002-second-harness.md) for the capability ports and the ACP adapter that made a second harness possible.
