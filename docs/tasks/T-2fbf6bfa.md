---
id: T-2fbf6bfa
title: Fix architecture boundary violations in core and TUI
status: done
scope:
    - ui/tui/sse.go
    - ui/tui/api.go
    - arch_test.go
    - pkg/core/ports.go
    - pkg/core/verdict.go
    - pkg/core/run.go
priority: 3
kind: task
require_review: llm
---

Goal: make the documented dependency direction enforceable.
Evidence from audit: ui/tui/sse.go:11 imports pkg/core and calls core.ParseSSE while ui/tui/api.go:22 already mirrors the wire DTOs; arch_test.go:235 checks only the internal prefix and omits harnesses/acp from walked roots; pkg/core/ports.go:37 ModelRef carries OpenCode JSON tags and PROpen and PROpened are GitHub and GitLab tokens in verdict.go:63-76; pkg/core/run.go:62 maps the vendor name opencode to LaunchMCP; prompt_text.go holds agent-facing wording because harness modules cannot import internal/app.
Steps: copy the small SSE parser into ui/tui and remove the core import, keeping the wire drift tests; make arch_test reject any beacon import from harnesses and ui and include harnesses/acp; make ModelRef neutral and build the vendor wire object in harnesses/opencode; move provider state classification behind the SCM adapters and remove provider constants from core; normalize vendor launch metadata to mcp, cli or ui at the boundary; propose a public prompt seam for harness modules in a follow-up discussion rather than changing wording in this card.
Accept: no file under ui or harnesses imports a Beacon package, the arch test fails on a synthetic violation, and core contains no vendor field names or product tokens. Work in your own worktree off current main. Never push to main. Never run go test, go build, go vet, linters, node tests or benchmarks locally. Open a PR without the perf label so mark-ready sets ready-to-merge. Call beacon_watch_pr. CI supplies every check.
