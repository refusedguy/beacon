---
id: T-d6da2e78
title: Avoid token byte conversion per request
status: done
scope:
    - cmd/beacon/http.go
    - cmd/beacon/http_test.go
depends:
    - T-4f928f6b
priority: 76
kind: task
require_review: llm
---

Goal: Auth check does not allocate two byte slices per monitor request.

Evidence: quality-09 item 7. http.go:270 converts token and header on every call.

Steps: Store token bytes on the server at construction and compare over strings with constant time semantics.

Check: Auth tests pass and a wrong token is still refused.
