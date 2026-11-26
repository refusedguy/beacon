---
id: T-f80a2aac
title: Unify env and fallback helpers
status: in_progress
scope:
    - cmd/beacon/commands.go
    - internal/infra/config/config.go
    - cmd/beacon/main.go
    - cmd/beacon/doctor.go
    - cmd/beacon/init.go
depends:
    - T-0bb0a179
priority: 74
kind: task
require_review: llm
---

Goal: One operation for first non-empty env, config and default.

Evidence: quality-06. envString, literalOrEnv, firstNonEmpty, loadTokens and doctor/init reads express the same shape with reversed arguments and different empty semantics.

Steps: Add envOr(key, fallback) and coalesce(values...) and route those sites. Keep the config key string at the read site.

Check: Command and doctor tests pass with identical token resolution.
