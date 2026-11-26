---
id: T-85aa33ce
title: Add acp module require and replace
status: done
scope:
    - go.mod
depends:
  - T-d12d266a
priority: 60
kind: task
require_review: llm
---

Goal: Make the root module self-contained.

Evidence: final-14. cmd/beacon imports harnesses/acp but root go.mod has no require or replace for it. go.work hides the gap.

Steps: Add require github.com/refusedguy/beacon/harnesses/acp v0.0.0 and a replace to ./harnesses/acp.

Check: A GOWORK=off build of the root module succeeds.

Accept: go.work entries stay unchanged.
