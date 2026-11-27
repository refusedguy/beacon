---
id: T-d3fd197f
title: Add prerequisite preflight for tools and panel
status: done
scope:
    - cmd/beacon/doctor.go
    - cmd/beacon/main.go
    - README.md
priority: 2
kind: task
require_review: llm
---

Goal: tell the operator exactly which prerequisite is unmet when beacon tools or the status panel do not appear.
Evidence: the ten tools come from a pooled MCP entry in a parent opencode.json, not from the plugin; the plugin only records a config and never attaches or reloads. The repo opencode.json has no mcp section while the parent config hardcodes the binary path. The live log shows 887 successful server connections and 1086 Cannot find package @opentui/solid warnings, so tools load while the TUI half fails silently. beacon doctor prints all OK lines while the local binary is stale and TUI peers cannot import; it checks PATH rather than the actual configured command, does not name the lock owner and does not compare the recorded source hash.
Steps: add a preflight view to beacon doctor that names each prerequisite with the concrete manual fix in the same line: MCP config source, the command path from the active config, binary freshness against the recorded source hash, current lock owner PID and daemon-error content, TUI plugin peer availability, operator agent identity and generated permissions; compare the actual configured command instead of a PATH binary; exit nonzero on a failed prerequisite that blocks tool or panel loading; do not change plugin import semantics or start a daemon from doctor.
Accept: when tools or panel are missing, one command names the missing prerequisite and the fix instead of reporting all OK.
