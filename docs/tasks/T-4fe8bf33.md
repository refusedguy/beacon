---
id: T-4fe8bf33
title: Sync UI benchmark state maps with production view
status: done
scope:
    - internal/bench/ui_bench.js
priority: 99
kind: task
require_review: llm
---

Goal: make the advisory perf job produce comparable rows instead of aborting in BenchmarkHotPaths.

Evidence: CI run 45943232881, job 140322011518 failed with AssertionError at internal/bench/ui_bench.js:248, 'the after mirror differs from production ui_view.js'. The expected line is 'P0 R1 C1 E0 F0 B1 M12 X1' but the mirror emits 'undefinedundefined R1 C1 undefinedundefined undefinedundefined B1 M12 undefinedundefined'. The local copies of STATE_BAR_COLOR and STATE_BAR_LETTER in ui_bench.js name only running, waiting_ci, blocked and merged. Production ui_view.js exports STATE_BAR_KEYS with eight entries: pending, running, waiting_ci, needs_rebase, needs_agent_fix, blocked, merged, closed. The mirror therefore writes undefined letters for four states and the correctness gate always fails.

Steps: copy all eight keys, colors and letters from ui_view.js into the two frozen maps in ui_bench.js. Keep the fixture, seed and hash unchanged. Keep every assertion. Do not change production ui_view.js or ci.yml.

Check: a labeled PR with no chart changes produces a PR comment with per-side rows that carry work, errors and limiter.

Accept: no benchmark command, wire contract or required check name changes.
