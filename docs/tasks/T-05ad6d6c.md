---
id: T-05ad6d6c
title: Remove duplicate govet invocation
status: available
scope:
    - .github/workflows/ci.yml
    - .golangci.yml
depends:
    - T-PLACEHOLDER10
priority: 56
kind: task
require_review: llm
---

Goal: Run govet once.

Evidence: final-09. golangci-lint enables govet by default while the vet job runs go vet ./... too.

Steps: Check the golangci config. If govet is enabled, disable it or drop the vet job.

Check: A push still reports vet findings once.

Accept: If govet is already disabled, close this card with no change.
