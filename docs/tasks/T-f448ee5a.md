---
id: T-f448ee5a
title: Stop normalizing unchanged snapshots in plugin watcher
status: available
scope:
    - .opencode/plugins/beacon-project/run_watcher.js
    - .opencode/plugins/beacon-project/run_watcher_test.js
    - .opencode/plugins/beacon-project/state/session_identity.js
    - .opencode/plugins/beacon-project/state/session_identity_test.js
priority: 88
kind: task
require_review: llm
---

Goal: Idle ticks and agentless events must not rebuild normalized state or the worker list.

Evidence: opt-js-ops.md shows run_watcher normalizes every 10s tick even with no change, and agentRole rebuilds the worker list on every call.

Steps: Compare a cheap snapshot fingerprint before normalizing. Freeze the configured worker list once per config. Memoize identity per session ID. Keep all public exports.

Check: With 1000 unchanged ticks and 10000 calls, a counting test reports zero normalizations and zero worker-list rebuilds after warmup, with identical emitted events.

Accept: No TUI peer packages added.

Open a PR, call beacon_watch_pr, do not self-merge.
