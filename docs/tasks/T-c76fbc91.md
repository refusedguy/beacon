---
id: T-c76fbc91
title: Cheap prefilter before log redaction
status: done
scope:
    - pkg/core/redact/redact.go
    - internal/infra/shared/logger.go
    - pkg/core/redact/redact_test.go
priority: 94
kind: task
require_review: llm
---

Goal: Stop ten regex passes per log field when no secret is present.

Evidence: quality-09 item 1. logger.go rebuilds a Record and redacts every string and error attribute; redact.go runs ten ReplaceAllString passes per value.

Steps: Add a cheap prefix prefilter and collapse rules into one alternation regex where possible. Keep all current redaction cases.

Check: Five alternating benchmark passes show non-overlapping ranges and identical redacted output.
