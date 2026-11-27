// Count full normalizeSnapshot passes across one createSlots().applyAll() refresh.
//
// The load hook below wraps the real ui_model.js export. Every production
// caller of normalizeSnapshot therefore goes through the counter, so this is
// not a mirror of the call graph: it measures the call graph itself. The hook
// runs before the dynamic imports, which is why they are dynamic rather than
// static. No production file is edited.
//
// Run: node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js internal/bench/ui_slots_bench.js
//
// CI runs a bounded smoke pass with BENCH_ITERATIONS=50 and BENCH_WARMUP=0.
// Full mode uses the defaults below and is the nightly comparison baseline.
import assert from "node:assert/strict"
import { registerHooks } from "node:module"
import { performance } from "node:perf_hooks"

registerHooks({
  load(url, context, nextLoad) {
    const result = nextLoad(url, context)
    if (!url.endsWith("/ui/ui_model.js")) return result
    const source = typeof result.source === "string"
      ? result.source
      : new TextDecoder().decode(result.source)
    return {
      ...result,
      source: `${source}
export let normalizeCalls = 0
export function resetNormalizeCalls() { normalizeCalls = 0 }
const __realNormalizeSnapshot = normalizeSnapshot
normalizeSnapshot = (value) => { normalizeCalls += 1; return __realNormalizeSnapshot(value) }
`,
    }
  },
})

const { createSlots, SLOT_KEYS } = await import("../../.opencode/plugins/beacon-project/ui/ui_slots.js")
const model = await import("../../.opencode/plugins/beacon-project/ui/ui_model.js")
const {
  blockedOpenFrom, DETAIL_OPEN, SIDEBAR_OPEN, sidebarModel,
} = await import("../../.opencode/plugins/beacon-project/ui/ui_view.js")

const LIGHTER = "two normalizeSnapshot passes per sidebar and footer refresh"

function envInt(name, fallback, min) {
  const raw = process.env[name]
  if (raw === undefined || raw === "") return fallback
  const value = Number(raw)
  if (!Number.isSafeInteger(value) || value < min) {
    throw new Error(`${name} must be an integer >= ${min}; got ${JSON.stringify(raw)}`)
  }
  return value
}

const ITERATIONS = envInt("BENCH_ITERATIONS", 50, 1)
const WARMUP = envInt("BENCH_WARMUP", 0, 0)
const ROUNDS = 5
const NOW = Date.parse("2026-11-27T10:00:00Z")

// Raw wire shape, so every normalizeSnapshot pass converts all runs and queue
// cards. Three active runs, two recent runs and five queue cards are fixed.
const wireRun = (id, taskID, state, extra = {}) => ({
  ID: id, TaskID: taskID, State: state, Title: `Task ${taskID}`, PRNumber: 41,
  HeadSHA: "abcdef1234567890", Branch: `beacon/${taskID}`, Reason: "waiting CI",
  CreatedAt: "2026-11-27T09:00:00Z", UpdatedAt: "2026-11-27T09:07:00Z",
  Durations: { waiting_ci_seconds: 120, running_seconds: 15, blocked_seconds: 0 },
  Timeline: [{ at: "2026-11-27T09:05:00Z", kind: "checks_pending", state: "waiting_ci" }],
  Decisions: [{ at: "2026-11-27T09:06:00Z", kind: "reconcile.decision", verdict: "merge" }],
  Checks: [{ name: "go test", state: "success", duration_seconds: 28 }],
  ...extra,
})
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
assert.equal(FIXTURE.active.length, 3, "fixture must hold three active runs")
assert.equal(FIXTURE.recent.length, 2, "fixture must hold two recent runs")
assert.equal(FIXTURE.queue.length, 5, "fixture must hold five queue cards")

// Host doubles. createElement/insert/setProp build an inspectable tree so the
// rendered line count can be read back after each refresh.
const ui = {
  createElement: (tag) => ({ tag, props: {}, children: [] }),
  insert: (parent, child) => { parent.children.push(child) },
  setProp: (node, key, value) => { node.props[key] = value },
}
const theme = {
  text: {
    base: "base", muted: "muted", action: { primary: { base: "action" } },
    feedback: {
      error: { base: "error" }, warning: { base: "warning" },
      success: { base: "success" }, info: { base: "info" },
    },
  },
  background: { raised: { base: "panel", high: "hover" } },
}
const color = (key) => key === "base" ? theme.text.base
  : key === "muted" ? theme.text.muted
    : key === "action" ? theme.text.action.primary.base
      : key === "raised" ? theme.background.raised.high
        : key === "panel" ? theme.background.raised.base
          : theme.text.feedback[key]?.base ?? theme.text.muted
