---
id: T-d2eb126c
title: Add noise policy and control key to registry
status: available
scope:
    - internal/bench/registry.go
    - internal/bench/registry_test.go
    - docs/perf/bench-registry.json
depends:
    - T-b08e5abe
    - T-8a1a041c
priority: 28
kind: task
require_review: llm
---

Goal: make the registry own noise policy and the control plan metadata.

Evidence: noise-09-control.md and noise-03-statistics.md. Add a top-level noise block with defaultFloorPct, maxAgeHours, minSamples and quantile. Add a spec for the pure control key from @1 with placement both. Keep names and limiter strings owned by the Go registry. Regenerate the JSON and keep the drift test green.

Accept: no ci.yml change, no local execution. Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check.
