---
id: T-56b0a7dd
title: Publish perf environment pins and floor age in artifacts
status: done
scope:
    - .github/workflows/ci.yml
    - internal/bench/report.go
    - internal/bench/run_watcher_bench.js
priority: 2
kind: task
require_review: llm
---

Goal: let a reader reproduce or reject a perf verdict by seeing the machine, toolchain and harness that produced it.
Evidence: the PR advisory, noise floor and nightly gate run on three machine classes; ubuntu-latest is a moving image label; setup-go resolves 1.26 to a patch at run time and no artifact records it; compiler flags and GOAMD64 are not captured; the Go harness prints a seed but no fixture hash; the pinned CPU index goes only to the step log; the combined harness hash is read but never printed; the noise floor artifact publishes only a quantile and discards both pass logs; maxAgeHours 168 in the registry is never applied.
Steps: have the pass-running shell steps write perf-env.txt with go version, GOFLAGS, CGO_ENABLED, GOAMD64, GOGC, GOTOOLCHAIN, GOMAXPROCS, /proc/cpuinfo model name and the literal test argument vector; add ImageOS, ImageVersion, RUNNER_ENVIRONMENT, RUNNER_OS, RUNNER_ARCH and RUNNER_NAME to the comment body and to noise-floor.json; add perf-hash to the artifact path and print both hashes; print the fixture hash from ui_bench.js and add one to run_watcher_bench.js; upload perf-base.txt and perf-head.txt with the noise floor artifact; print the downloaded floor run id and created_at and apply maxAgeHours from the registry.
Accept: a PR comment contains enough data to tell whether base, head and floor used the same machine, toolchain, flags, fixtures and harness, and a stale floor is visibly flagged.
