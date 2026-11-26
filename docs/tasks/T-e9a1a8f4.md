---
id: T-e9a1a8f4
title: Cover remaining task source parsing costs
status: available
scope:
    - internal/infra/tasksource/github_issues.go
    - internal/infra/tasksource/parse.go
    - internal/infra/tasksource/markdown.go
    - internal/infra/tasksource/github_issues_test.go
    - internal/infra/tasksource/parse_test.go
priority: 80
kind: task
require_review: llm
---

Goal: GitHub issue listing and card text parsing must not scale with a hidden 10k cap or repeated splits.

Evidence: opt-tasksource.md findings 3 to 5 show GitHub stops at 10,000 items, the field cache cannot cover a card corpus, and card text is split several times.

Steps: Raise or make the GitHub bound explicit, share one field cache across the corpus, and split frontmatter once per card.

Check: Fixtures with 12,000 issues and 20,000 cards produce complete results with bounded file reads and identical fields.

Accept: No card format change.

Open a PR, call beacon_watch_pr, do not self-merge.
