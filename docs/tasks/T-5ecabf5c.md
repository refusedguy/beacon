---
id: T-5ecabf5c
title: Make the nightly perf gate enforce per-key thresholds and break baseline wedges
status: available
scope:
    - scripts/check-bench-regression.sh
    - .github/workflows/ci.yml
    - internal/bench/bench_test.go
    - internal/bench/hotspots_test.go
    - internal/bench/e2e_bench_test.go
    - docs/perf/bench-registry.json
priority: 2
kind: task
require_review: llm
---

Goal: make the nightly gate test the registry thresholds it claims to enforce and stop a single spike from wedging future nights.
Evidence: the gate only processes metrics named sec/op or ending in -median-sec, while every emitter uses -median-ns, so all 24 per-key 15 percent thresholds are dead; only aggregate sec/op of two benchmarks is gated. The 2 percent floor only suppresses output; the real limits are hardcoded 15 and 20. Recorded baseline medians vary 1.62 to 3.17 seconds on the perf runner, so 15 percent sits inside machine spread; the advisory already rejected a single fixed floor. Poisoned-baseline handling fails the compare and skips publish, so the next night downloads the same poisoned artifact forever, and perf-nightly cannot run manually. The manual full-suite step is unreachable under the schedule-only trigger. No CPU pinning, no control row and no spread trimming in nightly.
Steps: teach the script to read -median-ns and convert to seconds, or rename emitters, so registry thresholds apply; move the limits into the registry; enforce work and error equality against canonical rows; replace or supplement the fixed limits with a calibrated floor and spread rule; break the wedge by keeping the last known good baseline and marking an inconclusive night instead of skipping publish on every failure; add a manual dispatch trigger that runs the nightly comparison for recovery; delete or enable the dead full-suite step; document the remaining difference between hosted advisory and self-hosted nightly machine classes.
Accept: a 5 percent per-key regression fails the gate, a poisoned baseline self-heals on the next night, and every threshold in the registry is live.
