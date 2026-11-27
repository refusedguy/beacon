---
id: T-bec1a815
title: Quote malformed [perf] card titles
status: available
scope:
    - docs/tasks/T-364fbf77.md
    - docs/tasks/T-5d11af6c.md
    - docs/tasks/T-b8fc3701.md
    - docs/tasks/T-d42d4241.md
    - docs/tasks/T-de0058a2.md
priority: 1
kind: task
require_review: llm
---

Goal: repair five task cards whose frontmatter title starts with an unquoted [perf] token, which yaml.v3 reads as a flow sequence and rejects. The cards are invisible to Markdown.ListAvailable, the card index and .beacon/state.db. This is the exact malformed-card log the operator saw.

Cards: T-364fbf77, T-5d11af6c, T-b8fc3701, T-d42d4241, T-de0058a2. Fix: quote the title value with single quotes so the [perf] prefix stays inside a YAML string. No other card content changes.

The fix is already staged in the orchestrator main checkout together with two legitimate Beacon status flips from merged PRs #2849 and #2850. Commit only the five title lines plus those two status lines. No code changes and no local checks are needed; CI supplies verification.
