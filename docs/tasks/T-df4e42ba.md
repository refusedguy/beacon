---
id: T-df4e42ba
title: Parse poll interval once in config
status: done
scope:
    - internal/infra/config/config.go
    - internal/infra/config/config_test.go
depends:
    - T-64fe8d76
priority: 56
kind: task
require_review: llm
---

Goal: One parse result shared by Load and validate.

Evidence: final-10. Load computes a parse error and returns 30s, then validate reports the same error.

Steps: Parse once in Load and pass the result to validate.

Check: Config tests pass with the same silent default for code-built configs.

Accept: Removing the deliberate default changes behaviour, so keep it.
