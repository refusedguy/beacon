---
id: T-a9c3f8e6
title: Regenerate worker tool docs
status: available
scope:
    - AGENTS.md
    - README.md
    - .opencode/plugins/beacon-project/rules/context.js
depends:
    - T-4e71b0d2
priority: 1
kind: task
require_review: llm
---

Goal: publish the worker tool list after T-4e71b0d2 changes ToolCatalog.
Evidence: beacon-gen derives the tool table and counts in AGENTS.md, README.md and the project plugin rules from ToolCatalog. TestDocsUpToDate fails until those marker blocks are regenerated.
Steps: run go generate ./cmd/beacon-gen after T-4e71b0d2 lands and commit the regenerated marker blocks in AGENTS.md, README.md and .opencode/plugins/beacon-project/rules/context.js. Do not edit ToolCatalog or opencode.json here.
Accept: TestDocsUpToDate passes, AGENTS.md lists only beacon_watch_pr and beacon_wait as worker tools, and build and orchestrator own the rest.
