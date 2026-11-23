---
id: EXAMPLE-01
title: Signal reconciliation after an operator retry
status: in_progress
scope:
  - internal/app/
depends: []
priority: 1
---

# EXAMPLE-01. Signal reconciliation after an operator retry

This card was the example placeholder for the markdown task source. Replace it with real work.

Goal: a retried run is re-evaluated by the reconciler immediately, the same way an unblocked or approved run is.

Context: `Control.Unblock` and `Control.Approve` call `c.signal` after they save the run, which wakes the reconciler for the run's PR. `Control.Retry` resets the attempt counters and moves a blocked run back to waiting_ci, but it never signals. The run then waits for the next daemon tick before anything looks at it again.

Steps:
1. Call `c.signal(run)` after the save in `Control.Retry`.
2. Add `internal/app/control_test.go`. Prove a retried blocked run with a PR fires OnChange with that PR, moves to waiting_ci and clears its counters. Prove a non-blocked run is rejected without a signal.

Check: `go test ./internal/app/...` passes, and the new signal assertion fails against the old `Control.Retry`.

Accept: no change to any control action other than Retry, and no production file over 350 lines.
