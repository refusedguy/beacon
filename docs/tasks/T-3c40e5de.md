---
id: T-3c40e5de
title: Fetch independent PR signals concurrently
status: available
scope:
    - internal/infra/scm/github.go
    - internal/infra/scm/gitlab_mr.go
    - internal/infra/scm/gitlab_checks.go
    - internal/infra/scm/github_test.go
    - internal/infra/scm/gitlab_test.go
priority: 85
kind: task
require_review: llm
---

Goal: Halve round trips per Observe without changing any verdict.

Evidence: opt-scm.md shows GitLab MR does six serial calls and GitHub four after the PR read. Failed-job traces are also serial.

Steps: Group calls with no data dependency and run them under a bounded errgroup with a breaker limit. Keep ETag cache, error semantics and excerpt order unchanged. Use fake SCM tests that count round trips.

Check: GitLab rounds fall from 6 to 2 and GitHub from 4 to 2 with identical Observation fields, zero errors and Incomplete false. Targeted scm tests pass.

Accept: No policy or wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
