---
id: T-b740cced
title: Skip config rebuilds that cannot apply
status: available
scope:
    - cmd/beacon/config_watch.go
    - cmd/beacon/config_watch_test.go
priority: 82
kind: task
require_review: llm
---

Goal: A model-only config edit must not rebuild harness and policy or restart watchers.

Evidence: opt-http.md finding 5 shows every 2s tick reads, hashes, rebuilds and signals even when restartFields rejects the change.

Steps: Compute the effective change set first. Skip rebuilds and broadcasts when no restart field changed. Keep current behavior for real changes.

Check: A fake ticker with 60 model-only toggles reports one read and hash per tick, zero newHarness calls, zero reload broadcasts. Targeted tests pass.

Accept: No config semantics change.

Open a PR, call beacon_watch_pr, do not self-merge.
