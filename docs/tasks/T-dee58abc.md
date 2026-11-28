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

Goal: reject oversized cards before they enter dispatch.
Evidence: 113 pairs of available cards overlap on at least one path. ci.yml and reconciler.go are each named by 9 cards. T-8b02b25b had 15 files and 8273 lines. AGENTS.md:104 only asks for narrow scope; planner.go checks overlap but never size.
Steps: add a 3-2-1200 cap in TaskCreator.Create, where 3 is total scope paths, 2 is production files excluding tests and data, and 1200 is summed line count; add a skip code beside SkipNoScope in planner.go and reject oversize hand-edited cards in GreedyPlanner.Select; replace the ad hoc hot-file list in AGENTS.md with the cap and a singleton rule for files longer than 600 lines or named by 3 or more nonterminal cards; add tests for rejection and for a passing narrow card.
Accept: a card with 4 paths, 3 production files or more than 1200 scoped lines fails creation and cannot be dispatched.
