---
id: T-05d8adbd
title: Enforce one owner per hot file in CI
status: available
scope:
    - internal/infra/tasksource/card_scope_test.go
    - docs/perf/status.md
priority: 1
kind: task
require_review: llm
---

Goal: a new card with an oversized or overlapping Scope fails CI instead of starving dispatch.
Evidence: 16 of 27 available cards have 5 or more scope files; one card has 15. The ci.yml cluster has 11 cards, reconciler.go has 9. 638 of 714 dispatch skips are scope overlaps. T-8b02b25b is a consolidation of five already merged fixes with a 15-file scope. Dispatcher planner skips the whole candidate on one shared path at internal/app/planner.go:110-135.
Steps: add a corpus test in package tasksource that walks docs/tasks, skips done and dropped, and asserts non-empty scope, at most 4 paths, no path claimed by two non-terminal cards, and at most 2 hot files per card; keep an explicit hot list in the test and fail when the corpus disagrees; document the rule in docs/perf/status.md.
Accept: adding a card with 5 files or a shared hot file fails a CI test before it can block dispatch.
