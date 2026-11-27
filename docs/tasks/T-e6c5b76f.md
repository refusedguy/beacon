---
id: T-e6c5b76f
title: 'Orchestrator: start one specific card by ID without touching others'
status: available
scope:
    - AGENTS.md
priority: 1
kind: task
require_review: llm
---

Goal: give the orchestrator an exact, repeatable procedure to start ONE specific card by ID, so no unrelated cards are touched.

Problem observed: while trying to give a high-priority card a free slot, the orchestrator cancelled and deleted unrelated cards that merely shared ci.yml. That destroyed work and had to be reverted with five git revert commits.

Procedure to encode, to be followed literally by the orchestrator:
1. Read the target card file docs/tasks/<ID>.md. Record its status, scope, and depends.
2. Check beacon_status. If a run for this task already exists, do nothing and report its state. Never cancel it without an explicit operator instruction.
3. Check beacon_dispatch with n=1. Read skipped[].blocked_by and overlap. If the target is blocked by an active run, report the blocker ID and STOP. Do NOT cancel the blocker, do NOT delete other cards, do NOT change statuses.
4. To start exactly one card when it is blocked, ask the operator which blocker to resolve. Acceptable alternatives are: wait for that run to finish, or ask operator approval to cancel that specific run_id. Never infer that an unrelated card is disposable.
5. Never edit card status by hand in the main checkout to manipulate dispatch. Never use beacon_task_delete on a card that is not explicitly confirmed terminal by the operator.
6. After a successful beacon_dispatch, verify with beacon_status that only the intended TaskID moved to running.

Accept: a future orchestrator session follows these steps and does not cancel or delete any card other than the one explicitly requested by ID, and does not create revert commits.
