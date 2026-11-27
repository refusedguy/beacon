// Allocation case study for the sidebar view pipeline.
//
// Mirrors the ui_view.js call graph so every normalizeSnapshot call can be
// counted without adding instrumentation to production files. This follows
// the same convention as the Go mirrors in internal/bench/models.go.
//
// Run: node internal/bench/ui_bench.js
//
// CI runs a bounded smoke pass with BENCH_ITERATIONS=50 and BENCH_WARMUP=0.
// Full mode uses the defaults below and is the nightly comparison baseline.
import assert from "node:assert/strict"
import { createHash } from "node:crypto"
import os from "node:os"
import { performance } from "node:perf_hooks"
import {
  normalizeSnapshot, RECENT_LIMIT, REASON_WIDTH, str,
} from "../../.opencode/plugins/beacon-project/ui/ui_model.js"
import {
  blockedToggleItem, renderSnapshotLines, runLineItems, SIDEBAR_OPEN, STATE_BAR_EMPTY,
  STATE_BAR_FILLED, STATE_BAR_KEYS, STATE_BAR_WIDTH,
} from "../../.opencode/plugins/beacon-project/ui/ui_view.js"
import {
  baseIncidentItem, daemonAlertItem, dispatchItem, queueRows, readyCount,
  spacerItem, truncate,
} from "../../.opencode/plugins/beacon-project/ui/ui_alerts.js"

// STATE_BAR_COLOR and STATE_BAR_LETTER are private to ui_view.js. These two
// maps are copied verbatim so the mirror builds the same bar.
const STATE_BAR_COLOR = Object.freeze({
  pending: "muted", running: "success", waiting_ci: "info", needs_rebase: "warning",
  needs_agent_fix: "warning", blocked: "error", merged: "action", closed: "muted",
})
const STATE_BAR_LETTER = Object.freeze({
  pending: "P", running: "R", waiting_ci: "C", needs_rebase: "E",
  needs_agent_fix: "F", blocked: "B", merged: "M", closed: "X",
})

const SEED = 20261125
const NOW = Date.parse("2026-11-25T10:00:00Z")
const ROUNDS = 5

// envInt reads a bounded non-negative integer override. A missing or empty
// value uses the full-mode default; anything else must parse as an integer at
// or above min so a typo cannot silently shrink the nightly comparison.
function envInt(name, fallback, min) {
  const raw = process.env[name]
  if (raw === undefined || raw === "") return fallback
  const value = Number(raw)
  if (!Number.isSafeInteger(value) || value < min) {
    throw new Error(`${name} must be an integer >= ${min}; got ${JSON.stringify(raw)}`)
  }
  return value
}

const ITERATIONS = envInt("BENCH_ITERATIONS", 2000, 1)
const WARMUP = envInt("BENCH_WARMUP", 0, 0)

// The hash pins the fixture shape. Any edit to the seed or fixture rows
// changes the hash and fails the run before the harness prints a number.
const EXPECTED_FIXTURE_HASH = "8b649f17d1b4b680"
function hashFixture(fixture) {
  return createHash("sha256").update(JSON.stringify(fixture)).digest("hex").slice(0, 16)
}

const wireRun = (id, taskID, state, extra = {}) => ({
  ID: id, TaskID: taskID, State: state, Title: `Task ${taskID}`, PRNumber: 41,
  HeadSHA: "abcdef1234567890", Branch: `beacon/${taskID}`, Reason: "waiting CI",
  CreatedAt: "2026-11-25T09:00:00Z", UpdatedAt: "2026-11-25T09:07:00Z",
  Durations: { waiting_ci_seconds: 120, running_seconds: 15, blocked_seconds: 0 },
  Timeline: [{ at: "2026-11-25T09:05:00Z", kind: "checks_pending", state: "waiting_ci" }],
  Decisions: [{ at: "2026-11-25T09:06:00Z", kind: "reconcile.decision", verdict: "merge" }],
  Checks: [{ name: "go test", state: "success", duration_seconds: 28 }],
  ...extra,
})

