---
id: T-fac406a5
title: Run local format and tests before opening a PR
status: done
scope:
    - internal/app/dispatcher.go
    - harnesses/opencode/prompt_text.go
    - internal/app/dispatcher_test.go
priority: 82
kind: task
require_review: llm
---

Goal: A PR is not opened with a known lint or formatting failure.

Evidence: cicd-10.md shows fix rounds happen after CI, consuming attempts and adding 7m waits.

Steps: Add a pre-PR check in the worker prompt and harness: gofmt -l on changed Go files and targeted go test for the package. Fail the worker before PR creation with the exact command and output. Keep the check advisory on weak machines only if documented.

Check: A worker with an unformatted file does not open a PR and reports the gofmt output. Existing dispatch tests pass.

Accept: No Beacon wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
