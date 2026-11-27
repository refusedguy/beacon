---
id: T-9f4fd8f7
title: Fix SCM observation correctness on provider edge cases
status: done
scope:
    - internal/infra/scm/github.go
    - internal/infra/scm/gitlab_mr.go
    - internal/infra/scm/github_etag.go
    - internal/infra/scm/webhook.go
    - internal/infra/scm/gitlab_checks.go
priority: 2
kind: task
require_review: llm
---

Goal: make observations reflect one coherent provider state instead of mixing reads and trusting stale cache entries.
Evidence from audit: github.go:162-168 and gitlab_mr.go:91-95 treat a deleted base branch as a raw error or an endless wait because compare uses the branch ref instead of the SHA; github.go:292-320 breaks approval timestamp ties in the wrong direction so a dismissed review can still count; checks are not deduplicated by name so a re-run or a duplicate cancelled run can decide the verdict; getPR caches by PR number with no TTL or write-through after UpdateBranch or Merge; paginated lists can shift under concurrent writes and capped results are reported as complete; webhook.go ignores X-GitHub-Delivery and times out after two seconds while still returning 202; compare reports a dirty state with ahead_by zero which dispatches an agent instead of a branch update.
Steps: compare by base SHA from the PR object; break review ties by monotonic review ID and fail closed on missing timestamps; collapse check runs by name keeping the newest and set GitHub Filter explicitly to latest; write through the PR cache on mutation and bound it by TTL and size; deduplicate paginated results by provider ID and mark capped reads incomplete; deduplicate webhooks by delivery ID or body hash and return a retryable status when the channel is full; cross-check compare before acting on a conflict and include conflict in the observation identity.
Accept: one result per check name, a deleted base produces an explicit reason, a dismissed review cannot count as approval, and a duplicate webhook cannot trigger a second reconcile burst. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR without the perf label so mark-ready sets ready-to-merge. Call beacon_watch_pr. CI supplies every check.