// Raw wire shape, so normalization converts every run and queue card on each
// call. The fixture fires every branch of sidebarModel with default flags.
const FIXTURE = Object.freeze({
  active: [
    wireRun("run-a", "T-A", "running"),
    wireRun("run-b", "T-B", "waiting_ci"),
    wireRun("run-c", "T-C", "blocked", { Reason: "workspace: conflict: refusing to remove dirty worktree" }),
  ],
  recent: [
    wireRun("run-d", "T-D", "merged", { Reason: "automatic rebase" }),
    wireRun("run-e", "T-E", "closed", { Reason: "pull request closed" }),
  ],
  queue: [
    { ID: "T-EPIC", Title: "Harness epic", Status: "ready", Priority: 9, Kind: "epic" },
    { ID: "T-EPIC-01", Title: "Child one", Status: "available", Priority: 8 },
    { ID: "T-EPIC-02", Title: "Child two", Status: "available", Priority: 8 },
    { ID: "T-B1", Title: "Blocked one", Status: "waiting_deps", Priority: 5, DependsOn: ["T-BLOCK-A"] },
    { ID: "T-B2", Title: "Blocked two", Status: "waiting_deps", Priority: 4, DependsOn: ["T-BLOCK-A"] },
  ],
  metrics: { dispatch_count: 25, merge_count: 12 },
  daemon: { State: "alive", PID: 123, HeartbeatAgeSeconds: 2 },
  base_incident: { ID: "INC-1", BaseBranch: "main", FailedChecks: "[1,2]", Attempts: 2 },
})

const fixtureHashValue = hashFixture({ seed: SEED, fixture: FIXTURE })
if (fixtureHashValue !== EXPECTED_FIXTURE_HASH) {
  throw new Error(`fixture hash drift: expected ${EXPECTED_FIXTURE_HASH}, got ${fixtureHashValue}`)
}

let normalizeCalls = 0
const countNormalize = (value) => { normalizeCalls += 1; return normalizeSnapshot(value) }

// Work on an already-normalized view. No normalization happens below this
// line. These three helpers are what the fixed ui_view.js calls internally.
function activeFromView(view) {
  const seen = new Set()
  return view.active.filter((run) => {
    if (!run.id || seen.has(run.id)) return false
    seen.add(run.id)
    return true
  })
}
function recentFromViewWith(view, active) {
  const activeTasks = new Set(active.map((r) => str(r.taskID)).filter(Boolean))
  const seenTasks = new Set()
  const seenIDs = new Set()
  return view.recent.filter((run) => {
    const task = str(run.taskID)
    if (!run.id || seenIDs.has(run.id) || activeTasks.has(task) || seenTasks.has(task)) return false
    seenIDs.add(run.id)
    if (task) seenTasks.add(task)
    return true
  }).slice(0, RECENT_LIMIT)
}
function recentFromView(view) { return recentFromViewWith(view, activeFromView(view)) }
function stateBarFromView(view, active, recent) {
  const counts = Object.fromEntries(STATE_BAR_KEYS.map((key) => [key, 0]))
  for (const run of [...active, ...recent]) {
    const state = str(run.state).toLowerCase()
    if (Object.hasOwn(counts, state)) counts[state] += 1
  }
  const total = STATE_BAR_KEYS.reduce((sum, key) => sum + counts[key], 0)
  const filled = Math.min(total, STATE_BAR_WIDTH)
  const share = total ? filled / total : 0
  const exact = STATE_BAR_KEYS.map((key) => counts[key] * share)
  const cells = exact.map(Math.floor)
  let spare = filled - cells.reduce((sum, n) => sum + n, 0)
  const byFraction = exact.map((value, i) => ({ i, fraction: value - cells[i] })).sort((a, b) => b.fraction - a.fraction)
  for (let i = 0; spare > 0; i += 1, spare -= 1) cells[byFraction[i].i] += 1
  const segments = STATE_BAR_KEYS.map((key, i) => ({ text: STATE_BAR_FILLED.repeat(cells[i]), color: STATE_BAR_COLOR[key] }))
  segments.push({ text: STATE_BAR_EMPTY.repeat(STATE_BAR_WIDTH - filled), color: "muted" })
  const bar = segments.map((s) => s.text).join("")
  const mergeTotal = view.metrics?.mergeCount ?? counts.merged
  const legend = STATE_BAR_KEYS.map((key) => `${STATE_BAR_LETTER[key]}${key === "merged" ? mergeTotal : counts[key]}`).join(" ")
  const parts = []
  if (!active.length) parts.push("0 active")
  if (counts.running) parts.push(`${counts.running} running`)
  if (counts.waiting_ci) parts.push(`${counts.waiting_ci} in CI`)
  if (counts.blocked) parts.push(`${counts.blocked} blocked`)
  if (counts.merged) parts.push(`${counts.merged} done`)
  const summary = parts.length ? parts.join(" · ") : "no runs"
  return {
    segments, bar, counts, mergeTotal, total, filled, legend, summary,
    content: `states ${bar} ${legend}`,
    fg: counts.blocked > 0 ? "error" : "base",
  }
}

const sectionItem = (label, count, key, open) => ({
  marker: open ? "▼" : "▶", name: label, status: String(count),
  color: "action", bold: true, statusColor: "muted", sectionKey: key,
})

