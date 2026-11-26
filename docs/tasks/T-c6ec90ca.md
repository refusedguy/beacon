---
id: T-c6ec90ca
title: Debounce pings into one reload
status: available
scope:
    - ui/tui/model.go
    - ui/tui/model_test.go
depends:
    - T-PLACEHOLDER11
priority: 80
kind: task
require_review: llm
---

Goal: Bursts of events issue one runs read.

Evidence: final-15. The daemon pings every 10 seconds and bursts of pr_changed or unknown-run events each issue their own GET with no coalescing. Concurrent Bubble Tea commands can let an older response overwrite a newer one.

Steps: Drop ping before scheduling refresh and debounce reloads into one command per burst.

Check: A burst of five events produces one read with the newest snapshot.

Accept: No event is silently dropped.