const noop = () => {}
const signals = {
  snapshot: () => FIXTURE, setSnapshot: noop,
  error: () => null, setError: noop,
  selectedID: () => "", setSelectedID: noop,
  m: () => FIXTURE, selectedRun: () => null,
}
const handlers = {
  onSidebarItem: noop, onCard: noop, onCardItem: noop, onCopy: noop, onClose: noop,
  onBack: noop, onDetailItem: noop, onDetailAction: noop, onToggle: noop,
}
const epic = { open: Object.create(null), toggle: noop }
const env = {
  ui, theme, BOLD: 1, color,
  timers: { now: () => NOW },
  flags: { currentCard: null, detailOpen: false, cardOpen: false },
  signals, handlers, epic,
  sidebarOpen: { ...SIDEBAR_OPEN },
  detailSections: { ...DETAIL_OPEN, technical: false },
}

const errs = { destroyed: 0, mount: 0, mismatch: 0 }
const slotsApi = createSlots(env)
for (const key of SLOT_KEYS) {
  try { slotsApi.ensureSlot(key) } catch { errs.mount += 1 }
}
assert.equal(errs.mount, 0, `slot mount failed for ${errs.mount} slot(s)`)

// The sidebar pool is the container's direct children. A pooled line box sets
// gap=1 once at construction and flips visible on apply, so the count is the
// number of lines the operator actually sees after a refresh.
const renderedLines = () => slotsApi.slots.sidebar.node.children
  .filter((node) => node.props?.gap === 1 && node.props?.visible === true).length
const expectedLines = sidebarModel(
  FIXTURE, null, NOW, env.sidebarOpen, epic.open, blockedOpenFrom(epic.open),
).listItems.length
assert.ok(expectedLines > 0, "fixture rendered no sidebar lines")

// A destroyed slot must be skipped, leaving only the sidebar normalization.
const footer = slotsApi.slots.footer
model.resetNormalizeCalls()
footer.node.isDestroyed = true
try {
  slotsApi.applyAll()
  if (model.normalizeCalls !== 1) errs.destroyed += 1
} catch { errs.destroyed += 1 } finally { footer.node.isDestroyed = false }

function cycle() {
  slotsApi.applyAll()
  const lines = renderedLines()
  if (lines !== expectedLines) errs.mismatch += 1
  return lines
}

model.resetNormalizeCalls()
for (let i = 0; i < WARMUP; i += 1) cycle()

const samples = []
for (let round = 0; round < ROUNDS; round += 1) {
  model.resetNormalizeCalls()
  const start = performance.now()
  let lines = 0
  for (let i = 0; i < ITERATIONS; i += 1) lines = cycle()
  const ms = performance.now() - start
  samples.push({ ms, calls: model.normalizeCalls, lines })
}

assert.equal(errs.destroyed, 0, "destroyed slot was not skipped cleanly")
assert.equal(errs.mismatch, 0, "rendered line count drifted across refreshes")
assert.ok(samples.every((s) => s.calls === ITERATIONS * 2), "expected two normalizeSnapshot passes per applyAll refresh")
assert.ok(samples.every((s) => s.lines === expectedLines), "rendered lines must match in every round")

const median = (values) => {
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2
}
const times = samples.map((s) => s.ms)
const head = samples[0]
const totalErrs = errs.destroyed + errs.mount + errs.mismatch

console.log("sidebar refresh normalization count")
console.log(`fixture: ${FIXTURE.active.length} active, ${FIXTURE.recent.length} recent, ${FIXTURE.queue.length} queue cards`)
console.log(`${ROUNDS} rounds, ${ITERATIONS} applyAll cycles per round, ${WARMUP} warmup cycles`)
console.log(`sidebar-refresh: median ${median(times).toFixed(2)} ms, range ${Math.min(...times).toFixed(2)} to ${Math.max(...times).toFixed(2)} ms`)
console.log(`sidebar-refresh-work: cycles=${ITERATIONS} normalizations=${head.calls} lines=${head.lines}`)
console.log(`sidebar-refresh-calls: ${head.calls / ITERATIONS} normalizeSnapshot calls/refresh`)
console.log(`sidebar-refresh-lines: ${expectedLines} rendered lines`)
console.log(`sidebar-refresh-errs: ${totalErrs} errors (destroyed=${errs.destroyed} mount=${errs.mount} mismatch=${errs.mismatch})`)
console.log(`sidebar-refresh-limiter: ${LIGHTER}`)
