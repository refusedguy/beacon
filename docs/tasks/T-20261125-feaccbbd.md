---
id: T-20261125-feaccbbd
title: Make GitLab observation fail closed and decode responses safely
status: done
scope:
    - internal/infra/scm/gitlab_mr.go
    - internal/infra/scm/gitlab_checks.go
    - pkg/core/verdict.go
    - pkg/core/verdict_test.go
    - internal/infra/scm/gitlab_test.go
priority: 97
kind: task
require_review: llm
---

Goal: Make GitLab observation fail closed and decode responses safely.

Evidence: internal/infra/scm/gitlab_mr.go:49-97 discards five provider errors, and :184 and :220 discard json.Unmarshal errors. pkg/core/verdict.go:81-89 treats an empty check list as passing.

Scope: internal/infra/scm/gitlab_mr.go, internal/infra/scm/gitlab_checks.go, pkg/core/verdict.go, pkg/core/verdict_test.go, internal/infra/scm/gitlab_test.go.

Steps: Collect and return observation errors, or add an Observation incomplete flag that Decide treats as wait. Check both unmarshal errors and validate the merged state. Add page-two and provider-error tests.

Accept: A GitLab outage or malformed response can never produce a merge verdict.

No plugin JS, no task card edits unless the Scope says otherwise. Open a PR, call beacon_watch_pr, do not self-merge.
