---
id: T-d305eec7
title: Split server-safe status RPC out of the TUI entry
status: done
scope:
    - .opencode/plugins/beacon-project/ui/status_rpc.js
    - .opencode/plugins/beacon-project/index.js
    - .opencode/plugins/beacon-project/ui/ui.js
priority: 99
kind: task
require_review: llm
---

Goal: The server plugin must not import ui/ui.js or ui_runtime.js.

Evidence: OpenCode log shows `failed to load plugin` for the server half with cause `Cannot find package '@opentui/solid' imported from ui/ui_runtime.js`. index.js imports BeaconStatusRpc, createCliStatusSource and normalizeSnapshot from ./ui/ui.js, which pulls the TUI peer graph into the server bundle.

Scope: new ui/status_rpc.js, index.js, ui/ui.js.

Steps: Move BeaconStatusRpc, RUN_CHANGED_EVENT, createCliStatusSource and normalizeSnapshot plus their helpers into ui/status_rpc.js. Have index.js import them from there. Have ui/ui.js re-export them from status_rpc.js so TUI tests keep working. Do not import ui_runtime.js from the server path.

Check: OpenCode server plugin loads with no @opentui/solid error. TUI plugin tests pass. node --check passes on changed files.

Accept: No behavior change beyond import boundaries.

Open a PR, call beacon_watch_pr, do not self-merge.
