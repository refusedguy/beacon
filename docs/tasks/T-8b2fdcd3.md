---
id: T-8b2fdcd3
title: 'W2-1: cut HTTP event frames and Go TUI view allocations'
status: done
scope:
    - cmd/beacon/http.go
    - cmd/beacon/http_test.go
    - ui/tui/model.go
    - ui/tui/model_test.go
priority: 34
kind: task
require_review: llm
---

Goal: cut allocations and write calls in the HTTP event path and the Go TUI renderer without changing any SSE byte or rendered frame.

Evidence: advisory rows http-frame-emission and go-tui-view now exist with per-key floors of 36.6% and 67.6% from calibration run 45984052138. W2-0 wired the benchmarks and the registry. The current code still uses fmt.Appendf in eventFrames.run at cmd/beacon/http.go:181 and issues two rw.Write calls per frame in events. ui/tui/model.go View builds a strings.Builder without Grow and has no full-frame cache keyed by the render inputs.

Variants, one commit and one push each, up to five:
1. V1 replace fmt.Appendf in eventFrames.run with a preallocated buffer and strconv or append, keeping the exact SSE bytes.
2. V2 in events build id and frame into one buffer and call rw.Write once per frame, keeping the id sequence and Flush behavior.
3. V3 add strings.Builder.Grow in ui/tui/model.go View using the row count and header size.
4. V4 add a full-frame cache keyed by the tuple (width, cursor, status, failed, runs) and return the cached string when unchanged. Keep the existing renderCache pad reuse.
5. V5 combine only the variants that individually produced a gain.

Accept: PR carries the perf label and no ready-to-merge until every variant is tried. Read the beacon-pr-perf comment after each push. Keep a variant only when the deciding row reports gain with equal work, errs zero, non-empty limiter and cache warm. For HTTP variants the deciding row is http-frame-emission; for TUI variants it is go-tui-view. A flat, inconclusive or regression verdict means git reset --hard HEAD~1 and git push --force-with-lease. The final PR body carries a table with base, head, ranges, work, errs, limiter and verdict for each kept variant, then set ready-to-merge.
Protocol: Work in your own worktree and branch off current main. Never push to main. Never run go test, go build, go vet, linters or benchmarks locally. Open a PR and call beacon_watch_pr. CI supplies every check.
