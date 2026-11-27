---
id: T-4c9a4faa
title: Fix advisory perf measurement path completely
status: done
scope:
    - .github/workflows/ci.yml
    - internal/infra/shared/workflow.go
    - internal/infra/shared/workflow_test.go
priority: 99
kind: task
require_review: llm
---

Goal: make perf-pr-advisory produce trustworthy comparable rows for every labeled PR and stop it from acting as a required check.

Evidence: static audit /home/nbook/tmp/backup/opencode/research/perf-harness-audit.md. Nine perf2 PRs all report 'no comparable rows' with a green advisory check. Their own heads carry the old four-key ui_bench.js, so copying head into base copies the broken mirror. Production plugin files are copied into base, so tui-view cannot show a change. A B B A B gives two base samples and three head samples and the comment median picks the larger of two. The tui-view range is overwritten by the last pass. run_side omits -benchmem so planner bytes and allocs never appear. ParseWorkflowJobNames returns perf-pr-advisory as a required check, and a failed advisory step produced verdict needs_agent_fix.

Steps:
1. In the two-tree step, fetch the PR base ref and write `git show origin/<base.ref>:internal/bench/ui_bench.js` into both worktrees. Keep the changed-file loop for Go benchmark test files and other harness files.
2. Remove `.opencode/plugins/beacon-project/*` from the copy case so each tree keeps its own production UI.
3. Add -benchmem to the three go test benchmark commands in run_side.
4. Make the passes symmetric: A B B A B A, three per side. Change the comment median to average the two middle values for an even count.
5. Delete the line that assigns ranges[side]['tui-view'] on each production: median line so it uses the normal min and max fallback.
6. Have run_side append `abort <side>` to perf-status when any command exits nonzero, and include that file in the comment body while still exiting zero.
7. Add continue-on-error: true to the Build comparison comment step.
8. In ParseWorkflowJobNames, skip a job whose body contains continue-on-error: true so advisory jobs are not required checks. Add a test.

Check: a labeled PR that changes no harness file produces per-side rows with work, errs, limiter, three samples per side and no abort lines.

Accept: no benchmark command result is fabricated, no wire contract changes, and the job never writes a failure check run.
