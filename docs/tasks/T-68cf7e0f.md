---
id: T-68cf7e0f
title: Cache task tree across red-base lookups
status: done
scope:
    - internal/app/finalize.go
    - internal/app/finalize_test.go
priority: 90
kind: task
require_review: llm
---

Goal: One red-base incident reads the card source once, not once per active run.

Evidence: opt-app.md shows finalize.go builds taskTree for taskByID, epicTask and baseBranchFor. blockRunsOnRedBase calls baseBranchFor per active run, giving 500 full card reads with 1000 cards.

Steps: Build the tree once at the start of blockRunsOnRedBase and pass it to the helpers. Keep behavior identical for duplicate IDs and missing cards. Add a counting fake source asserting one ListAvailable call.

Check: A benchmark with 1000 cards and 500 waiting_ci runs reports one ListAvailable call and one tree build. Targeted app tests pass.

Accept: No verdict or wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
