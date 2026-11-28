# Perf status

As of 2026-11-28, HEAD `ea40bd1f`. Operator-owned. Refresh date and HEAD with the last change of each session, in the same commit. The handoff at `/home/nbook/tmp/handoff-beacon-perf-full.md` is at `541a55ee` and behind; its pass-order and ci.yml-owner snapshots conflict. Live ci.yml, `beacon status`, `beacon_recent`, `gh pr view` and `git log` win: pass order `B A A B B A A B B A` (five per side), ci.yml owner T-4536fbaa in #2851, not T-b186b563. `cards.md` wave states are stale.

CI owns every number. Store pointers, not medians. `docs/perf/dossier.md` and local harness numbers are historical and invalid as evidence. Known: advisory green and non-gating, 15 keys, baseline unchanged, false `card-parse regression` gone. Unknown: current medians, five-pass spread rule, `json-control` usable, calibrated nightly floor.

## Measured

| PR | CI run | Variant | Evidence |
| --- | --- | --- | --- |
| #2722 | 45947551633 | kept, planner-select gain | advisory comment |
| #2723 | 45947556674 | reverted, inconclusive | advisory comment |
| #2732 | 45947591004 | reverted, mcp-journal regression | advisory comment |
| #2786 | 45973256317 | kept open, 15 rows inconclusive | artifact `perf-compare-2786-<sha>` |

The other perf2 PRs (#2726 to #2731) were reverted; their advisory comments carry the verdicts.

## In flight

| Card | PR | Head sha (gh pr view) | Owner | Next action | Gate |
| --- | --- | --- | --- | --- | --- |
| T-8bb8a594 | #2845 | 9cf67fb4 | refusedguy | read comment, await merge | label set, advisory non-gating |
| T-cb7f5069 | #2842 | 50241a3b | refusedguy | read comment, finish variants | `ready-to-merge` |
| T-4536fbaa | #2851 | b9f0c298 | refusedguy | read comment, finish variants | `ready-to-merge`, ci.yml owner |

## Deferred

| Work | Reason | Revisit |
| --- | --- | --- |
| control drift fix | duplicate rows and cache block raw rows | after #2786 posts comparable rows |
| ratios in nightly gate | floors tuned to absolute seconds | only with a required-check card |

## Card scope ownership

A new card may claim at most 4 scope paths, at most 2 hot files, and no path that another non-terminal card already claims. A hot file is a path named by two or more non-terminal cards. `internal/infra/tasksource/card_scope_test.go` walks `docs/tasks`, skips done and dropped cards, and keeps the explicit hot list. The test fails when the corpus disagrees with that list. A card with 5 paths or a shared hot file now fails CI before it can starve dispatch. Cards on the test's legacy list keep their existing scope until their owners split it.

## Constraints

- PR #2786 stays open until the operator says otherwise. It is the measurement instrument.
- Advisory job exits zero and adds no required Criterion.
- One card owns ci.yml at a time.
- No local test, build, vet, lint or benchmark for perf work.
- Fix order: duplicate canonical rows, then cache export, then control drift.
