---
id: T-b6aae5a8
title: Merge first-non-empty helpers into pkg/core
status: available
scope:
    - pkg/core/text.go
    - internal/app/dispatcher.go
    - internal/infra/tasksource/parse.go
    - internal/infra/tasksource/markdown.go
    - internal/infra/tasksource/create.go
priority: 66
kind: task
require_review: llm
---

Goal: One firstNonEmpty helper with agreed trim semantics.

Evidence: final-04. firstNonEmptyLocal in dispatcher.go and firstNonEmpty in tasksource/parse.go do the same scan, but one trims and one does not.

Steps: Choose one semantic, put the helper in pkg/core and call it from all four call sites.

Check: Task parsing and dispatch tests pass with unchanged values.

Accept: Callers that need untrimmed values keep them by adjusting inputs, not a second helper.
