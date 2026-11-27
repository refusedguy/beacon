---
id: T-546efee1
title: Always copy UI benchmark harness into base tree
status: done
scope:
    - .github/workflows/ci.yml
priority: 99
kind: task
require_review: llm
---

Goal: make every labeled PR produce comparable advisory rows.

Evidence: CI runs 45945359020 and eight sibling runs all passed perf-pr-advisory but wrote 'no comparable rows'. The base pass aborts at internal/bench/ui_bench.js:248. The two-tree step copies harness files only when git diff --name-only BASE HEAD lists them. For the nine perf2 PRs the merge-base predates PR #2736, so the fixed ui_bench.js is not in that diff and the base tree keeps the old four-key mirror. PR #2736 itself produced rows only because its branch carried the fix.

Steps: in the two-tree step, copy internal/bench/ui_bench.js from the head tree into the base tree unconditionally, before the changed-file loop. Keep the changed-file loop for Go benchmark test files and other harness changes. Leave production code at each tree's own revision. Keep the job advisory, keep exit zero, keep the comment marker, and change no check name.

Check: a labeled PR that changes no harness file still produces per-side rows with work, errs and limiter.

Accept: no benchmark command, wire contract or required check name changes.
