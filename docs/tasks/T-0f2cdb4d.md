---
id: T-0f2cdb4d
title: 'P1: Harden advisory parser trust and floor calibration'
status: available
scope:
    - .github/workflows/ci.yml
priority: 18
kind: task
require_review: llm
---

Goal: close the measurement-trust gaps found in the harness audit before launching more perf variants.
Evidence from audit: the workflow is taken from the PR head, so inline Node verdict rules are editable by the PR; expected rows come from measured data rather than registry; three copied canonical harness files are outside the parity hash; the global floor helper counts -benchtime=5x calibration rows, producing 43.2% instead of 11.9%; control drift only blanks ratio columns; HotPaths allocation lines are printed inside the timed region and benchstat drops them; worktree-remove has a blanket work-mismatch bypass.
Steps in .github/workflows/ci.yml: 1) execute the comment builder and floor calibrator from BASE_REF (git show BASE_REF:.github/workflows/ci.yml) or mark the advisory inconclusive when ci.yml appears in the PR diff; 2) derive expected rows from base-registry specs with placement pr or both and require exact set equality per pass; 3) add cmd/beacon/http_test.go, ui/tui/model_test.go and ui/tui/refresh_bench_test.go to hash_tree; 4) make the floor calibration side() keep only the later row per pass, matching toPasses; 5) require json-control in every pass and feed control drift into every verdict; 6) buffer or move the printReport output outside the timed region so benchstat sees a clean BenchmarkHotPaths result line with B/op and allocs/op; 7) replace the blanket worktree-remove work bypass with an explicit 3 to 2 allowance and require errs zero for timing gain or regression.
This card owns ci.yml alone. No other card may edit ci.yml while it runs.
Accept: on unchanged PR #2786 all rows stay flat with no false verdict, and HotPaths rows show real allocation columns. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR with the perf label at creation using `gh pr create --label perf` when the card is a perf card, otherwise no perf label. Call beacon_watch_pr. One variant is one commit and one push. Read the beacon-pr-perf comment after each push. Keep a variant only on gain, including gain (allocs/bytes) and gain (calls reduced), with equal work, errs zero, non-empty limiter and warm cache. Revert a losing variant with git reset --hard HEAD~1 and git push --force-with-lease. Never add ready-to-merge before all variants are checked and the final table is in the PR body. CI supplies every check.
