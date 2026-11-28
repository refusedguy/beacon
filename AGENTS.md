# AGENTS.md

Beacon turns task cards into pull requests with parallel coding agents. This file is the contract for work in this repository.

## Architecture

Dependencies point one way:

1. `pkg/core` holds domain types and ports. It imports only the standard library.
2. `internal/app` holds services and use cases. It depends on `pkg/core`.
3. `internal/infra` holds adapters: config, harness, mcp, notify, planner, policy, scm, sqlite, tasksource, workspace. It depends on `internal/app` and `pkg/core`.
4. `cmd/beacon` is the Cobra composition root. It wires services and adapters.

An inner package never imports an outer one. The 350-line production-file limit is advisory: `arch_test.go` logs an `ADVISORY` line instead of failing. Treat it as a prompt to decompose the file. `arch_test.go` enforces cyclomatic complexity of at most 20 per function, with two functions grandfathered pending decomposition.

Three modules sit outside the root module in `go.work`: `harnesses/opencode` and `harnesses/acp` are agent harness adapters, and `ui/tui` is the operator monitor (`go.work:3-8`). `ui/tui` imports no Beacon package. It reaches the daemon through loopback HTTP and SSE only.

## Vocabulary

`GLOSSARY.md` defines the names used in cards, code and reports, including the state-to-action table for the eight run states in `pkg/core/run.go:15-32` and their transitions in `pkg/core/run.go:117-124`. Use those exact names in prose. A `Criterion` is one merge check, not a "merge rule". A `DecisionKind` names a decision record; a `Verdict` names a merge outcome. A `DecisionAction` ties a verdict or kind to its `ActionDispatch` and `ActionCounter`. `Run.NoteObservation` is the one place that stamps a run's head, base and update time from an observation.

## Loopback API

The monitor uses three routes:

| Route | Wire shape |
| --- | --- |
| `GET /api/v1/runs` | `{"runs":[Run]}` or a bare `[Run]` array |
| `GET /api/v1/events` | `text/event-stream` frames with a JSON `Event` payload |
| `POST /api/v1/control` | `{"action":"approve\|unblock\|cancel\|retry\|nudge\|watch","run_id":"...","sha":"..."}` |

`Run` carries `id`, `task_id`, `state`, `pr`, `head_sha`, `branch`, `reason` and `updated_at`. `Event` carries `id`, `type`, `run_id` and `at`. Events are notifications. After any event the client re-reads `/api/v1/runs` instead of treating event payloads as state.

The endpoint defaults to `http://127.0.0.1:7432` and is set with `--endpoint`. The token comes from `--token` or `BEACON_API_TOKEN` and is sent as `Authorization: Bearer <token>` (`ui/tui/main.go:18`). Only loopback endpoints are accepted. `beacon serve` generates a fresh token on every start and writes the address, token and PID to `.beacon/connection.json` (`cmd/beacon/lock.go:108-120`, `cmd/beacon/serve.go:39-44`). Read the current value after each restart. The six control verbs come from `pkg/core/control.go:8-26` and the handler rejects any other value (`cmd/beacon/http.go:411-424`).

## MCP tools

<!-- BEGIN BEACON DOCS: agents-tools -->
`beacon mcp` serves 12 tools over stdio: 6 worker tools and 6 orchestrator-only tools. `ToolCatalog()` in `internal/infra/mcp/registry.go` declares them.

| Tool | Purpose | Agent |
| --- | --- | --- |
| `beacon_dispatch` | Pick up to N non-overlapping tasks and start agents. | worker |
| `beacon_watch_pr` | Bind a pull request to a running task. | worker |
| `beacon_status` | List active runs, PRs and blockers. | worker |
| `beacon_recent` | List the most recently finished runs. | worker |
| `beacon_wait` | Wait for a run to reach an active verdict or a terminal state. | worker |
| `beacon_control` | Control a run: approve, unblock, cancel, retry, nudge or watch. | worker |
| `beacon_task_create` | Create and publish a task card. Orchestrator only. | orchestrator only |
| `beacon_task_delete` | Delete task cards in one commit and push. Orchestrator only. | orchestrator only |
| `beacon_task_update` | Update task cards in one commit and push. Orchestrator only. | orchestrator only |
| `beacon_task_close` | Close task cards with a status and reason in one commit and push. Orchestrator only. | orchestrator only |
| `beacon_task_start` | Start one specific task by ID. Orchestrator only. | orchestrator only |
| `beacon_reconcile` | Force one reconciler tick for a PR and report its decision. Orchestrator only. | orchestrator only |
<!-- END BEACON DOCS: agents-tools -->

`opencode.json` tool permissions are generated, not hand-edited. `ToolCatalog()` in `internal/infra/mcp/registry.go` lists every tool and marks the orchestrator-only ones. Run `go generate ./...` after changing the catalog. `cmd/beacon-gen` rewrites only the permission entries it owns and keeps every other config key, agent description, mode and custom permission as they were. Generation happens at development time, never at binary startup. `TestPermissionsUpToDate` in `internal/infra/mcp/permissions_test.go` regenerates the file into a temp copy and fails when the committed `opencode.json` differs, so CI catches a stale permission list.

