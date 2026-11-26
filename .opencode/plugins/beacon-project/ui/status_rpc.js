// Server-safe status RPC for the beacon project plugin. The server half of
// OpenCode loads index.js, which must not import ui/ui.js or ui_runtime.js.
// ui/ui.js re-exports these names so TUI callers keep their existing imports.
export {
  BeaconStatusRpc, RUN_CHANGED_EVENT,
  createCliStatusSource, normalizeSnapshot,
} from "./ui_model.js"

import { REFRESH_MS, changedRuns, normalizeSnapshot } from "./ui_model.js"

// The daemon runs in the background, so the server half cannot read its event
// stream. Watch successful snapshot diffs instead and emit one notification
// per changed run. The key dedupes repeated emissions.
export function createRunChangeWatcher(source, { emit, deps = {} } = {}) {
  const setTimer = deps.setInterval ?? globalThis.setInterval.bind(globalThis)
  const clearTimer = deps.clearInterval ?? globalThis.clearInterval.bind(globalThis)
  const intervalMs = deps.intervalMs ?? REFRESH_MS
  let previous = null
  let timer = null
  let stopped = false
  let inFlight = false
  const tick = async () => {
    if (stopped || inFlight) return
    inFlight = true
    try {
      const next = normalizeSnapshot(await source.snapshot())
      if (stopped) return
      const changes = previous ? changedRuns(previous, next) : []
      previous = next
      for (const change of changes) await emit(change)
    } catch (error) {
      console.warn(`beacon run events: watch failed: ${error?.message ?? error}`)
    } finally { inFlight = false }
  }
  return {
    tick,
    get running() { return timer != null },
    start() {
      if (timer || stopped) return
      void tick()
      timer = setTimer(() => void tick(), intervalMs)
    },
    stop() { stopped = true; if (timer) clearTimer(timer); timer = null },
  }
}
