---
id: T-dee58abc
title: Enforce 3-2-1200 scope cap at card creation
status: available
scope:
    - internal/app/taskcreate.go
    - internal/app/taskcreate_test.go
    - internal/app/planner.go
    - internal/app/planner_test.go
    - AGENTS.md
priority: 1
kind: task
require_review: llm
---
Goal: reject oversized cards before they enter dispatch and keep Depends reserved for real ordering.
Evidence: 113 pairs of available cards overlap on at least one path. ci.yml and reconciler.go are each named by 9 cards. T-8b02b25b had 15 files and 8273 lines. AGENTS.md:104 only asks for narrow scope; planner.go checks overlap but never size. readyTasksQuery at internal/infra/sqlite/tasks.go:368-376 blocks a dependent until every direct owner is done or dropped, while OverlapByDependencies at pkg/core/task.go:305-308 only checks a direct edge, so two siblings on one owner already run in parallel. Several cards use Depends to serialize peers that only share a file, which makes them wait for a full merge without needing the output. An epic stores its own scope separately from each plan child at taskcreate.go:169-172, so a summed epic cap would wrongly reject valid narrow children.
Steps: add a 3-2-1200 cap of at most 3 scope paths, 2 production files and 1200 summed lines; apply it per card in the expanded graph after epicChildren and before writer.Write, covering epic scope and every child scope independently, never their aggregate; add a skip code beside SkipNoScope in planner.go as a runtime backstop; expand directory and glob scopes to concrete files before counting production files and lines; replace the ad hoc hot-file list in AGENTS.md with the cap, the singleton rule for hot files, and the rule that Depends is only for owner-to-dependent ordering while peers use disjoint Scope; add tests for a standalone oversize card, an oversize child, an epic with three narrow children, and two siblings on one owner dispatching in parallel.
Accept: oversize cards fail creation, an epic with three valid children passes, and sibling cards with disjoint scopes are not serialized by Depends.