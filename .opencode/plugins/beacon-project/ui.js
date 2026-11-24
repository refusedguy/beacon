// Beacon status panel and run detail for the OpenCode TUI. Sources import
// lazily in setup so `node --test` can load this module without TUI peers.
import { execFile as nodeExecFile } from "node:child_process"
import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { join, resolve } from "node:path"
import { promisify } from "node:util"
import { beaconCommand } from "./mcp.js"
import { locationInfo, projectRoot } from "./location.js"

const execFileAsync = promisify(nodeExecFile)
export const REFRESH_MS = 5000
export const RPC_TIMEOUT_MS = 10000
export const BEACON_STATUS_RPC_ID = "beacon.project.status"
export const PANEL_NAME = "beacon.detail"
export const RUN_CHANGED_EVENT = "runChanged"
export const EVENT_IDLE_MS = 30000
export const RESUBSCRIBE_MIN_MS = 1000
export const RESUBSCRIBE_MAX_MS = 30000
export const TITLE_WIDTH = 18
export const REASON_WIDTH = 20
export const RECENT_LIMIT = 5
export const EMPTY_SNAPSHOT = Object.freeze({ active: [], recent: [] })

const str = (v) => (typeof v === "string" ? v : v == null ? "" : String(v))
const num = (v) => { const n = Number(v); return Number.isFinite(n) ? n : 0 }
const pick = (s, names) => { for (const n of names) { const v = s?.[n]; if (v != null) return v } }
const list = (v, fn) => (Array.isArray(v) ? v : []).map(fn)
const normalize = (value, spec) => {
  const s = value && typeof value === "object" ? value : {}
  const out = {}
  for (const [key, names, coerce] of spec) out[key] = (coerce ?? str)(pick(s, names))
  return out
}
const EVENT_SPEC = [
  ["id", ["ID", "id"], num], ["at", ["At", "at"]], ["kind", ["Kind", "kind"]],
  ["state", ["State", "state"]], ["runID", ["RunID", "run_id", "runId"]],
  ["pr", ["PRNumber", "pr", "pr_number", "prNumber"], num],
  ["sha", ["HeadSHA", "head_sha", "headSHA"]], ["reason", ["Reason", "reason"]],
]
const DECISION_SPEC = [
  ...EVENT_SPEC.filter(([k]) => k !== "sha"),
  ["taskID", ["TaskID", "task_id", "taskId"]], ["verdict", ["Verdict", "verdict"]],
  ["action", ["Action", "action"]],
  ["fields", ["Fields", "fields"], (v) => (v && typeof v === "object" && !Array.isArray(v) ? v : {})],
]
const DURATION_SPEC = [
  ["waitingCI", ["waiting_ci_seconds", "waitingCISeconds", "waitingCI"], num],
  ["running", ["running_seconds", "runningSeconds", "running"], num],
  ["blocked", ["blocked_seconds", "blockedSeconds", "blocked"], num],
]
const RUN_SPEC = [
  ["id", ["ID", "id"]], ["taskID", ["TaskID", "task_id", "taskID", "taskId"]],
  ["state", ["State", "state"], (v) => str(v) || "unknown"], ["title", ["title", "Title"]],
  ["pr", ["PRNumber", "pr", "pr_number", "prNumber"], num],
  ["sha", ["HeadSHA", "head_sha", "headSHA", "sha"]], ["branch", ["Branch", "branch"]],
  ["reason", ["Reason", "reason"]],
  ["rebaseAttempts", ["RebaseAttempts", "rebase_attempts", "rebaseAttempts"], num],
  ["fixAttempts", ["FixAttempts", "fix_attempts", "fixAttempts"], num],
  ["maxAttempts", ["MaxAttempts", "max_attempts", "maxAttempts"], num],
  ["createdAt", ["CreatedAt", "created_at", "createdAt"]],
  ["updatedAt", ["UpdatedAt", "updated_at", "updatedAt"]],
  ["durations", ["Durations", "durations"], (v) => normalize(v, DURATION_SPEC)],
  ["timeline", ["Timeline", "timeline"], (v) => list(v, (e) => normalize(e, EVENT_SPEC))],
  ["decisions", ["Decisions", "decisions"], (v) => list(v, (d) => normalize(d, DECISION_SPEC))],
]
const METRIC_KEYS = {
  dispatchCount: "dispatch_count", dispatchCycles: "dispatch_cycles", skipCount: "skip_count",
  rebaseCount: "rebase_count", fixCount: "fix_count", mergeCount: "merge_count",
  blockedCount: "blocked_count", ciWaitSeconds: "ci_wait_seconds", activeCount: "active_count",
  recentCount: "recent_count",
}
const counts = (v) => {
  const s = v && typeof v === "object" && !Array.isArray(v) ? v : {}
  const out = {}
  for (const [k, n] of Object.entries(s)) out[k] = num(n)
  return out
}
export function normalizeMetrics(v) {
  const s = v && typeof v === "object" && !Array.isArray(v) ? v : {}
  const out = { skips: counts(pick(s, ["skips", "Skips"])), blocked: counts(pick(s, ["blocked", "Blocked"])) }
  for (const [key, name] of Object.entries(METRIC_KEYS)) out[key] = num(pick(s, [name, key]))
  return out
}
export const normalizeRun = (v) => normalize(v, RUN_SPEC)
export function normalizeSnapshot(v) {
  const s = v && typeof v === "object" ? v : {}
  const out = { active: list(s.active, normalizeRun), recent: list(s.recent, normalizeRun) }
  if (s.metrics !== undefined) out.metrics = normalizeMetrics(s.metrics)
  const generatedAt = pick(s, ["generated_at", "generatedAt"])
  if (generatedAt !== undefined) out.generatedAt = generatedAt
  return out
}
export function parseSnapshot(text) {
  if (text == null || String(text).trim() === "") return {}
  try { const v = JSON.parse(String(text)); return v && typeof v === "object" && !Array.isArray(v) ? v : {} } catch { return {} }
}
// Identity includes the SHA and state, since a run often changes state without
// receiving a new commit. The key dedupes repeated emissions of the same state.
export const runSignature = (run) => [str(run.id), str(run.sha), str(run.state), str(run.updatedAt)].join("|")
export const runChangedPayload = (run) => Object.freeze({
  id: str(run.id), sha: str(run.sha), state: str(run.state), at: str(run.updatedAt), key: runSignature(run),
})
export function changedRuns(previous, next) {
  const before = new Map()
  const identity = (run) => run
  for (const run of [...list(previous?.active, identity), ...list(previous?.recent, identity)]) if (run.id) before.set(run.id, runSignature(run))
  const seen = new Set()
  const out = []
  for (const run of [...list(next?.active, identity), ...list(next?.recent, identity)]) {
    if (!run.id || seen.has(run.id)) continue
    seen.add(run.id)
    const key = runSignature(run)
    if (before.get(run.id) === key) continue
    before.set(run.id, key)
    out.push(runChangedPayload(run))
  }
  return out
}
export const eventKey = (data) => str(data?.key) || [str(data?.id), str(data?.sha), str(data?.state), str(data?.at)].join("|")
export function eventMatchesLocation(eventLocation, ownLocation) {
  const event = locationInfo({ location: eventLocation })
  const own = locationInfo({ location: ownLocation })
  if (!event || !own) return false
  if (event.workspaceID && own.workspaceID && event.workspaceID !== own.workspaceID) return false
  if (event.projectID && own.projectID && event.projectID !== own.projectID) return false
  const left = event.canonical || event.projectDirectory || event.directory
  const right = own.canonical || own.projectDirectory || own.directory
  return Boolean(left && right && resolve(left) === resolve(right))
}
export const tuiLocation = (ctx) => ctx?.location ?? ctx?.data?.location?.default?.() ?? null
export function readTitle(root, taskID, deps = {}) {
  const id = str(taskID)
  if (!id) return ""
  const path = join(root, "docs", "tasks", `${id}.md`)
  try {
    if (!(deps.existsSync ?? fsExistsSync)(path)) return id
    const body = String((deps.readFileSync ?? fsReadFileSync)(path, "utf8"))
    const front = /^---\s*\r?\n([\s\S]*?)\r?\n---/.exec(body)
    const m = front && /^title:\s*(.+?)\s*$/m.exec(front[1])
    return m ? m[1].trim().replace(/^(["'])(.*)\1$/, "$2").trim() || id : id
  } catch { return id }
}
export function enrichRuns(root, runs, deps = {}) {
  return list(runs, (run) => {
    const r = normalizeRun(run)
    if (!r.title) r.title = readTitle(root, r.taskID, deps)
    return r
  })
}

export class CliStatusSource {
  constructor({ root, deps = {} }) {
    this.root = root
    this.deps = { execFile: execFileAsync, existsSync: fsExistsSync, readFileSync: fsReadFileSync, ...deps }
  }
  #exec(base, args) {
    const [program, ...rest] = base
    return this.deps.execFile(program, [...rest, ...args], {
      cwd: this.root, encoding: "utf8", maxBuffer: 16 * 1024 * 1024,
      env: { ...process.env, BEACON_PROJECT_ROOT: this.root }, timeout: RPC_TIMEOUT_MS,
    })
  }
  async snapshot() {
    const base = this.deps.command ? this.deps.command() : this.#base()
    const snap = normalizeSnapshot(parseSnapshot((await this.#exec(base, ["status", "--snapshot"])).stdout))
    return { ...snap, active: enrichRuns(this.root, snap.active, this.deps), recent: enrichRuns(this.root, snap.recent, this.deps) }
  }
  #base() {
    const parts = beaconCommand(this.root)
    if (parts[parts.length - 1] === "mcp") parts.pop()
    return parts
  }
}
export const createCliStatusSource = (opts) => new CliStatusSource(opts)
const schema = (check) => ({ "~standard": { version: 1, vendor: "beacon", validate: (v) => {
  const issue = check(v)
  return issue ? { issues: [{ message: issue }] } : { value: v }
} } })
export const RunChangedEvent = Object.freeze({
  schema: schema((v) => {
    if (!v || typeof v !== "object" || Array.isArray(v)) return "event must be an object"
    if (typeof v.id !== "string" || !v.id) return "event.id must be a non-empty string"
    return null
  }),
})
export const BeaconStatusRpc = Object.freeze({
  id: BEACON_STATUS_RPC_ID,
  events: Object.freeze({ [RUN_CHANGED_EVENT]: RunChangedEvent }),
  methods: Object.freeze({ snapshot: Object.freeze({
    input: schema(() => null),
    output: schema((v) => {
      if (!v || typeof v !== "object" || Array.isArray(v)) return "output must be an object"
      if (!Array.isArray(v.active) || !Array.isArray(v.recent)) return "output must carry active and recent runs"
      const m = v.metrics
      if (m === undefined) return null
      if (!m || typeof m !== "object" || Array.isArray(m)) return "metrics must be an object"
      for (const [key, name] of Object.entries(METRIC_KEYS)) for (const f of [name, key]) if (m[f] !== undefined && !Number.isFinite(Number(m[f]))) return `metrics.${f} must be numeric`
      return null
    }),
  }) }),
})
export class RpcStatusSource {
  constructor(ctx, { timeout = RPC_TIMEOUT_MS } = {}) { this.ctx = ctx; this.timeout = timeout }
  async snapshot() {
    const rpc = this.ctx.client.rpc(BeaconStatusRpc)
    const location = tuiLocation(this.ctx)
    return normalizeSnapshot(await rpc.snapshot({}, { location, signal: AbortSignal.timeout(this.timeout) }))
  }
}
export function createStatusSource(ctx, { root, deps = {} } = {}) {
  const cli = new CliStatusSource({ root: root ?? projectRoot(ctx) ?? process.cwd(), deps })
  const rpc = typeof ctx?.client?.rpc === "function" ? new RpcStatusSource(ctx) : null
  return { async snapshot() {
    if (rpc) { try { return await rpc.snapshot() } catch {} }
    return cli.snapshot()
  } }
}
export const errorMessage = (error) => (error && typeof error.message === "string" ? error.message : String(error))

// Live subscriptions miss events while the client is disconnected. A failed
// subscription is dropped and retried with exponential backoff; the poll in
// setup keeps the panel fresh until the subscription recovers.
export function createRunChangeBridge(rpc, ownLocation, onEvent, deps = {}) {
  const now = deps.now ?? (() => Date.now())
  const setTimer = deps.setTimeout ?? globalThis.setTimeout.bind(globalThis)
  const clearTimer = deps.clearTimeout ?? globalThis.clearTimeout.bind(globalThis)
  let off = null
  let retry = null
  let attempts = 0
  let lastEventAt = 0
  let stopped = false
  let keys = new Set()
  const teardown = () => {
    const current = off
    off = null
    try { current?.() } catch {}
  }
  const scheduleRetry = () => {
    if (stopped || retry) return
    const delay = Math.min(RESUBSCRIBE_MIN_MS * 2 ** attempts, RESUBSCRIBE_MAX_MS)
    attempts += 1
    retry = setTimer(() => { retry = null; subscribe() }, delay)
  }
  const subscribe = () => {
    if (stopped) return
    try {
      const unsubscribe = rpc.events.on(RUN_CHANGED_EVENT, (event) => {
        if (stopped) return
        try {
          if (!eventMatchesLocation(event?.location, ownLocation)) return
          const key = eventKey(event?.data)
          if (key && keys.has(key)) return
          if (key) { keys.add(key); if (keys.size > 128) keys = new Set([...keys].slice(-64)) }
          lastEventAt = now()
          onEvent(event)
        } catch {
          teardown()
          scheduleRetry()
        }
      })
      if (typeof unsubscribe !== "function") throw new Error("events.on did not return an unsubscribe function")
      off = unsubscribe
      attempts = 0
    } catch {
      teardown()
      scheduleRetry()
    }
  }
  subscribe()
  return {
    get active() { return typeof off === "function" },
    get lastEventAt() { return lastEventAt },
    get pendingRetry() { return retry != null },
    stop() {
      stopped = true
      if (retry) clearTimer(retry)
      retry = null
      teardown()
      keys.clear()
    },
  }
}

const STATE_VIEW = Object.freeze({
  pending: { marker: "○", color: "muted", label: "PEND" },
  running: { marker: "●", color: "success", label: "RUN" },
  waiting_ci: { marker: "◐", color: "info", label: "CI" },
  needs_rebase: { marker: "◆", color: "warning", label: "REBASE" },
  needs_agent_fix: { marker: "◆", color: "warning", label: "FIX" },
  blocked: { marker: "▲", color: "error", label: "BLOCK" },
  merged: { marker: "✔", color: "success", label: "MERGED" },
  closed: { marker: "✖", color: "muted", label: "CLOSED" },
})
const stateView = (state) => STATE_VIEW[str(state).toLowerCase()] ?? { marker: "·", color: "muted", label: "?" }
export function truncate(value, width) {
  const text = str(value)
  return text.length <= width ? text : width <= 1 ? text.slice(0, width) : `${text.slice(0, width - 1)}…`
}
export function shortTaskID(value) {
  const id = str(value)
  return id ? (id.length > 10 ? id.slice(-8) : id) : "-"
}
export function elapsed(ms) {
  if (!Number.isFinite(ms) || ms < 0) return ""
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`
}
function timeOf(v) {
  if (v == null || v === "") return 0
  if (typeof v === "number") return Number.isFinite(v) ? v : 0
  const n = Number(v)
  if (Number.isFinite(n) && n > 0) return n
  const t = Date.parse(str(v))
  return Number.isFinite(t) ? t : 0
}
const ageOf = (run, now) => { const t = Math.max(timeOf(run.updatedAt), timeOf(run.createdAt)); return t ? elapsed(now - t) : "" }
const tookOf = (run) => { const a = timeOf(run.createdAt), b = timeOf(run.updatedAt); return a && b ? elapsed(b - a) : "" }
const FAILURE_CODE = /^[A-Z][A-Z0-9_]*$|^[a-z]+(?:_[a-z0-9]+)+$/
const failureReason = (reason) => (FAILURE_CODE.test(str(reason)) ? str(reason) : "")
const keep = (run, seen) => {
  const key = run.id || run.taskID || run.title
  if (!key || seen.has(key)) return false
  seen.add(key)
  return true
}
export function runLineItems(run, now = Date.now(), recent = false) {
  const v = stateView(run.state)
  const title = run.title && run.title !== run.taskID ? truncate(run.title, TITLE_WIDTH) : ""
  const name = [shortTaskID(run.taskID), title].filter(Boolean).join(" ")
  const meta = [run.pr ? `#${run.pr}` : "", recent ? tookOf(run) : ageOf(run, now)].filter(Boolean).join(" ")
  const reason = recent ? failureReason(run.reason) : truncate(run.reason, REASON_WIDTH)
  return [
    { marker: v.marker, name, status: v.label, color: v.color, bold: true, runID: run.id },
    { marker: "", name: meta, status: reason, color: "muted", runID: run.id },
  ]
}
const section = (label, count) => ({ marker: "▼", name: label, status: String(count), color: "action", bold: true })
export function snapshotLineItems(snapshot = EMPTY_SNAPSHOT, error = null, now = Date.now()) {
  const view = normalizeSnapshot(snapshot)
  const seen = new Set()
  const active = view.active.filter((r) => keep(r, seen))
  const recent = view.recent.filter((r) => keep(r, seen)).slice(0, RECENT_LIMIT)
  const items = [{ marker: "", name: "Beacon", status: "", color: "action", bold: true }]
  if (error) items.push({ marker: "", name: `[stale] ${truncate(error, REASON_WIDTH)}`, status: "", color: "warning" })
  items.push(section("Active", active.length))
  if (!active.length) items.push({ marker: "", name: "No active runs", status: "", color: "muted" })
  else active.forEach((r) => items.push(...runLineItems(r, now)))
  items.push(section("Recent", recent.length))
  if (!recent.length) items.push({ marker: "", name: "No recent runs", status: "", color: "muted" })
  else recent.forEach((r) => items.push(...runLineItems(r, now, true)))
  return items
}
export function renderSnapshotLines(snapshot, error, now) {
  return snapshotLineItems(snapshot, error, now).map((i) => [i.marker, i.name, i.status].filter((x) => x !== "" && x != null).join(" "))
}
export function metricsLine(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const m = view.metrics ?? {}
  const blocked = m.blockedCount ?? view.active.filter((r) => r.state === "blocked").length
  const stamps = [...view.active, ...view.recent].map((r) => timeOf(r.updatedAt)).filter(Boolean)
  const at = timeOf(view.generatedAt) || Math.max(0, ...stamps)
  const date = at ? new Date(at) : null
  const clock = date && Number.isFinite(date.getTime()) ? date.toISOString().slice(11, 19) : "--:--:--"
  const name = `Beacon active ${view.active.length} · blocked ${blocked} · rebase ${m.rebaseCount ?? 0} · fix ${m.fixCount ?? 0} · updated ${clock}`
  return { marker: "", name, status: "", color: "muted" }
}
export function detailLines(run, metrics, now = Date.now()) {
  if (!run) return [{ text: "No run selected", color: "muted" }]
  const v = stateView(run.state)
  const line = (text, color = "base", bold = false) => ({ text, color, bold })
  const stamp = (value) => { const t = str(value); return t.length >= 19 ? t.slice(11, 19) : t || "?" }
  const d = run.durations ?? {}
  const m = metrics ?? {}
  const out = [
    line("Run detail", "base", true),
    line(`Metrics dispatch ${m.dispatchCount ?? 0} · rebase ${m.rebaseCount ?? 0} · fix ${m.fixCount ?? 0} · merge ${m.mergeCount ?? 0} · ci_wait ${elapsed((m.ciWaitSeconds ?? 0) * 1000)}`, "muted"),
    line(`ID ${run.id || "-"} · task ${run.taskID || "-"}`, "base", true),
    line(`${v.marker} ${run.title || run.taskID || "-"}`, v.color, true),
    line(`State ${run.state} · PR ${run.pr ? `#${run.pr}` : "-"} · ${v.label}`),
    line(`Branch ${run.branch || "-"} · SHA ${run.sha ? truncate(run.sha, 12) : "-"}`, "muted"),
    line(`Created ${stamp(run.createdAt)} · Updated ${stamp(run.updatedAt)} · ${ageOf(run, now)}`, "muted"),
    line(`Durations wait_ci ${elapsed(d.waitingCI * 1000)} · running ${elapsed(d.running * 1000)} · blocked ${elapsed(d.blocked * 1000)}`, "muted"),
    line(`Attempts ${run.rebaseAttempts}/${run.maxAttempts || "?"} rebase · ${run.fixAttempts}/${run.maxAttempts || "?"} fix`, "muted"),
    line(`Reason ${run.reason || "-"}`, run.state === "blocked" ? "error" : "base"),
    line("Timeline", "base", true),
  ]
  const timeline = [...(run.timeline ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
  timeline.length ? timeline.forEach((e) => out.push(line(`  ${stamp(e.at)} ${e.kind || "event"}${e.state ? ` → ${e.state}` : ""}`, "muted"))) : out.push(line("  (none)", "muted"))
  out.push(line("Decisions", "base", true))
  const decisions = [...(run.decisions ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
  decisions.length ? decisions.forEach((d) => out.push(line(`  ${stamp(d.at)} ${d.kind || "decision"} ${d.verdict || d.action || ""}`.trimEnd(), "muted"))) : out.push(line("  (none)", "muted"))
  return out
}
export function detailCommand(open) {
  return {
    id: "beacon.run.detail", title: "Beacon run detail", description: "Open the selected Beacon run",
    group: "Beacon", bind: "ctrl+g", palette: true, slash: { name: "beacon-detail" },
    run: () => open(PANEL_NAME),
  }
}

// Builds row nodes once. Every later apply() only changes props, so focus and
// click handlers survive a refresh. Kept free of Solid so tests can drive it.
export function createRowMount(ui, { color, BOLD = 0, onRow, hoverBg }) {
  const { createElement, insert, setProp } = ui
  const cell = (grow = false) => {
    const t = createElement("text")
    setProp(t, "wrapMode", "none")
    setProp(t, "selectable", false)
    setProp(t, "truncate", true)
    setProp(t, "flexShrink", grow ? 1 : 0)
    if (grow) { setProp(t, "flexGrow", 1); setProp(t, "minWidth", 0) }
    return t
  }
  return (container, items) => {
    const slots = []
    const apply = () => {
      const rows = items()
      rows.forEach((item, i) => {
        let s = slots[i]
        if (!s) {
          const box = createElement("box")
          setProp(box, "flexDirection", "row")
          setProp(box, "gap", 1)
          setProp(box, "minWidth", 0)
          const dot = cell()
          const name = cell(true)
          const status = cell()
          insert(box, dot); insert(box, name); insert(box, status)
          insert(container, box)
          s = { box, dot, name, status }
          if (onRow) {
            setProp(box, "onMouseDown", () => { if (s.item?.runID) onRow(s.item) })
            setProp(box, "onMouseOver", () => setProp(box, "backgroundColor", hoverBg))
            setProp(box, "onMouseOut", () => setProp(box, "backgroundColor", undefined))
          }
          slots[i] = s
        }
        const isLine = item.text !== undefined
        s.item = item
        setProp(s.dot, "content", isLine ? "" : str(item.marker))
        setProp(s.dot, "fg", color(item.color))
        setProp(s.name, "content", isLine ? str(item.text) : str(item.name))
        setProp(s.name, "fg", item.color === "muted" ? color("muted") : color("base"))
        setProp(s.name, "attributes", item.bold ? BOLD : 0)
        setProp(s.status, "content", isLine ? "" : str(item.status))
        setProp(s.status, "fg", color("muted"))
        setProp(s.box, "visible", true)
      })
      slots.slice(rows.length).forEach((s) => setProp(s.box, "visible", false))
    }
    apply()
    return apply
  }
}

async function setup(ctx, deps = {}) {
  // Keep the imports literal so the OpenCode runtime plugin can rewrite them.
  // A computed import(specifier) is invisible to its rewriter, and this
  // directory has no node_modules for Bun to resolve the TUI peers.
  const defaultImport = (specifier) => {
    if (specifier === "@opentui/solid") return import("@opentui/solid")
    if (specifier === "solid-js") return import("solid-js")
    if (specifier === "@opentui/core") return import("@opentui/core")
    throw new Error(`unexpected import: ${specifier}`)
  }
  const load = deps.import ?? defaultImport
  const [{ createElement, insert, setProp }, { createSignal }] = await Promise.all([
    load("@opentui/solid"), load("solid-js"),
  ])
  let BOLD = 0
  try { ({ TextAttributes: { BOLD } } = await load("@opentui/core")) } catch {}
  const source = createStatusSource(ctx)
  const theme = ctx.theme
  const timers = {
    now: deps.now ?? (() => Date.now()),
    setTimeout: deps.setTimeout ?? globalThis.setTimeout.bind(globalThis),
    clearTimeout: deps.clearTimeout ?? globalThis.clearTimeout.bind(globalThis),
    setInterval: deps.setInterval ?? globalThis.setInterval.bind(globalThis),
    clearInterval: deps.clearInterval ?? globalThis.clearInterval.bind(globalThis),
  }
  const [snapshot, setSnapshot] = createSignal(EMPTY_SNAPSHOT)
  const [error, setError] = createSignal(null)
  const [detailItems, setDetailItems] = createSignal(detailLines(null))
  const [footerItems, setFooterItems] = createSignal([metricsLine(EMPTY_SNAPSHOT)])
  const store = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-selected-run", { initial: { id: "" } }) : [{ id: "" }, () => {}]
  const [selectedID, setSelectedID] = createSignal(store[0].id || "")
  let stopped = false
  let pollTimer
  const color = (key) => key === "base" ? theme.text.base
    : key === "muted" ? theme.text.muted
    : key === "action" ? theme.text.action.primary.base
    : theme.text.feedback[key]?.base ?? theme.text.muted
  const mount = createRowMount({ createElement, insert, setProp }, {
    color, BOLD, onRow: (item) => select(item.runID), hoverBg: theme.background.raised.high,
  })
  const rows = () => snapshotLineItems(snapshot(), error())

  function select(id) {
    if (!id) return
    setSelectedID(id)
    void store[1]((draft) => { draft.id = id }).catch(() => {})
    ctx.ui.panel.open(PANEL_NAME)
  }
  function sync() {
    const snap = snapshot()
    setFooterItems([metricsLine(snap)])
    const run = [...snap.active, ...snap.recent].find((r) => r.id === selectedID())
    setDetailItems(detailLines(run, snap.metrics, timers.now()))
  }

  // The reconciler's renderer is only active inside a slot render callback.
  // Creating nodes here reaches it before any renderer exists and throws
  // "No renderer found", so the first render callback builds every node once.
  let nodes = null
  function ensureNodes() {
    if (nodes) return nodes
    const listBox = () => {
      const box = createElement("box")
      setProp(box, "flexDirection", "column")
      return box
    }
    const sidebar = listBox()
    const footer = listBox()
    const detail = createElement("box")
    setProp(detail, "flexDirection", "column")
    setProp(detail, "flexGrow", 1)
    nodes = {
      sidebar, footer, detail,
      applySidebar: mount(sidebar, rows),
      applyFooter: mount(footer, footerItems),
      applyDetail: mount(detail, detailItems),
    }
    nodes.applySidebar()
    nodes.applyFooter()
    sync()
    nodes.applyDetail()
    return nodes
  }

  const refresh = async () => {
    if (stopped) return
    try {
      const next = await source.snapshot()
      if (stopped) return
      setSnapshot(next ?? EMPTY_SNAPSHOT)
      setError(null)
    } catch (cause) { if (!stopped) setError(errorMessage(cause)) }
    if (!nodes) return
    sync()
    nodes.applySidebar()
    nodes.applyFooter()
    nodes.applyDetail()
  }
  let rpc = null
  try { rpc = typeof ctx?.client?.rpc === "function" ? ctx.client.rpc(BeaconStatusRpc) : null } catch {}
  const bridge = rpc?.events ? createRunChangeBridge(rpc, tuiLocation(ctx), () => void refresh(), timers) : null
  void refresh()
  pollTimer = timers.setInterval(() => {
    if (stopped) return
    if (bridge?.active && timers.now() - bridge.lastEventAt < EVENT_IDLE_MS) return
    void refresh()
  }, REFRESH_MS)
  const disposers = [
    ctx.ui.slot({ append: "sidebar.content", render: () => ensureNodes().sidebar }),
    ctx.ui.slot({ append: "home.footer.status", render: () => ensureNodes().footer }),
    ctx.ui.slot({ append: "session.panel", render: (input) => {
      if (input.name !== PANEL_NAME) return null
      const n = ensureNodes()
      sync()
      n.applyDetail()
      return n.detail
    } }),
    ctx.ui.slot({ append: "app", render: () => {
      ctx.keymap.layer(() => ({ mode: "global", commands: [detailCommand((name) => ctx.ui.panel.open(name))] }))
      return null
    } }),
  ]
  return () => {
    stopped = true
    timers.clearInterval(pollTimer)
    bridge?.stop()
    disposers.forEach((d) => d?.())
  }
}

export const beaconStatusDefinition = { id: "beacon-project-status", setup }
export function defineBeaconStatusPlugin(Plugin) { return Plugin.define(beaconStatusDefinition) }
let Plugin
try { ;({ Plugin } = await import("@opencode/plugin/tui")) } catch { Plugin = { define: (d) => d } }
export default defineBeaconStatusPlugin(Plugin)
