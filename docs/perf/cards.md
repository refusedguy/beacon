# Performance cards

Performance cards carry `perf: true` in their frontmatter and a `[perf]` prefix in the title. The prefix stays visible in the TUI queue even when the sidebar truncates the rest of the title.

Find them with:

```
rg -l '^perf: true' docs/tasks
rg -n '^title: \[perf\]' docs/tasks
```

Workers add the `perf` label to their pull request. They withhold `ready-to-merge` until every variant is tried. The advisory job posts `<!-- beacon-pr-perf -->` comments with medians, ranges, work, errors and limiter.

## Wave 1

| Card | Title | Scope | State |
| --- | --- | --- | --- |
| T-de0058a2 | JS sidebar read and grouping allocation cut | `ui_model.js`, `ui_alerts.js` and tests | running |
| T-5d11af6c | Planner allocation cut after dispatch merge | `internal/app/planner.go` and tests | PR #2741, waiting CI |
| T-b8fc3701 | Reconcile tick reads and fanout cut | `internal/app/reconciler.go`, `finalize.go` and tests | running |
| T-364fbf77 | Snapshot decode and assembly fast path | `internal/infra/sqlite/store.go`, `internal/app/status.go` and tests | PR #2743, waiting CI |
| T-d42d4241 | Task sync and dispatch launch cut | `internal/infra/sqlite/tasks.go`, `internal/app/dispatcher.go` and tests | running |

## Wave 2

Wave 2 cards are not created yet. Planned scopes: `cmd/beacon/http.go` and `ui/tui/model.go`, then `internal/infra/tasksource/parse.go` and `.github/workflows/ci.yml`.

## Rules

A variant stays only when work counts match, errors are zero, a limiter is named, ranges do not overlap and the head median is strictly better. An inconclusive or slower variant is reverted with `git reset --hard HEAD~1` and a forced push.
