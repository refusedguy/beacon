---
id: T-f2c81cb2
title: Unify excerpt truncators
status: available
scope:
    - pkg/core/prompt_text.go
    - internal/app/base_red.go
    - internal/app/base_red_test.go
depends:
  - T-08e7e230
  - T-46f4da32
priority: 60
kind: task
require_review: llm
---
Goal: One excerpt truncator with one marker.

Evidence: final-13. truncate uses an ellipsis suffix and compactExcerpt uses a 1200 rune limit with a different marker.

Steps: Move one implementation to pkg/core, keep the 1200 rune limit and the rune-safe scan from the recent optimization, and use one marker.

Check: Correction protocol fixtures pass with the new marker.

Accept: Medium risk because wake text excerpts may be parsed by fixtures.