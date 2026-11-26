---
id: T-deae5948
title: Consolidate duration state categories
status: done
scope:
    - internal/app/status.go
    - internal/app/status_test.go
priority: 48
kind: task
require_review: llm
---

Goal: One mapping from RunState to duration bucket.

Evidence: final-11. status.go maps RunState to a duration bucket in two places with different buckets.

Steps: Add one helper and call it from both sites.

Check: Status tests pass with unchanged duration values.

Accept: Low payoff. Close with no change if the two buckets are genuinely different.
