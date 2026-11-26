---
id: T-1b2e1dd7
title: Merge the four Go CI jobs into one
status: available
scope:
    - .github/workflows/ci.yml
depends:
    - T-PLACEHOLDER7
priority: 78
kind: task
require_review: llm
---

Goal: One Go job restores the cache once and reuses build output.

Evidence: final-09. build, vet, test and lint each check out, set up Go with cache and compile the same packages on separate runners.

Steps: Merge them into one job with ordered steps and keep all four signals as named steps.

Check: A push runs all four checks and the required check names are preserved.

Accept: Wall clock may grow because go test -race dominates. One failure stops later steps.
