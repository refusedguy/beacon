---
id: T-d5d63a8e
title: Show attempt-aware CI state in detail panel
status: done
scope:
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_model.js
    - .opencode/plugins/beacon-project/ui/ui_view_test.js
    - .opencode/plugins/beacon-project/ui/ui_model_test.js
    - .opencode/plugins/beacon-project/ui/ui_detail_test.js
priority: 1
kind: task
require_review: llm
---

Goal: make the detail panel show the current attempt, not a stale mixture of previous attempts.

Evidence from the operator screenshot of PR #2782: the panel shows green checkmarks left over from an earlier attempt while the run is in `waiting_ci`. The phase line says `Coding 7m · CI wait 3m · Blocked 0s`, counting from the first start, so the operator cannot see how long the current CI round has been waiting. The technical section shows `Rebase 0/12 · Fix 0/12` even after a rebase occurred, and the timeline shows `transition.rebase attempt` followed by another `needs_rebase` without a clear result. The operator cannot tell whether a rebase happened, passed, or failed.

Steps:
1. In `ui_view.js`, stop rendering checks from a previous head SHA as current. When the observation head differs from the head those checks belong to, show them as stale or hide them and say checks are pending for the current head. Use `run.headSha` and the check result's head SHA when present.
2. Use attempt-scoped durations for the phase line. When the run has a rebase or fix transition after the first start, show the current `CI wait` or `Coding` time from that transition instead of the cumulative duration. Keep cumulative time in the technical section under a separate label.
3. In `ui_model.js`, normalize attempt counters and transition events so the view can tell whether a rebase finished, failed, or is still running. Add fields only from the existing timeline and decision data.
4. In the technical section, replace the bare `Rebase 0/12 · Fix 0/12` with the current attempt outcome, head SHA at that attempt, and a short rebase status. Keep the old counters as secondary information.
5. Add tests with a fixture that contains a first attempt with green checks, a rebase transition, and a second `waiting_ci` observation with new checks pending. Assert the panel does not show the old green checks as current, shows the current CI wait time, and names the rebase outcome.

Accept: no wire contract changes, no change to Beacon state or reconciliation, and no local test or build runs.
