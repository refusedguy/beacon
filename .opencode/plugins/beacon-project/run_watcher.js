// Import the model directly so the server half can load this module without
// pulling in the TUI runtime through ui/ui.js.
import { REFRESH_MS, changedRuns, normalizeSnapshot } from "./ui/ui_model.js"

const pick = (s, names) => { for (const n of names) { const v = s?.[n]; if (v != null) return v } }
const str = (v) => (typeof v === "string" ? v : v == null ? "" : String(v))

// changedRuns reads exactly four fields from each active/recent run. Read the
// same aliases as RUN_SPEC so a raw fixture and an already normalized source
// produce the same key. state falls back to "unknown" just like runSignature.
const changeKey = (run) => {
  const s = run && typeof run === "object" ? run : {}
  const id = str(pick(s, ["ID", "id"]))
  const sha = str(pick(s, ["HeadSHA", "head_sha", "headSHA", "sha"]))
  const state = str(pick(s, ["State", "state"])) || "unknown"
  const at = str(pick(s, ["UpdatedAt", "updated_at", "updatedAt"]))
  return `${id}|${sha}|${state}|${at}`
}

// Stringify only the ordered run identities the watcher can emit. Timelines,
// decisions, checks, metrics and the queue never change an emitted event, so
// they are not walked. Keeping every run in order is conservative: a different
// key can trigger one extra normalization, but an equal key always means the
// same changedRuns result for the same previous snapshot.
export const snapshotFingerprint = (raw) => {
  const s = raw && typeof raw === "object" ? raw : {}
  const keys = []
  for (const section of ["active", "recent"]) {
    for (const run of Array.isArray(s[section]) ? s[section] : []) keys.push(changeKey(run))
  }
  return JSON.stringify(keys)
}

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
