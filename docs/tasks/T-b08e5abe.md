---
id: T-b08e5abe
title: Register four missing perf keys
status: available
scope:
    - internal/bench/registry.go
    - internal/bench/registry_test.go
    - docs/perf/bench-registry.json
priority: 20
kind: task
require_review: llm
---

Goal: close the gap reported by PR #2786 baseline measurement.

Evidence: the advisory comment printed `keys without a registry entry: snapshot, worktree-remove, tui-view, run-watcher-fp`. Their limiter text came from the stale map in ci.yml, not from docs/perf/bench-registry.json. That undermines the registry as the single source of truth.

Steps: add Spec entries for snapshot, worktree-remove, tui-view and run-watcher-fp with the exact limiter strings currently used in the comment, their work and err metrics, unit, placement and threshold. Regenerate docs/perf/bench-registry.json. Add a test that every canonical key printed by the harnesses has a registry entry and fails on an unknown key. Do not change ci.yml or bench output.

Accept: a baseline perf PR prints no parser notes about keys without a registry entry. No local test or build runs.
