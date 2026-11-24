# 3. Empty queue on the first plugin render

Status: recorded, fixed by T-20261124-d6417c0b. No Go edits.

## Why this is here

After an OpenCode restart the Beacon sidebar showed a queue of zero even though the daemon reported pending cards. A manual plugin reload made the cards appear. This note records the measured cause and the guard that stops an empty first render from becoming a cached baseline.

## Symptom and measured evidence

With a fake `ctx` that had no `location` at setup, the sidebar rendered a queue of 0. The same fake `ctx` with `ctx.location.project.canonical` set rendered a queue of 6. Changing nothing but the location changed the queue.

The fix adds two tests that pin the behavior. One starts from a transient cwd root and shows 0 cards, then sets `ctx.location` and shows 2 on the next poll without a reload. The other checks that an unverified root keeps both queue cards while a verified root drops a card whose file is missing or done.

## Root cause

Before the fix, `ui.js:84` and `ui_model.js:330` computed the root once at setup as `deps.root ?? projectRoot(ctx) ?? process.cwd()`. `projectRoot` in `location.js:40` reads only `ctx.location`. At first setup that value is often absent, so the plugin froze `process.cwd()` as the project root.

`readCard` in `ui_model.js:179-187` builds `<root>/docs/tasks/<id>.md` and returns null when the file is missing. `cardIsPending` at `:208-211` returns false for a null card, and `createStatusSource` at `:340` then dropped every queue item. With the wrong root all six cards vanished.

The same wrong root went to `CliStatusSource.#exec` at `ui_model.js:225-230` and was pinned as `BEACON_PROJECT_ROOT`, so the CLI query pointed at the wrong checkout. `refresh()` also cached the empty snapshot as a baseline. A manual reload reran setup when `ctx.location` existed, resolved the canonical root, and filled the queue. Polling could not recover on its own because the root and the status source were captured once.

Storage flags, `lastSeenState` and `bridge.active` were checked and are not involved.

## What changed

- `ui.js` no longer falls back to `process.cwd()` when it computes the root. `showCard` asks the status source for its current root instead of using the value captured at setup.
- `createStatusSource` re-resolves the root on each call. cwd is a transient fallback, never a verified root. A late `ctx.location` replaces it without a reload.
- `refresh()` renders a snapshot only when it has data, or when it is empty after the root is verified. It skips `applyAll` when nothing was rendered, so the empty first poll is not cached.
- The queue filter runs only after the root is verified. Before that every card from the daemon stays in the queue.
- `CliStatusSource` is recreated when the root changes, and its cwd and `BEACON_PROJECT_ROOT` follow the current root.

## What still fails safely

`readCard` still returns null for a genuinely missing card. Once the root is verified, `cardIsPending` drops only cards whose files are missing or already done. A missing card does not survive just because an earlier poll kept every card.

During an RPC outage the first empty snapshot is not cached as a baseline. Toasts wait for a confirmed poll. After two failures the panel shows the error and keeps the last rendered snapshot.

With no `location` at all, the plugin shows the cards the daemon reports and does not silently zero the queue. The CLI still runs from cwd, but no card is discarded on that basis alone.

## Code and source pointers

- T-20261124-d6417c0b, merged in pull request #2394.
- `.opencode/plugins/beacon-project/ui.js`: setup root, `showCard`, `refresh`.
- `.opencode/plugins/beacon-project/ui_model.js`: `createStatusSource`, `rootOf`, `readCard`, `cardIsPending`, `CliStatusSource.#exec`.
- `.opencode/plugins/beacon-project/location.js`: `projectRoot`.
- `.opencode/plugins/beacon-project/ui_model_test.js` and `ui_setup_test.js`: the two tests that pin the first render and the late location.
