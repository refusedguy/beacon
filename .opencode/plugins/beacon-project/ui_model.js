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
export const TITLE_WIDTH = 22
export const REASON_WIDTH = 24
export const VALUE_WIDTH = 22
export const RECENT_LIMIT = 5
// Detail sections share one dim rule so groups read as blocks, not a wall.
export const RULE = "─".repeat(30)
export const MAX_ROWS = 64
export const DECISION_LIMIT = 20
export const EMPTY_SNAPSHOT = Object.freeze({ active: [], recent: [] })

export const str = (v) => (typeof v === "string" ? v : v == null ? "" : String(v))
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
  constructor({ root, deps = {}, limits = {} }) {
    this.root = root
    this.limits = limits
    this.deps = { execFile: execFileAsync, existsSync: fsExistsSync, readFileSync: fsReadFileSync, ...deps }
  }
  #exec(base, args) {
    const [program, ...rest] = base
    return this.deps.execFile(program, [...rest, ...args], {
      cwd: this.root, encoding: "utf8", maxBuffer: 16 * 1024 * 1024,
      env: { ...process.env, BEACON_PROJECT_ROOT: this.root }, timeout: RPC_TIMEOUT_MS,
    })
  }
  #statusArgs(limits) {
    const args = ["status", "--snapshot"]
    if (limits?.recentLimit) args.push("--recent-limit", String(limits.recentLimit))
    if (limits?.decisionsLimit) args.push("--decisions-limit", String(limits.decisionsLimit))
    return args
  }
  async snapshot(limits = this.limits) {
    const base = this.deps.command ? this.deps.command() : this.#base()
    const args = this.#statusArgs(limits)
    const snap = normalizeSnapshot(parseSnapshot((await this.#exec(base, args)).stdout))
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
    input: schema((v) => {
      if (v == null) return null
      if (typeof v !== "object" || Array.isArray(v)) return "input must be an object"
      return null
    }),
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
  constructor(ctx, { timeout = RPC_TIMEOUT_MS, limits = {} } = {}) {
    this.ctx = ctx; this.timeout = timeout; this.limits = limits
  }
  async snapshot(limits = this.limits) {
    const rpc = this.ctx.client.rpc(BeaconStatusRpc)
    const location = tuiLocation(this.ctx)
    return normalizeSnapshot(await rpc.snapshot(limits ?? {}, { location, signal: AbortSignal.timeout(this.timeout) }))
  }
}
export function createStatusSource(ctx, { root, deps = {}, limits = {} } = {}) {
  const cli = new CliStatusSource({ root: root ?? projectRoot(ctx) ?? process.cwd(), deps, limits })
  const rpc = typeof ctx?.client?.rpc === "function" ? new RpcStatusSource(ctx, { limits }) : null
  return { async snapshot(input = limits) {
    if (rpc) { try { return await rpc.snapshot(input) } catch {} }
    return cli.snapshot(input)
  } }
}
export const errorMessage = (error) => (error && typeof error.message === "string" ? error.message : String(error))

