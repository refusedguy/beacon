---
id: T-c575dc9b
title: Cache the build script existence check per root
status: done
scope:
    - .opencode/plugins/beacon-project/rules/context.js
    - .opencode/plugins/beacon-project/rules/context_test.js
priority: 75
kind: task
require_review: llm
---

Goal: Orchestrator events must not stat the same build script path repeatedly.

Evidence: opt-js-ops.md finding 5 shows existsSync on scripts/build-beacon.sh for every event.

Steps: Cache one boolean per project root and invalidate only when the root changes.

Check: A 10,000-event fixture reports one existsSync call and identical context output.

Accept: No rule behavior change.

Open a PR, call beacon_watch_pr, do not self-merge.
