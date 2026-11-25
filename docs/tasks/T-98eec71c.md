---
id: T-98eec71c
title: Collapse queue parent derivation in the plugin
status: available
scope:
    - .opencode/plugins/beacon-project/ui/ui_alerts.js
    - .opencode/plugins/beacon-project/ui/ui_alerts_test.js
priority: 80
kind: task
require_review: llm
---

Goal: Make queueGroups the single source for parent, child and epic membership.

Evidence: queueGroups and queueRows each derive parent IDs and child counts from the same ID-prefix rule.

Scope: ui/ui_alerts.js and ui/ui_alerts_test.js.

Steps: Move parentOf and childrenOf into queueGroups. Have queueRows consume that result.

Accept: Collapsed epic, blocked group and +N more tests pass unchanged.

Open a PR, call beacon_watch_pr, do not self-merge.
