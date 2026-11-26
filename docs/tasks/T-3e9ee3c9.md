---
id: T-3e9ee3c9
title: Make orchestrator.md refer to AGENTS.md
status: done
scope:
    - .opencode/agents/orchestrator.md
priority: 62
kind: task
require_review: llm
---

Goal: Stop copying AGENTS.md rules into the agent file.

Evidence: final-13. Rules 2 and 5 restate the disjoint scope and build-restart sequence, and line 31 duplicates the global writing style.

Steps: Keep frontmatter permissions and replace copied rules with a refer to AGENTS.md and the global writing skill.

Check: The agent still receives the rules through the global instructions.

Accept: Narrowing rule 2 to the AGENTS.md wording could allow two cards the agent previously blocked. Call that out in the PR body.
