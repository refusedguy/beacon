// Import the model directly so the server half can load this module without
// pulling in the TUI runtime through ui/ui.js.
import { REFRESH_MS, changedRuns, normalizeSnapshot } from "./ui/ui_model.js"

// Serializing the raw snapshot is much cheaper than rebuilding the normalized
// runs, queue and metrics on an idle tick. An unchanged fingerprint means the
// previous normalized snapshot and its diff are still valid.
export const snapshotFingerprint = (raw) => JSON.stringify(raw ?? null)

// The Beacon daemon runs in the background, so this plugin cannot read the
// reconciler's event stream. Watch successful snapshot diffs instead and emit
// one notification per changed run. The key dedupes repeated emissions.
export function createRunChangeWatcher(source, { emit, deps = {} } = {}) {
  const setTimer = deps.setInterval ?? globalThis.setInterval.bind(globalThis)
  const clearTimer = deps.clearInterval ?? globalThis.clearInterval.bind(globalThis)
  const intervalMs = deps.intervalMs ?? REFRESH_MS
  const normalize = deps.normalizeSnapshot ?? normalizeSnapshot
  const fingerprint = deps.fingerprint ?? snapshotFingerprint
  let previous = null
  let previousKey = null
  let timer = null
  let stopped = false
  let inFlight = false
  const tick = async () => {
    if (stopped || inFlight) return
    inFlight = true
    try {
      const raw = await source.snapshot()
      if (stopped) return
      const key = fingerprint(raw)
      if (key === previousKey) return
      if (stopped) return
      const next = normalize(raw)
      if (stopped) return
      const changes = previous ? changedRuns(previous, next) : []
      previous = next
      previousKey = key
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
