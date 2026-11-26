// Plugin settings come from userConfig under ctx.options, with the full config
// object as a fallback for older OpenCode versions. Keep every value optional
// so a missing field never changes behaviour.
const DEFAULT_STATUS_POLL_MS = 10000
const DEFAULT_SNAPSHOT_RECENT_LIMIT = 5
const DEFAULT_SNAPSHOT_DECISIONS_LIMIT = 20

export function pluginOptions(ctx) {
  const opts = ctx?.options && typeof ctx.options === "object" ? ctx.options : {}
  const config = ctx?.config && typeof ctx.config === "object" ? ctx.config : {}
  return { ...config, ...opts }
}

function boolOption(value, fallback) {
  return typeof value === "boolean" ? value : fallback
}

function positiveInt(value, fallback) {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback
}

// Zero or negative disables background polling without changing the default.
export function pollIntervalMs(value) {
  if (value === undefined || value === null || value === "") return DEFAULT_STATUS_POLL_MS
  const n = Number(value)
  if (!Number.isFinite(n)) return DEFAULT_STATUS_POLL_MS
  return n <= 0 ? 0 : Math.floor(n)
}

export function snapshotLimits(options = {}) {
  return {
    recentLimit: positiveInt(options.snapshotRecentLimit, DEFAULT_SNAPSHOT_RECENT_LIMIT),
    decisionsLimit: positiveInt(options.snapshotDecisionsLimit, DEFAULT_SNAPSHOT_DECISIONS_LIMIT),
  }
}

export function statusPanelEnabled(options = {}) {
  return boolOption(options.statusPanelEnabled, true)
}

export function watcherEnabled(options = {}) {
  return boolOption(options.watcherEnabled, true)
}
