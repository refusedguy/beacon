---
id: T-8299bbba
title: Give matrix entries per-module path filters
status: available
scope:
    - .github/workflows/ci.yml
depends:
    - T-573cc3a0
priority: 84
kind: task
require_review: llm
---

Goal: Run only the matrix legs whose module changed.

Evidence: ciopt-07 item 2 and ciopt-10. test-modules always runs five legs. scm, tasksource, harness and plugin leaf changes have short import chains.

Steps: Add a filter key to each matrix entry and gate on needs.changes.outputs[matrix.filter]. Keep shared paths forcing every leg.

Check: A harness-only change runs its leg and not the root legs.

Accept: Branch protection must not require skipped matrix checks.
