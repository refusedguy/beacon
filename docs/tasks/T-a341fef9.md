---
id: T-a341fef9
title: Remove Redact pass-through wrapper
status: available
scope:
    - internal/infra/shared/redact.go
    - internal/infra/notify/telegram.go
    - internal/infra/harness/cli_exec.go
    - internal/infra/mcp/server.go
    - internal/infra/scm/gitlab_mr.go
    - internal/infra/scm/gitlab_checks.go
    - internal/infra/shared/redact_test.go
priority: 70
kind: task
require_review: llm
---

Goal: One redaction implementation, no duplicate marker.

Evidence: quality-07 item 3. Redact is a pass-through and redacted copies redact.Marker only for a test.

Steps: Import pkg/core/redact at the five callers and delete the wrapper file and its test.

Check: Redaction behaviour and log output are unchanged.