// The shared render step. Mirrors sidebarModel lines 238-270 with the three
// derived values supplied by whichever side is under test.
function compose(view, bar, active, recent, now) {
  const error = null
  const open = SIDEBAR_OPEN
  const epicOpen = null
  const blockedOpen = null
  const items = [
    { marker: "", name: "Beacon", status: bar.summary, color: "action", bold: true },
    { marker: "", name: bar.legend, status: "", color: "muted" },
  ]
  if (error) items.push({ marker: "", name: `[stale] ${truncate(error, REASON_WIDTH)}`, status: "", color: "warning" })
  const listItems = []
  for (const alert of [daemonAlertItem(view.daemon), baseIncidentItem(view.baseIncident)]) {
    if (alert) listItems.push(alert)
  }
  const ready = readyCount(view.queue)
  listItems.push(sectionItem("Active", active.length, "active", open.active !== false))
  if (open.active !== false) {
    if (!active.length && ready === 0) {
      listItems.push({ marker: "", name: "Idle · no ready tasks", status: "", color: "muted" })
    } else {
      if (!active.length) listItems.push({ marker: "", name: "No active runs", status: "", color: "muted" })
      active.forEach((run) => listItems.push(...runLineItems(run, now)))
      listItems.push(dispatchItem(ready))
    }
  }
  listItems.push(sectionItem("Queue", view.queue.length, "queue", open.queue === true))
  if (open.queue === true) {
    const toggle = blockedOpen != null ? blockedToggleItem(view.queue, blockedOpen) : null
    if (toggle) listItems.push(toggle)
    listItems.push(...queueRows(view.queue, epicOpen, blockedOpen))
    listItems.push(spacerItem())
  }
  listItems.push(sectionItem("Recent", recent.length, "recent", open.recent !== false))
  if (open.recent !== false) {
    if (!recent.length) listItems.push({ marker: "", name: "No recent runs", status: "", color: "muted" })
    recent.forEach((run) => listItems.push(...runLineItems(run, now, true)))
  }
  return { ...bar, items: [...items, ...listItems], listItems, active, recent, view, queue: view.queue, ready }
}

// BEFORE: every helper normalizes its own input. One sidebarModel call
// normalizes the snapshot eight times: once at entry, four inside the bar,
// once for active and twice for recent.
function beforeActive(snapshot) { return activeFromView(countNormalize(snapshot)) }
// The original recentRuns normalizes its input and then calls activeRuns,
// which normalizes again. Keep both calls so the count matches production.
function beforeRecent(snapshot) {
  const view = countNormalize(snapshot)
  return recentFromViewWith(view, beforeActive(view))
}
function beforeBar(snapshot) {
  const view = countNormalize(snapshot)
  const active = beforeActive(view)
  const recent = beforeRecent(view)
  return stateBarFromView(view, active, recent)
}
function beforeModel(snapshot, now) {
  const view = countNormalize(snapshot)
  const bar = beforeBar(view)
  const active = beforeActive(view)
  const recent = beforeRecent(view)
  return compose(view, bar, active, recent, now)
}

// AFTER: the public entry point normalizes once and hands the view to helpers
// that do not normalize again.
function afterModel(snapshot, now) {
  const view = countNormalize(snapshot)
  const active = activeFromView(view)
  const recent = recentFromView(view)
  const bar = stateBarFromView(view, active, recent)
  return compose(view, bar, active, recent, now)
}

const linesOf = (model) => model.items
  .map((i) => [i.marker, i.name, i.status].filter((x) => x !== "" && x != null).join(" "))

// Correctness gates. Both sides must render the same fixture, and the after
// side must match the production renderer exactly.
const beforeLines = linesOf(beforeModel(FIXTURE, NOW))
const afterLines = linesOf(afterModel(FIXTURE, NOW))
const productionLines = renderSnapshotLines(FIXTURE, null, NOW)
assert.deepEqual(beforeLines, afterLines, "removing normalizations changed the rendered lines")
assert.deepEqual(afterLines, productionLines, "the after mirror differs from production ui_view.js")
assert.ok(afterLines.length > 0, "the fixture rendered no lines")

// Warm up the mirrored and production renders outside the timed region when
// the caller asks for it. The Go harness does the same before its timed pass.
for (let i = 0; i < WARMUP; i += 1) {
  beforeModel(FIXTURE, NOW)
  afterModel(FIXTURE, NOW)
  renderSnapshotLines(FIXTURE, null, NOW)
}

function measure(fn) {
  normalizeCalls = 0
  const start = performance.now()
  let sink = 0
  for (let i = 0; i < ITERATIONS; i += 1) sink += fn()
  const ms = performance.now() - start
  return { ms, calls: normalizeCalls, sink }
}

