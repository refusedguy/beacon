---
id: T-5a0b4a2a
title: Make scheduled runs execute perf work only
status: available
scope:
    - .github/workflows/ci.yml
    - scripts/bench-e2e.sh
depends:
    - T-5c9551de
priority: 85
kind: task
require_review: llm
---

Goal: Nightly CI does not repeat the full PR matrix before running benchmarks.

Evidence: cicd-06.md shows scheduled runs still execute normal jobs and install benchstat unconditionally.

Steps: Gate non-perf jobs with if: github.event_name != 'schedule'. Gate benchstat install and artifact upload on the nightly compare step. Make bench-e2e.sh seed setup smoke-safe.

Check: A workflow_dispatch run lists only perf jobs and the compare step still produces a benchstat artifact.

Accept: PR behavior unchanged.

Open a PR, call beacon_watch_pr, do not self-merge.
