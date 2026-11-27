---
id: T-2f61d0ce
title: Settle in_progress check runs with a terminal conclusion
status: available
scope:
    - internal/infra/scm/github.go
    - internal/infra/scm/github_checks_test.go
priority: 1
kind: task
require_review: llm
---

Goal: a check run that GitHub reports as status in_progress with a terminal conclusion must not park a run in waiting_ci forever.
Evidence: PR 2845 head 9cf67fb has one vet check run with status in_progress and conclusion success, left over from workflow run 46042152590 that was cancelled. The GitHub mapper maps status in_progress to CheckPending regardless of conclusion, so forced reconcile keeps returning required checks not passed: vet (pending) and the run cannot advance even after the real CI run finishes. The newest-attempt-per-name collapse does not help because there is only one vet run recorded on that SHA.
Steps: in checks() treat a run with a terminal conclusion such as success, failure, cancelled or skipped as completed even when status still says in_progress; keep ordering by completion time then start time and keep the newest attempt per name; keep cancelled checks blocking per T-78b0be80 where that is the correct verdict; add a test with an in_progress check carrying a success conclusion and a test that a later real attempt still wins.
Accept: an in_progress check with a success conclusion no longer produces a pending verdict, and an ordinary green PR reaches merge instead of waiting on a stale check run.
