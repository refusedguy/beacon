# GLOSSARY

Beacon uses these terms in code, cards and reports. Each term has one meaning.

## Task

One unit of work in `docs/tasks/`. A task has an ID, title, body, priority, scope, dependencies and status. A task is the input to dispatch and the unit that a work run executes.

## Scope

A predicate over file paths. A scope names a file, a directory subtree, a glob or all files. Dispatch uses scopes to pick tasks that do not overlap, and a work run uses its task's scope to decide which files it may change.

## WorkRun

One execution of a task. A work run owns a worktree, a branch, a harness session, attempt counters and at most one pull request. Its state moves from pending through running and waiting states to merged or closed.

## Verdict

The outcome of the merge decision chain for a work run: `wait`, `merge`, `needs_rebase`, `needs_agent_fix`, `blocked` or `noop`. A verdict describes what happens next, not what has already happened.

## Criterion

One criterion in the merge decision chain. A criterion has a `Name()` and an `Evaluate(Observation)` method. `Evaluate` returns `VerdictNoop` when the criterion passes and a stopping verdict when it fails. `Decide` applies the invariants first, then the criteria in order, and returns a merge verdict only when every criterion passes. `MergeDecision.Criterion` names the criterion that stopped the chain.

## ActionDispatch

The reconcile handler a verdict selects: `noop`, `merge`, `rebase`, `fix`, `block` or `pending`. `DecisionAction.Dispatch` carries this value and `applyDecision` switches on it.

## ActionCounter

The metric a decision category increments: `dispatch_cycles`, `skip`, `dispatch`, `rebase` or `fix`. `DecisionAction.Counter` carries this value.

## DecisionAction

One row of the verdict and decision-kind table in `pkg/core/verdict.go`. It carries the `Label` written to decision records, the `ActionDispatch`, the `ActionCounter` and a `Qualifier` that gates a counter on a result such as `rebased`. `ActionForVerdict` and `ActionForKind` return the row for a verdict or a decision kind.

## DecisionKind

The string key of a decision category in the verdict and decision-kind table. Verdict strings and kind strings share the table. Kind names include `dispatch.candidates`, `dispatch.skip`, `transition.spawn`, `transition.rebase` and `transition.wake`. A decision kind names a decision record; it is not a verdict. `ActionForKind` maps a kind to its `DecisionAction`.

## BaseIncident

A recorded failure of checks on the base branch. An incident holds a signature of the failed checks, the runs it blocks, a fix attempt counter and a status of open or resolved. While an incident is open, merges stay blocked.

## AgentHarness

The adapter that starts, steers and stops agent sessions. The harness turns a work run into a session in the agent's own environment and reports session events back to the controller. `harnesses/opencode` is the current implementation.

## TaskSource

The adapter that supplies tasks. A task source lists available cards, reports their status and accepts task updates. GitHub issues and GitLab issues are task sources.

## ScmGateway

The adapter for source control operations. A gateway reads pull requests and checks, posts comments, rebases branches and merges pull requests. GitHub and GitLab gateways implement the same port.

## FixerRun

A work run created to repair a base incident. A fixer run carries the incident ID, works in its own worktree and opens its own pull request. When that pull request merges, the incident is resolved and blocked runs resume.

## Doctor

The `beacon doctor` setup check. It validates the git repo, config, agents, MCP server entry, binary, and endpoints. It prints one `[OK]`, `[WARN]`, or `[FAIL]` line per check and exits 1 when any check fails.

## InitWizard

The `beacon init` command. It detects the git remote, base branch, and project stack. It writes `.beacon/config.yaml`, the task directory, `.gitignore` entries, `opencode.json` permissions, and the project plugin so Beacon runs in any git project.

## ToolCatalog

The declarative list of MCP tools in `internal/infra/mcp/registry.go`. It names every tool and marks the orchestrator-only ones. `go generate` derives the `opencode.json` permissions from it.

## QueueTask

A local task card with its unresolved dependencies, returned by `QueueStore.ListQueueTasks`. It carries the card ID, title, priority and dependencies. `Waiting` marks a card whose dependencies are not finished. `makeQueue` maps a queue task into a `QueueItemSnapshot`.

## QueueItemSnapshot

The queue section of the status snapshot. It carries the card ID, title, priority, status and dependencies. `makeQueue` derives `status` from `QueueTask.Waiting`: `waiting_deps` when dependencies are unresolved, `ready` otherwise.

## FixPrompt

The prompt given to a fixer run. It prepends the worker instructions, states the red base branch and its failed checks, then asks the agent to fix the base branch and open a PR.

## Zero-value BaseIncident

The convention that the status snapshot always carries a `base_incident` object. When no incident is open, the object has an empty ID and no failed checks. Consumers use the empty ID to detect that case.
