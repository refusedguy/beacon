---
id: T-4a47e8cc
title: Remove redundant issue-scout permission explanation
status: available
scope:
    - .opencode/agents/issue-scout.md
priority: 48
kind: task
require_review: llm
---

Goal: State the beacon tool deny once.

Evidence: final-13. Line 37 repeats the deny from lines 17 to 22 with a rationale.

Steps: Move the rationale to AGENTS.md or a code comment and leave the rule in the frontmatter.

Check: Agent rule tests pass.

Accept: The agent must keep the deny rule, so do not weaken line 17 to 22.
