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

`beacon mcp` serves six worker and read-only tools plus five orchestrator tools over stdio:

| Tool | Purpose | Agent |
| --- | --- | --- |
| `beacon_dispatch` | Pick up to N non-overlapping tasks and start runs. | worker |
| `beacon_watch_pr` | Bind a run ID to a pull request number. | worker |
| `beacon_status` | List active runs, pull requests and blockers. | worker |
| `beacon_recent` | List the most recently finished runs. | worker |
| `beacon_wait` | Wait for a run to reach a verdict or a terminal state. | worker |
| `beacon_control` | Approve, unblock, cancel, retry, nudge or watch a run. Pass `run_ids` instead of `run_id` to apply one verb to several runs and get one result per id. The vocabulary is `pkg/core/control.go:8-26`; the batch form is `internal/infra/mcp/tool_control.go:12-32` and `internal/infra/mcp/tool_control.go:67-86`. | worker |
| `beacon_task_create` | `{"title": "...", ...}` creates one card; `{"tasks": [{"title": "...", "depends": ["@1"], ...}, ...]}` creates several and returns `{"created": [{"id", "path"}], "pushed": true}`. `@N` in `depends` means the Nth card in the batch (1-based) and must name an earlier card. | orchestrator only |
| `beacon_task_delete` | `{"ids": ["T-...", ...]}` deletes several task cards in one commit and push. | orchestrator only |
| `beacon_task_update` | `{"updates": [{"id": "T-...", "title": "...", ...}]}` edits several task cards in one commit and push. Omitted fields stay unchanged and `status` is ignored. | orchestrator only |
| `beacon_task_close` | `{"updates": [{"id": "T-...", "status": "done\|dropped", "reason": "..."}], "operation_id": "..."}` closes several cards in one commit and push. It writes `status` and `status_reason` into frontmatter, refuses any card with an active run and returns `{"results": [{"id", "status"}], "refused": [...], "pushed": true}`. Replaying the same `operation_id` returns the stored result without a second commit. | orchestrator only |
| `beacon_reconcile` | `{"pr": 42}` forces one reconciler tick and returns `{"pr", "decision": {"verdict", "reason", "criterion"}, "state", "error"}`. Errors and panics are captured in the result, so the daemon loop keeps running. | orchestrator only |

`opencode.json` tool permissions are generated, not hand-edited. `ToolCatalog()` in `internal/infra/mcp/registry.go` lists every tool and marks the orchestrator-only ones. Run `go generate ./...` after changing the catalog. `cmd/beacon-gen` rewrites only the permission entries it owns and keeps every other config key, agent description, mode and custom permission as they were. Generation happens at development time, never at binary startup. `TestPermissionsUpToDate` in `internal/infra/mcp/permissions_test.go` regenerates the file into a temp copy and fails when the committed `opencode.json` differs, so CI catches a stale permission list.

No other `beacon_*` MCP tool exists. The MCP server runs as one pooled `beacon mcp` process started from the project `opencode.json`. The local plugin only resolves a command path: `BEACON_BIN` when it exists, then `.opencode/bin/beacon`, `bin/beacon`, a binary on `PATH`, and finally `go run ./cmd/beacon mcp` when `cmd/beacon/main.go` exists (`.opencode/plugins/beacon-project/mcp/mcp.js:86-107`). It never reads build inputs, never starts a build and never rebuilds a stale binary. There is no HEAD watcher and no automatic `ctx.mcp.reload()`. After Go code lands on `main`, run `bash scripts/build-beacon.sh` explicitly, then restart the MCP daemon and the OpenCode TUI; the plugin emits that reminder from `.opencode/plugins/beacon-project/rules/context.js:8`. A stale local binary keeps serving until that explicit rebuild. `beacon status --snapshot` bounds its decision history; `--recent-limit` and `--decisions-limit` tighten it further without a rebuild. `opencode.json` denies `beacon_task_create`, `beacon_task_delete`, `beacon_task_update`, `beacon_task_close` and `beacon_reconcile` globally and allows them only for the `orchestrator` agent, with a temporary allow for `build`. Workers do not inherit those allows.

Spawn workers with `harness.command: worker` in `.beacon/config.yaml`. The OpenCode harness passes that value as the session agent, so each worker inherits the global deny rules for `beacon_task_create`, `beacon_task_delete`, `beacon_task_update`, `beacon_task_close` and `beacon_reconcile`. A worker must never create, delete, update or close task cards, and must never force a reconcile. It reports new work to the orchestrator instead.

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

## Orchestrator practice

The operator works in the default `build` agent. That agent gets orchestrator rules from the plugin. Keep the main checkout clean and let workers own their worktrees.

- Do not dispatch a task again after `workspace conflict` or `dirty worktree` until you find the cause. A repeated dispatch does not clear a stale worktree. Diagnose in this order: `beacon status --snapshot` for the run's `state` and `reason`, `beacon status --daemon` for a missing reconciler (`cmd/beacon/lock.go:250-282`), `git status --short` inside `.beacon/worktrees/<id>` for uncommitted files (`pkg/core/errors.go:51-54`, `internal/infra/workspace/git_worktree.go:468-482`). Clear or preserve that work explicitly, then re-arm the blocked or closed run with `beacon_control {"action":"retry","run_id":"..."}` (`internal/app/control.go:288-315`).
- Before `beacon_task_create`, check `git log -n 10 --oneline`, `beacon_status` and `beacon_recent`. Do not recreate a card for work that is in flight or already merged.
- Give each card a narrow, disjoint `Scope`. Never run two cards that touch `ui.js`, `ui_view.js` or `reconciler.go` at the same time.
- After Go code lands on `main`, run `bash scripts/build-beacon.sh`, then tell the operator to restart the MCP daemon and the OpenCode TUI once. Go code is not hot-reloaded.

## Shell guards

The OpenCode plugin rejects three commands at the tool boundary: `grep -r` over the whole tree, `find /` and a direct push to `main`. Use `rg -n PATTERN <explicit-directory>` instead of recursive grep or root find.

## Prose

Write plain prose. Use active voice, short sentences and one idea per sentence. No em dashes, filler phrases or rule of three. Apply the same rules to docs, PR bodies and commit messages.

## Conventions

Use the standard library `testing` package for all tests. Do not add GoConvey or another third-party testing framework. Existing tests use `testing`, and the module declares no GoConvey dependency.

Do not put copyright or license headers in Go files. The repository has no Genome Research Ltd header and no other copyright or SPDX header. Do not add one to new files.
