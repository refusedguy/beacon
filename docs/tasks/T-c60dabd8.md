---
id: T-c60dabd8
title: Fix operator-facing errors and next actions in plugin and TUI
status: done
scope:
    - .opencode/plugins/beacon-project/ui/ui_error.js
    - .opencode/plugins/beacon-project/ui/ui_refresh.js
    - .opencode/plugins/beacon-project/ui/ui_view.js
    - .opencode/plugins/beacon-project/ui/ui_actions.js
    - .opencode/plugins/beacon-project/index.js
    - ui/tui/main.go
    - ui/tui/api.go
    - ui/tui/model.go
priority: 2
kind: task
require_review: llm
---

Goal: turn dead-end messages into one cause and one action the operator can take.
Evidence from audit: ui_refresh.js:64-65 hides the first poll failure; ui_error.js:5 replaces all stderr with a fixed stale message; index.js:117-121 logs RPC registration failure only to console; ui/tui/main.go:31-33 discards SSE errors so rows freeze silently; api.go:175-176 shows a raw 401 string; model.go:256 reports 47 runs while showing 20; cancel and retry fire on one keypress at model.go:291-298; ui_view.js:393-395 and ui_actions.js:27,68-71 say unavailable or no selection without a next step; ui_view.js:33-53 maps unfinished recent states to OK; ui_view.js:332-339 says Queue 0 with no reason; ui_model.js:509-537 uses an unverified project root without showing it.
Steps: mark the first poll failure as retry pending and keep stale at two; include a short cause and one action in the stale banner; surface live-update state in the footer; forward SSE and auth errors to the model with a concrete action; disclose the row cap and scroll hint; require confirmation for cancel and retry; append a next step to every dead-end message; use one vocabulary for recent states; state why the queue is empty; display the project root and whether it is verified.
Accept: no status line or banner leaves the operator without either a cause or a next command. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR without the perf label so mark-ready sets ready-to-merge. Call beacon_watch_pr. CI supplies every check.
