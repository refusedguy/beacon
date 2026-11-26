---
id: T-ce0cf892
title: Skip matrix jobs in workflow required check names
status: available
scope:
    - internal/infra/config/config.go
    - internal/infra/config/config_test.go
    - internal/infra/checksource/workflow.go
    - internal/infra/checksource/workflow_test.go
priority: 100
kind: task
require_review: llm
---

Goal: Workflow mode never requires a check name that GitHub does not emit.

Evidence: reconcile on PRs 2630, 2631, 2632, 2634, 2635, 2636 and 2637 returns 'required checks not passed: test-modules (missing)' while gh pr checks shows all checks green. ci.yml has a top-level job named test-modules with strategy.matrix. GitHub reports only expanded names such as 'test-modules (root command and infra, ., ./cmd/... ./internal/...)'. DetectRequiredChecks and checksource.Workflow take the literal top-level name, so the requirement can never be satisfied. The separate job test already depends on the whole matrix and covers it.

Steps: Exclude jobs with strategy.matrix from resolved required check names in both resolvers. Keep the aggregator job test as the signal that every matrix cell passed. Add a test that parses the current ci.yml and asserts test-modules is absent from the resolved list while test, build, vet, lint, plugin-tui-smoke and perf-smoke remain.

Check: beacon_reconcile on the seven stuck PRs stops reporting test-modules missing and merges them when other criteria pass.

Accept: Do not edit ci.yml. Do not change the manual mode semantics.
