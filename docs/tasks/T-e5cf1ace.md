---
id: T-e5cf1ace
title: Add ready-to-merge when required checks turn green
status: available
scope:
    - .github/workflows/ci.yml
priority: 1
kind: task
require_review: llm
---

Goal: an ordinary PR that already has green required checks gets ready-to-merge without needing a new push.
Evidence: PRs 2856 and 2858 were MERGEABLE with all required checks green and mergeStateStatus CLEAN, yet sat in waiting_ci with reason missing label ready-to-merge. ci.yml mark-ready runs only on pull_request events with types opened, synchronize, reopened, labeled. A worker opens the PR before CI completes, so at synchronize time the checks are still pending and the label step does run, but on this checkout the PRs reached CLEAN without a later synchronize event and never got the label. Beacon's merge.labels criterion then blocks forever even though every check passed. Perf PRs are intentionally excluded from mark-ready and get the label after a measured gain; that path is correct and must stay.
Steps: make mark-ready also run on check_suite or workflow_run completion for non-perf PRs, or let ci.yml add the label on a scheduled or workflow_run event when all required checks are green and the PR has no perf label; keep the perf exclusion so perf PRs are never auto-labeled; keep pull-requests: write least privilege and do not create a new required check name by adding a job-level continue-on-error or matrix to mark-ready.
Accept: an ordinary PR whose checks turn green without a fresh push receives ready-to-merge within one workflow run, and a perf PR never receives it automatically.
