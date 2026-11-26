---
id: T-efc794fe
title: Split markdown card index cache
status: available
scope:
    - internal/infra/tasksource/markdown.go
    - internal/infra/tasksource/cardindex.go
depends:
    - T-ce5cc1be
priority: 74
kind: task
require_review: llm
---

Goal: Cache state and parsing live in separate files.

Evidence: quality-10 item 7. The mtime index cache at :62-239 is distinct from parsing at :268-398 and status rewrite at :415-476.

Steps: Move the cache and its package-level state to cardindex.go.

Check: Tasksource tests pass with identical cache behaviour.
