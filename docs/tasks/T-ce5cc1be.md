---
id: T-ce5cc1be
title: Delete dead markdown wrappers
status: done
scope:
    - internal/infra/tasksource/markdown.go
priority: 88
kind: task
require_review: llm
---

Goal: Remove three one-line adapters with no caller.

Evidence: quality-01. splitFrontmatter at :287, headingID at :361 and parseLegacy at :392 were replaced by *Lines variants in ce4270dd. Only their doc comments reference them.

Steps: Delete the three wrappers and comments. No caller changes needed.

Check: rg finds no symbol references and tasksource tests pass.
