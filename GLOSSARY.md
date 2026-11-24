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

## BaseIncident

A recorded failure of checks on the base branch. An incident holds a signature of the failed checks, the runs it blocks, a fix attempt counter and a status of open or resolved. While an incident is open, merges stay blocked.

## AgentHarness

The adapter that starts, steers and stops agent sessions. The harness turns a work run into a session in the agent's own environment and reports session events back to the controller. `harnesses/opencode` is the current implementation.

## TaskSource

The adapter that supplies tasks. A task source lists available cards, reports their status and accepts task updates. GitHub issues and GitLab issues are task sources.

## ScmGateway

The adapter for source control operations. A gateway reads pull requests and checks, posts comments, rebases branches and merges pull requests. GitHub and GitLab gateways implement the same port.

## MergeRule

One criterion in the merge decision chain. A rule returns `noop` when it passes and a stopping verdict when it fails. The controller evaluates rules in order and returns a merge verdict only when every rule passes.

## FixerRun

A work run created to repair a base incident. A fixer run carries the incident ID, works in its own worktree and opens its own pull request. When that pull request merges, the incident is resolved and blocked runs resume.

## Doctor

The `beacon doctor` setup check. It validates the git repo, config, agents, MCP server entry, binary, and endpoints. It prints one `[OK]`, `[WARN]`, or `[FAIL]` line per check and exits 1 when any check fails.

## InitWizard

The `beacon init` command. It detects the git remote, base branch, and project stack. It writes `.beacon/config.yaml`, the task directory, `.gitignore` entries, `opencode.json` permissions, and the project plugin so Beacon runs in any git project.

## ToolCatalog

The declarative list of MCP tools in `internal/infra/mcp/registry.go`. It names every tool and marks the orchestrator-only ones. `go generate` derives the `opencode.json` permissions from it.

## QueueTask

A local task card shown in the queue section of the status snapshot. It carries the card ID, title, priority, and unresolved dependencies. `Waiting` marks a card whose dependencies are not finished.

## FixPrompt

The prompt given to a fixer run. It prepends the worker instructions, states the red base branch and its failed checks, then asks the agent to fix the base branch and open a PR.

## Zero-value BaseIncident

The convention that the status snapshot always carries a `base_incident` object. When no incident is open, the object has an empty ID and no failed checks. Consumers use the empty ID to detect that case.
