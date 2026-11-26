// Server-safe status RPC for the beacon project plugin. The server half of
// OpenCode loads index.js, which must not import ui/ui.js or ui_runtime.js.
// ui/ui.js re-exports these names so TUI callers keep their existing imports.
export {
  BeaconStatusRpc, RUN_CHANGED_EVENT,
  bodyHash, createCliStatusSource, elapsedMs, normalizeSnapshot,
  requestId, REQUEST_ID_HEADER, STARTED_AT_HEADER, timingFromContext, timingMeta,
} from "./ui_model.js"

// The watcher core lives in run_watcher.js. Re-export it so server callers keep
// their existing import path without carrying a second state machine.
export { createRunChangeWatcher } from "../run_watcher.js"
