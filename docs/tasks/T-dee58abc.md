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
Goal: reject oversized cards before they enter dispatch, including children inside epics.
Evidence: 113 pairs of available cards overlap on at least one path. ci.yml and reconciler.go are each named by 9 cards. T-8b02b25b had 15 files and 8273 lines. AGENTS.md:104 only asks for narrow scope; planner.go checks overlap but never size. An epic stores its own scope separately from each plan child at internal/app/taskcreate.go:169-172, and core.ExpandChildren turns children into independent cards at pkg/core/task_rules.go:152-170, so a summed epic cap would wrongly reject a valid epic with several narrow children.
Steps: add a 3-2-1200 cap of at most 3 scope paths, 2 production files and 1200 summed lines; apply it per card in the expanded graph after epicChildren and before writer.Write, so it covers the epic own scope and every child scope independently, never their aggregate; add a skip code beside SkipNoScope in planner.go as a runtime backstop for hand-edited cards; expand directory and glob scopes to concrete files before counting production files and lines; replace the ad hoc hot-file list in AGENTS.md with the cap and a singleton rule for files longer than 600 lines or named by 3 or more nonterminal cards; add tests for rejection of a standalone oversize card, rejection of an oversize child, and acceptance of an epic with three narrow children.
Accept: a card with 4 paths, 3 production files or more than 1200 scoped lines fails creation, while an epic with three valid children and no epic-level scope passes.