No other `beacon_*` MCP tool exists. The MCP server runs as one pooled `beacon mcp` process started from the project `opencode.json`. The local plugin only resolves a command path: `BEACON_BIN` when it exists, then `.opencode/bin/beacon`, `bin/beacon`, a binary on `PATH`, and finally `go run ./cmd/beacon mcp` when `cmd/beacon/main.go` exists (`.opencode/plugins/beacon-project/mcp/mcp.js:86-107`). It never reads build inputs, never starts a build and never rebuilds a stale binary. There is no HEAD watcher and no automatic `ctx.mcp.reload()`. After Go code lands on `main`, run `bash scripts/build-beacon.sh` explicitly, then restart the MCP daemon and the OpenCode TUI; the plugin emits that reminder from `.opencode/plugins/beacon-project/rules/context.js:8`. A stale local binary keeps serving until that explicit rebuild. `beacon status --snapshot` bounds its decision history; `--recent-limit` and `--decisions-limit` tighten it further without a rebuild. `opencode.json` denies <!-- BEGIN BEACON DOCS: denied-actions -->
`beacon_beacon_reconcile`, `beacon_beacon_task_close`, `beacon_beacon_task_create`, `beacon_beacon_task_delete`, `beacon_beacon_task_start`, `beacon_beacon_task_update`
<!-- END BEACON DOCS: denied-actions --> globally and allows them only for the `orchestrator` agent, with a temporary allow for `build`. Workers do not inherit those allows. OpenCode checks namespaced actions, so every tool name carries the `beacon_beacon_` prefix. The control action <!-- BEGIN BEACON DOCS: control-action -->
`beacon_beacon_control`
<!-- END BEACON DOCS: control-action --> is allowed for `build`, `orchestrator` and `worker`.

Spawn workers with `harness.command: worker` in `.beacon/config.yaml`. The OpenCode harness passes that value as the session agent, so each worker inherits the global deny rules for <!-- BEGIN BEACON DOCS: denied-actions -->
`beacon_beacon_reconcile`, `beacon_beacon_task_close`, `beacon_beacon_task_create`, `beacon_beacon_task_delete`, `beacon_beacon_task_start`, `beacon_beacon_task_update`
<!-- END BEACON DOCS: denied-actions -->. A worker must never create, delete, update or close task cards, and must never force a reconcile. It reports new work to the orchestrator instead.

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
8. Give every encountered failure a disposition before the next dispatch. File a card with evidence or record the failure as declined with a reason. No failure is dropped silently.

## Orchestrator practice

The operator works in the default `build` agent. That agent gets orchestrator rules from the plugin. Keep the main checkout clean and let workers own their worktrees.

- Do not dispatch a task again after `workspace conflict` or `dirty worktree` until you find the cause. A repeated dispatch does not clear a stale worktree. Diagnose in this order: `beacon status --snapshot` for the run's `state` and `reason`, `beacon status --daemon` for a missing reconciler (`cmd/beacon/lock.go:250-282`), `git status --short` inside `.beacon/worktrees/<id>` for uncommitted files (`pkg/core/errors.go:51-54`, `internal/infra/workspace/git_worktree.go:468-482`). Clear or preserve that work explicitly, then re-arm the blocked or closed run with `beacon_control {"action":"retry","run_id":"..."}` (`internal/app/control.go:288-315`).
- Before `beacon_task_create`, check `git log -n 10 --oneline`, `beacon_status` and `beacon_recent`. Do not recreate a card for work that is in flight or already merged.
- Keep every card inside the 3-2-1200 cap: at most 3 `Scope` paths, 2 production files and 1200 summed lines after directories and globs expand to files. The cap applies to an epic and to each plan child separately, never to their sum.
- A hot file such as `internal/app/reconciler.go` or `.github/workflows/ci.yml` is a singleton. One card owns it and lists no other path in its `Scope`. Do not scope that file in another card until the owner merges.
- Use `Depends` only to order a card behind the owner whose output it needs. Do not use `Depends` to serialize peers. Peers take disjoint `Scope` paths and dispatch in parallel once their owner is done.
- After Go code lands on `main`, run `bash scripts/build-beacon.sh`, then tell the operator to restart the MCP daemon and the OpenCode TUI once. Go code is not hot-reloaded.

## Shell guards

The OpenCode plugin rejects three commands at the tool boundary: `grep -r` over the whole tree, `find /` and a direct push to `main`. Use `rg -n PATTERN <explicit-directory>` instead of recursive grep or root find.

## Prose

Write plain prose. Use active voice, short sentences and one idea per sentence. No em dashes, filler phrases or rule of three. Apply the same rules to docs, PR bodies and commit messages.

## Conventions

Use the standard library `testing` package for all tests. Do not add GoConvey or another third-party testing framework. Existing tests use `testing`, and the module declares no GoConvey dependency.

Do not put copyright or license headers in Go files. The repository has no Genome Research Ltd header and no other copyright or SPDX header. Do not add one to new files.
