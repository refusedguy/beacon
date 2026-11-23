# AGENTS.md

Beacon turns task cards into pull requests with parallel coding agents. This file is the contract for work in this repository.

## Architecture

Dependencies point one way:

1. `pkg/core` holds domain types and ports. It imports only the standard library.
2. `internal/app` holds services and use cases. It depends on `pkg/core`.
3. `internal/infra` holds adapters: config, harness, mcp, notify, planner, policy, scm, sqlite, tasksource, workspace. It depends on `internal/app` and `pkg/core`.
4. `cmd/beacon` is the Cobra composition root. It wires services and adapters.

An inner package never imports an outer one. The 350-line production-file limit is advisory: `arch_test.go` logs an `ADVISORY` line instead of failing. Treat it as a prompt to decompose the file. `arch_test.go` enforces cyclomatic complexity of at most 20 per function, with two functions grandfathered pending decomposition.

Two modules sit outside the root module in `go.work`: `harnesses/opencode` is the agent harness adapter, and `ui/tui` is the operator monitor. `ui/tui` imports no Beacon package. It reaches the daemon through loopback HTTP and SSE only.

## Loopback API

The monitor uses three routes:

| Route | Wire shape |
| --- | --- |
| `GET /api/v1/runs` | `{"runs":[Run]}` or a bare `[Run]` array |
| `GET /api/v1/events` | `text/event-stream` frames with a JSON `Event` payload |
| `POST /api/v1/control` | `{"action":"approve\|unblock\|cancel\|retry","run_id":"...","sha":"..."}` |

`Run` carries `id`, `task_id`, `state`, `pr`, `head_sha`, `branch`, `reason` and `updated_at`. `Event` carries `id`, `type`, `run_id` and `at`. Events are notifications. After any event the client re-reads `/api/v1/runs` instead of treating event payloads as state.

The endpoint defaults to `http://127.0.0.1:7432` and is set with `--endpoint`. The token comes from `--token` or `BEACON_API_TOKEN` and is sent as `Authorization: Bearer <token>`. Only loopback endpoints are accepted.

## MCP tools

`beacon mcp` serves five worker tools and two orchestrator tools over stdio:

| Tool | Purpose | Agent |
| --- | --- | --- |
| `beacon_dispatch` | Pick up to N non-overlapping tasks and start runs. | worker |
| `beacon_watch_pr` | Bind a run ID to a pull request number. | worker |
| `beacon_status` | List active runs, pull requests and blockers. | worker |
| `beacon_wait` | Wait for a run to reach a verdict or a terminal state. | worker |
| `beacon_control` | Approve, unblock, cancel or retry a run. | worker |
| `beacon_task_create` | Create a task card and publish it for dispatch. | orchestrator only |
| `beacon_task_delete` | `{"ids": ["T-...", ...]}` deletes several task cards in one commit and push. | orchestrator only |

`opencode.json` tool permissions are generated, not hand-edited. `ToolCatalog()` in `internal/infra/mcp/registry.go` lists every tool and marks the orchestrator-only ones. Run `go generate ./...` after changing the catalog. `cmd/beacon-gen` rewrites only the permission entries it owns and keeps every other config key, agent description, mode and custom permission as they were. Generation happens at development time, never at binary startup. `TestPermissionsUpToDate` in `internal/infra/mcp/permissions_test.go` regenerates the file into a temp copy and fails when the committed `opencode.json` differs, so CI catches a stale permission list.

No other `beacon_*` MCP tool exists. The OpenCode plugin starts the server from `.opencode/bin/beacon` when that binary exists. Before each start it compares `.opencode/bin/beacon.build-sha` with `git rev-parse HEAD` and runs `scripts/build-beacon.sh` when the sha file is missing or stale. The build script writes the current HEAD into `beacon.build-sha`, so the tool list comes from current source rather than a stale binary. The plugin polls HEAD in the project root every few seconds and reloads MCP when it changes. A reload re-resolves the command, so a change on main rebuilds a stale binary before the daemon restarts. No manual rebuild or MCP reconnect is needed after a main change. When no local binary exists, the plugin falls back to `bin/beacon`, a binary on `PATH`, or `go run ./cmd/beacon mcp`. `opencode.json` denies `beacon_task_create` and `beacon_task_delete` globally and allows them only for the `orchestrator` agent, with a temporary allow for `build`. Workers do not inherit those allows.

Spawn workers with `harness.command: worker` in `.beacon/config.yaml`. The OpenCode harness passes that value as the session agent, so each worker inherits the global deny rules for `beacon_task_create` and `beacon_task_delete`. A worker must never create or delete task cards. It reports new work to the orchestrator instead.

## Task cards

Cards live in `docs/tasks/`. A card uses YAML frontmatter followed by titled sections. The file `Scope:` names every path the task may change, and `Depends:` names the cards that must land first.

```
---
id: T-123
status: todo
type: task
---
# T-123. Short title

Scope: pkg/core/run.go, pkg/core/run_test.go
Depends: T-120

Goal: one sentence describing the outcome.

Context: the fact or source that motivates the change.

Steps: the ordered work.

Check: the measurement that proves the outcome.

Accept: the boundary that must not change.
```

Edit only files in `Scope:`. Do not edit files owned by another card. Do not edit another card's status by hand.

## Workflow

1. Read the card and check `Depends:`.
2. Work in your own worktree and branch. Never push directly to `main`.
3. Format changed Go files with `gofmt -w`.
4. Run `gofmt -w` on changed Go files and targeted `go test` for the changed packages. On weak machines leave the full `go test ./...`, `go vet ./...` and `go build ./...` run to CI.
5. Open a pull request, then call `beacon_watch_pr` with the run ID and PR number.
6. Use `beacon_wait` to wait for a verdict and `beacon_status` to inspect state.
7. Use `beacon_control` for approve, unblock, cancel and retry actions.

## Shell guards

The OpenCode plugin rejects three commands at the tool boundary: `grep -r` over the whole tree, `find /` and a direct push to `main`. Use `rg -n PATTERN <explicit-directory>` instead of recursive grep or root find.

## Prose

Write plain prose. Use active voice, short sentences and one idea per sentence. No em dashes, filler phrases or rule of three. Apply the same rules to docs, PR bodies and commit messages.
