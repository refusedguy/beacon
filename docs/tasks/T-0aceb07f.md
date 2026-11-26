---
id: T-0aceb07f
title: Share FNV body hash
status: done
scope:
    - .opencode/plugins/beacon-project/index.js
    - .opencode/plugins/beacon-project/ui/ui_actions.js
    - .opencode/plugins/beacon-project/protocol/body_hash.js
    - .opencode/plugins/beacon-project/protocol/body_hash_test.js
depends:
    - T-41ea023d
priority: 84
kind: task
require_review: llm
---

Goal: One hash implementation for dispatch request ids.

Evidence: quality-05 item 1. index.js:35-44 and ui_actions.js:11-19 duplicate constants, JSON.stringify and 8-digit padding.

Steps: Add a peer-free bodyHash module and build requestId and dispatchRequestId as prefixes over it.

Check: Existing log ids are byte-identical.
