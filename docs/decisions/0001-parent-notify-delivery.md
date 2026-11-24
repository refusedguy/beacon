# 1. Parent session notification delivery

Status: recorded, no change made.

## Why this is here

Beacon sends a short message to the orchestrator session when a run merges. The message can take minutes to appear. This note records the measured cause, the options, and the code paths involved. It exists so the next reader does not repeat the investigation.

## What happens now

`internal/app/finalize.go` calls `notifyParent` right after `SCM.Merge` and `Store.Save`. The harness then posts to `/api/session/<id>/prompt` with `delivery: "queue"` and `resume: false`.

OpenCode 2.3.2 treats that request as durable admission. `packages/core/src/session/session.ts:182` calls `execution.wake(sessionID)` only when `resume` is not false. With `resume: false`, the message enters `session_inbox` and stays there. `packages/core/src/session/inbox.ts` promotes queued items only at an idle boundary. A running agent asks for the `steer` scope, which rejects queued items.

The message appears when the operator next activates the session. Several messages can be promoted in one drain. On 24 November 2026, two messages admitted six minutes apart became visible three milliseconds apart.

The 30 second reconciler poll is not the cause. Beacon merges and posts the notice in the same tick.

## Measured delay

Delay is the time from the `transition.parent_notify` decision to the message becoming visible in the parent session.

| Pull request | Recorded delay |
| --- | --- |
| #2378 | 112 seconds |
| #2379 | 29 seconds |
| #2380 | 500 seconds |
| #2381 | 138 seconds |
| #2359 | 250 seconds |

`internal/app/finalize.go` posts within 0.04 to 0.30 seconds of the merge in every measured case. All of the delay comes after that post.

## Options

1. Set `resume` to true and keep `delivery: "queue"`. OpenCode wakes the session and promotes the message at the next boundary. An idle session shows it immediately. An active agent finishes its current step first. This is a one-line change in `harnesses/opencode/opencode.go`.
2. Set `delivery: "steer"` and `resume: true`. This injects the message into the current turn. It arrives faster and interrupts the agent. An informational notice does not justify that.
3. Send the notice to the monitor over SSE instead of a session prompt. This leaves the session untouched and needs more code.
4. Keep the current behavior. The delay stays variable and depends on session activation.

## Current decision

No option was accepted. The behavior is recorded here so the operator can choose later. The recommendation in the investigation was option 1, but it was not applied.

## Code and source pointers

- `internal/app/finalize.go`: `notifyParent`, message text, and the guard for an empty parent session.
- `internal/app/reconciler.go`: merge order, `run.Apply(EvMerged)`, `Store.Save`, then `notifyParent`.
- `harnesses/opencode/opencode.go`: `NotifyParent`, request body, and token handling.
- OpenCode 2.3.2 in `/tmp/opencode-2.3.2`: `packages/core/src/session/session.ts`, `packages/core/src/session/inbox.ts`, and `packages/core/src/session/runner/llm.ts`.
