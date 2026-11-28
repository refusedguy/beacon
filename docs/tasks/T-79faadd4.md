---
id: T-79faadd4
title: Extract shared perf harness setup into one script
status: done
scope:
    - .github/workflows/ci.yml
    - scripts/perf-harness.sh
priority: 5
kind: task
require_review: llm
status_reason: Already delivered in PR 2882 (commit cbac2f91); worker found no remaining work
---

Goal: remove duplicated perf harness setup from ci.yml without changing any required check name or workflow trigger.
Evidence: perf-pr-advisory at ci.yml:366-1226 and perf-noise-floor at ci.yml:1230-1469 repeat compile warmup (512-538 vs 1265-1289), run_side (540-588 vs 1291-1339) and canonical row parsing. docs/tasks/T-5dbcac20.md records this. Splitting into separate workflow files is unsafe because DetectWorkflowJobNames at internal/infra/shared/workflow.go:15-37 globs .github/workflows/*.y*ml and would add reusable-workflow inner job names to the required list; GitHub reports called-workflow checks under qualified names so Beacon would wait forever.
Steps:
1. Extract the shared warmup, run_side invocation and environment setup from both perf jobs into a single shell script under scripts/, with parameters for the output file prefix and the tree directory.
2. Keep both job names, both continue-on-error flags, both triggers and every check context unchanged. Both jobs call the script with their own arguments.
3. The trust guard at ci.yml:416 currently reads only .github/workflows/ci.yml. If any inline verdict script moves into the shared file, update the guard to hash both files and update the golden parser test that reads ci.yml from the checkout.
4. Keep changes.outputs.perf and artifact lookups pointing at ci.yml unless the job that owns them moves. Do not move required jobs.
5. Verify that DetectWorkflowJobNames still returns the same eight names after the change and that no new job appears in that list.

Accept: no duplicate harness code remains, no required check name changes, no workflow file is added under .github/workflows that could alter Beacon detection, and the advisory comment continues to work on both PR and scheduled runs.
