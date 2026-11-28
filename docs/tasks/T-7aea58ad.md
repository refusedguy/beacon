---
id: T-7aea58ad
title: Make worker fix prompts match the actual situation
status: done
scope:
    - pkg/core/prompt_text.go
    - internal/app/reconciler.go
    - internal/app/reconcile_observation.go
    - internal/app/base_red.go
    - internal/app/finalize.go
    - internal/app/control.go
    - internal/app/idle.go
    - internal/app/dispatcher.go
priority: 2
kind: task
require_review: llm
---

Goal: stop asking a worker to do an action that contradicts its state or that it cannot influence.
Evidence: WakeText and WakeTextLimited append OpenPRAndWatchInstruction unconditionally although a woken run already has a PR; the same generic 'Fix the issue' covers failed checks, merge conflicts, review threads and automatic rebase failures; an SCM UpdateBranch error wakes the worker to fix a provider call it cannot influence; the fixer prompt has no run ID, no watch instruction and no base SHA, and lists PR failed checks next to a base excerpt; idle nudge renders PR #0 and an empty SHA then repeats the instruction twice; an operator nudge is rewritten into a fix command; review prompt has no criteria and no negative branch; spawn prompt hard-codes Go checks in non-Go stacks; the limited wake truncates the excerpt but not the reason that already contains the full excerpt.
Steps: add a separate existing-PR instruction rendered when PRNumber is nonzero; add an Action field to WakeContext and set it from verdict and criterion; do not consume a fix attempt on an SCM UpdateBranch failure and notify the operator instead; pass run ID and base SHA into fixerPrompt and use base failed checks; give idle nudge its own no-PR template with real PR and SHA; add a GuidanceText renderer with no appended action for Control.Nudge; include criteria and a negative branch in the review prompt; move stack-specific checks out of the stock spawn block into config; truncate the reason in WakeTextLimited.
Accept: a worker reads one instruction that matches its state, and a provider failure never spends a fix attempt.
