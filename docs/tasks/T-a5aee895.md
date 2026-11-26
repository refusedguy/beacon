---
id: T-a5aee895
title: Align required checks with current CI job names
status: available
scope:
    - .beacon/config.yaml
    - cmd/beacon/config_test.go
priority: 97
kind: task
require_review: llm
---

Goal: A green PR is not held by check names that the current workflow no longer emits.

Evidence: PR #2541 has all checks passing, but beacon_reconcile returns 'required checks not passed: build (missing), vet (missing), test (missing), lint (missing)'. Its branch rewrote ci.yml to a verify job and a matrix of test jobs. The config still lists build, vet, test and lint. Card T-42ae53b1 restores those names in ci.yml but is blocked by the active run on ci.yml, so the run can never merge.

Steps: Change merge.required_checks to the names the current workflow actually emits, using stable job names where possible. Add a config test that reads the workflow job names and asserts the required list is a subset. Do not edit ci.yml in this card.

Check: beacon_reconcile on PR #2541 stops reporting missing checks once the config is reloaded.

Accept: After PR #2541 lands, T-42ae53b1 restores the canonical names and this list is reverted in a follow-up card.
