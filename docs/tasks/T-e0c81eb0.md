---
id: T-e0c81eb0
title: Surface the control result reason
status: done
scope:
    - ui/tui/model.go
    - ui/tui/model_test.go
depends:
    - T-c6ec90ca
priority: 70
kind: task
require_review: llm
---

Goal: A refused control action shows the daemon reason.

Evidence: final-15. The model requires both OK and Applied, so a response missing applied decodes as false and reports a bare refusal.

Steps: Surface the daemon reason and treat applied as authoritative only when present.

Check: A refusal with reason renders that reason.

Accept: Approved actions keep the same display.