const preparedView = normalizeSnapshot(FIXTURE)
const prepared = {
  view: preparedView,
  active: activeFromView(preparedView),
  recent: recentFromView(preparedView),
  bar: stateBarFromView(preparedView, activeFromView(preparedView), recentFromView(preparedView)),
}
const sides = {
  before: () => beforeModel(FIXTURE, NOW).items.length,
  after: () => afterModel(FIXTURE, NOW).items.length,
  // The floor this change cannot cross: item building, queueRows, sorting and
  // string joins with all normalization done outside the timed region.
  renderOnly: () => compose(prepared.view, prepared.bar, prepared.active, prepared.recent, NOW).items.length,
  // Production does not use the counting wrapper. Its calls are reported by
  // the mirrors above, but its time must track the after side once the fix
  // lands in ui_view.js.
  production: () => renderSnapshotLines(FIXTURE, null, NOW).length,
}
const samples = { before: [], after: [], renderOnly: [], production: [] }
for (let round = 0; round < ROUNDS; round += 1) {
  const order = round % 2 === 0
    ? ["before", "after", "renderOnly", "production"]
    : ["production", "renderOnly", "after", "before"]
  for (const side of order) samples[side].push(measure(sides[side]))
}

const median = (values) => {
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2
}
const range = (values) => [Math.min(...values), Math.max(...values)]
const summary = (side) => {
  const times = samples[side].map((s) => s.ms)
  const calls = samples[side].map((s) => s.calls)
  return {
    ms: median(times), min: range(times)[0], max: range(times)[1],
    callsPerRender: median(calls) / ITERATIONS,
    sink: samples[side].every((s) => s.sink > 0),
  }
}
const before = summary("before")
const after = summary("after")
const renderOnly = summary("renderOnly")
const production = summary("production")
const savedMs = before.ms - after.ms
const normalizeShare = savedMs / before.ms
const perNormalizeMs = savedMs / ((before.callsPerRender - after.callsPerRender) * ITERATIONS)
const afterNormalizeMs = after.ms - renderOnly.ms
const afterNormalizeShare = afterNormalizeMs / after.ms

console.log("sidebar allocation case study")
console.log(`seed: ${SEED}`)
console.log(`fixture hash: ${fixtureHashValue}`)
console.log(`host: ${os.cpus().length} cores, load average ${os.loadavg().map((n) => n.toFixed(2)).join(" ")}`)
console.log(`fixture: ${FIXTURE.active.length} active, ${FIXTURE.recent.length} recent, ${FIXTURE.queue.length} queue cards`)
console.log(`${ROUNDS} alternating rounds per side, ${ITERATIONS} renders per round, ${WARMUP} warmup renders`)
console.log(`rendered lines: ${beforeLines.length} (before == after == production)`)
console.log(`work counted in every round: ${before.sink && after.sink ? "yes" : "NO"}`)
console.log(`before: median ${before.ms.toFixed(2)} ms, range ${before.min.toFixed(2)} to ${before.max.toFixed(2)} ms, ${before.callsPerRender} normalize calls/render`)
console.log(`after:  median ${after.ms.toFixed(2)} ms, range ${after.min.toFixed(2)} to ${after.max.toFixed(2)} ms, ${after.callsPerRender} normalize calls/render`)
console.log(`render only: median ${renderOnly.ms.toFixed(2)} ms, range ${renderOnly.min.toFixed(2)} to ${renderOnly.max.toFixed(2)} ms, ${renderOnly.callsPerRender} normalize calls/render`)
console.log(`production: median ${production.ms.toFixed(2)} ms, range ${production.min.toFixed(2)} to ${production.max.toFixed(2)} ms`)
console.log(`normalize work removed: ${(normalizeShare * 100).toFixed(1)}% of the before median`)
console.log(`cost per removed normalize: ${perNormalizeMs.toFixed(4)} ms`)
console.log(`limiter: one normalizeSnapshot at the entry point is ${(afterNormalizeShare * 100).toFixed(1)}% of the after median; renderOnly is ${(renderOnly.ms / before.ms * 100).toFixed(1)}% of the before median`)
// One canonical row for the measured plan. Work is the sink flag the human
// report already checks; the harness aborts on a mismatch, so errs is zero.
console.log(`beacon-perf: ${JSON.stringify({
  key: "tui-view",
  median_ms: production.ms,
  min_ms: production.min,
  max_ms: production.max,
  work: before.sink && after.sink ? 1 : 0,
  errs: 0,
  limiter: "normalizeSnapshot + render",
})}`)
