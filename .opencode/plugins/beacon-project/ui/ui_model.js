// Data model, CLI transport and RPC schema for the Beacon panels. Sources load
// TUI peers lazily in setup so `node --test` can import this module alone.
import { execFile as nodeExecFile } from "node:child_process"
import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { join, relative, resolve } from "node:path"
import { promisify } from "node:util"
import { beaconCommand } from "../mcp/mcp.js"
import { locationInfo, projectRoot } from "../state/location.js"
import { cliEnv } from "../state/session_identity.js"
const execFileAsync = promisify(nodeExecFile)
export const REFRESH_MS = 5000
export const RPC_TIMEOUT_MS = 10000
export const BEACON_STATUS_RPC_ID = "beacon.project.status"
export const PANEL_NAME = "beacon.detail"
export const RUN_CHANGED_EVENT = "runChanged"
export const EVENT_IDLE_MS = 30000
export const RESUBSCRIBE_MIN_MS = 1000
export const RESUBSCRIBE_MAX_MS = 30000
export const REASON_WIDTH = 40
export const RECENT_LIMIT = 5
export const DECISION_LIMIT = 20
export const EMPTY_SNAPSHOT = Object.freeze({ active: [], recent: [], queue: [] })

const bool = (v) => v === true || v === 1 || v === "true", raw = (v) => (v == null ? "" : v)
export const failedCheckCount = (v) => {
  if (Array.isArray(v)) return v.length
  if (typeof v === "string" && v.trim().startsWith("[")) {
    try { const parsed = JSON.parse(v); if (Array.isArray(parsed)) return parsed.length } catch {}
  }
  if (v == null || v === "") return "?"
  const n = Number(v)
  return Number.isFinite(n) ? n : raw(v)
}

export const str = (v) => (typeof v === "string" ? v : v == null ? "" : String(v))
const num = (v) => { const n = Number(v); return Number.isFinite(n) ? n : 0 }, pick = (s, names) => { for (const n of names) { const v = s?.[n]; if (v != null) return v } }
const list = (v, fn) => (Array.isArray(v) ? v : []).map(fn)
const normalize = (value, spec) => {
  const s = value && typeof value === "object" ? value : {}
  const out = {}
  for (const [key, names, coerce] of spec) out[key] = (coerce ?? str)(pick(s, names), s)
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
const CHECK_SPEC = [
  ["name", ["Name", "name"]], ["state", ["State", "state"]],
  ["durationSeconds", ["duration_seconds", "durationSeconds"], num],
]
const QUEUE_SPEC = [
  ["id", ["ID", "id"]], ["title", ["Title", "title"]], ["kind", ["Kind", "kind"]],
  ["priority", ["Priority", "priority"], raw],
  ["status", ["Status", "status"], (v) => str(v) || "ready"],
  ["dependsOn", ["DependsOn", "depends_on", "dependsOn"], (v) => list(v, str)],
]
const BASE_INCIDENT_SPEC = [
  ["id", ["ID", "id"]], ["baseBranch", ["BaseBranch", "base_branch", "baseBranch"]],
  ["failedChecks", ["FailedChecks", "failed_checks", "failedChecks"], failedCheckCount],
  ["fixerRunID", ["FixerRunID", "fixer_run_id", "fixerRunID"]],
  ["attempts", ["Attempts", "attempts"], num],
]
const DAEMON_SPEC = [
  ["state", ["State", "state"]], ["pid", ["PID", "pid"], num],
  ["heartbeatAgeSeconds", ["HeartbeatAgeSeconds", "heartbeat_age_seconds", "heartbeatAgeSeconds"], num],
  ["error", ["Error", "error"]],
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
  ["checks", ["Checks", "checks"], (v, s) => Object.defineProperty(list(v, (c) => normalize(c, CHECK_SPEC)), "ciWallSeconds", { value: num(pick(s, ["ci_wall_seconds", "ciWallSeconds"]) ?? pick(v, ["ciWallSeconds"])), writable: true, configurable: true })],
  ["ciWallSeconds", ["ci_wall_seconds", "ciWallSeconds"], num],
  ["sessionID", ["SessionID", "session_id", "sessionID"]],
  ["isFixer", ["IsFixer", "is_fixer", "isFixer"], bool],
  ["blockedIncidentID", ["BlockedIncidentID", "blocked_incident_id", "blockedIncidentID"]],
  ["prURL", ["PRURL", "pr_url", "prURL"]],
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
  const out = { active: list(s.active, normalizeRun), recent: list(s.recent, normalizeRun), queue: [] }
  // A queued card shares its task ID with a run. Keep that run on the queue row
  // so the card header can show its PR and total time without another lookup.
  const byTask = new Map([...out.recent, ...out.active].filter((r) => r.taskID).map((r) => [r.taskID, r]))
  out.queue = list(pick(s, ["queue", "Queue"]), (item) => {
    const row = normalize(item, QUEUE_SPEC)
    return byTask.has(row.id) ? Object.assign(row, { run: byTask.get(row.id) }) : row
  })
  const incident = pick(s, ["base_incident", "baseIncident", "BaseIncident"]); if (incident != null) out.baseIncident = normalize(incident, BASE_INCIDENT_SPEC)
  const daemon = pick(s, ["daemon", "Daemon"]); if (daemon != null) out.daemon = normalize(daemon, DAEMON_SPEC)
  if (s.metrics !== undefined) out.metrics = normalizeMetrics(s.metrics)
  const generatedAt = pick(s, ["generated_at", "generatedAt"]); if (generatedAt !== undefined) out.generatedAt = generatedAt
  return out
}
export function parseSnapshot(text) {
  if (text == null || String(text).trim() === "") return {}
  try { const v = JSON.parse(String(text)); return v && typeof v === "object" && !Array.isArray(v) ? v : {} } catch { return {} }
}
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
export const CARD_BODY_LINES = 80
const unquote = (v) => v.trim().replace(/^(["'])(.*)\1$/, "$2").trim()

// Read a card from docs/tasks. A missing or unreadable file returns null so
// the caller fails closed instead of showing a stale SQLite queue row.
export function readCard(root, id, deps = {}) {
  const cardID = str(id)
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(cardID)) return null
  const path = join(root, "docs", "tasks", `${cardID}.md`)
  let body
  try {
    if (!(deps.existsSync ?? fsExistsSync)(path)) return null
    body = String((deps.readFileSync ?? fsReadFileSync)(path, "utf8"))
  } catch { return null }
  const match = /^---\s*\r?\n([\s\S]*?)\r?\n---\s*\r?\n?/.exec(body)
  const meta = Object.create(null)
  let key = ""
  for (const line of (match?.[1] ?? "").split(/\r?\n/)) {
    const item = /^\s+-\s+(.+?)\s*$/.exec(line)
    if (item && key) { (Array.isArray(meta[key]) ? meta[key] : (meta[key] = [])).push(unquote(item[1])); continue }
    const field = /^([A-Za-z_][\w-]*):\s*(.*?)\s*$/.exec(line)
    if (field) { key = field[1].toLowerCase(); meta[key] = field[2] ? unquote(field[2]) : [] }
  }
  const text = (name) => { const v = meta[name]; return Array.isArray(v) ? v.join(", ") : str(v) }
  const rest = match ? body.slice(match[0].length) : body
  const field = (name) => text(name) || new RegExp(`^${name}:\\s*(.+?)\\s*$`, "im").exec(rest)?.[1] || ""
  return {
    id: cardID, title: text("title") || cardID, priority: text("priority"), status: text("status"),
    kind: text("kind") || "task", scope: field("scope"), depends: field("depends") || field("depends_on"), meta,
    parent: (field("epic") || (/^beacon\/(.+)$/.exec(field("base_branch"))?.[1] ?? "")).trim(),
    path, relativePath: relative(root, path), body: rest.replace(/^\r?\n/, "").split(/\r?\n/).slice(0, CARD_BODY_LINES).join("\n"),
  }
}
export const readTitle = (root, taskID, deps = {}) => readCard(root, taskID, deps)?.title || str(taskID)
export const cardIsPending = (root, id, deps = {}) => {
  const card = readCard(root, id, deps)
  return Boolean(card) && !["done", "dropped"].includes(str(card.status).toLowerCase())
}
export function enrichRuns(root, runs, deps = {}) {
  return list(runs, (run) => {
    const r = normalizeRun(run)
    if (!r.title) r.title = readTitle(root, r.taskID, deps)
    return r
  })
}

export class CliStatusSource {
  constructor({ root, deps = {}, limits = {}, sessionID, model }) {
    Object.assign(this, { root, limits, sessionID, model })
    this.deps = { execFile: execFileAsync, existsSync: fsExistsSync, readFileSync: fsReadFileSync, ...deps }
  }
  #exec(base, args) {
    const [program, ...rest] = base
    return this.deps.execFile(program, [...rest, ...args], {
      cwd: this.root, encoding: "utf8", maxBuffer: 16 * 1024 * 1024,
      env: cliEnv(this.root, this), timeout: RPC_TIMEOUT_MS,
    })
  }
  #statusArgs(limits) {
    const args = ["status", "--snapshot"]
    if (limits?.recentLimit) args.push("--recent-limit", String(limits.recentLimit))
    if (limits?.decisionsLimit) args.push("--decisions-limit", String(limits.decisionsLimit))
    return args
  }
  #command() {
    return typeof this.deps.command === "function" ? this.deps.command() : this.#base()
  }
  async snapshot(limits = this.limits) {
    const snap = normalizeSnapshot(parseSnapshot((await this.#exec(this.#command(), this.#statusArgs(limits))).stdout))
    return { ...snap, active: enrichRuns(this.root, snap.active, this.deps), recent: enrichRuns(this.root, snap.recent, this.deps) }
  }
  async control({ action, runID, sha, message } = {}) {
    const args = ["control", "--action", str(action), "--run-id", str(runID), "--json"]
    if (sha) args.push("--sha", str(sha))
    if (message) args.push("--message", str(message))
    return parseSnapshot((await this.#exec(this.#command(), args)).stdout)
  }
  async dispatch({ n = 1 } = {}) {
    const count = num(n) > 0 ? Math.floor(num(n)) : 1
    return parseSnapshot((await this.#exec(this.#command(), ["dispatch", String(count)])).stdout)
  }
  #base() {
    const parts = beaconCommand(this.root)
    if (parts[parts.length - 1] === "mcp") parts.pop()
    return parts
  }
}
export const createCliStatusSource = (opts) => new CliStatusSource(opts)
const schema = (check) => ({ "~standard": { version: 1, vendor: "beacon", validate: (v) => {
  const issue = check(v); return issue ? { issues: [{ message: issue }] } : { value: v }
} } })
const objectInput = (check) => schema((v) => v == null ? null : (typeof v !== "object" || Array.isArray(v)) ? "input must be an object" : check ? check(v) : null)
export const RunChangedEvent = Object.freeze({
  schema: schema((v) => {
    if (!v || typeof v !== "object" || Array.isArray(v)) return "event must be an object"
    if (typeof v.id !== "string" || !v.id) return "event.id must be a non-empty string"
    return null
  }),
})
// One vocabulary for the RPC schema, the CLI bridge and the panel. It mirrors
// core.ControlActions; changing it must change the Go vocabulary too.
export const CONTROL_ACTIONS = Object.freeze(["approve", "unblock", "cancel", "retry", "nudge", "watch"])
export const BeaconStatusRpc = Object.freeze({
  id: BEACON_STATUS_RPC_ID,
  events: Object.freeze({ [RUN_CHANGED_EVENT]: RunChangedEvent }),
  methods: Object.freeze({
    snapshot: Object.freeze({
      input: objectInput(),
      output: schema((v) => {
        if (!v || typeof v !== "object" || Array.isArray(v)) return "output must be an object"
        if (!Array.isArray(v.active) || !Array.isArray(v.recent)) return "output must carry active and recent runs"
        const m = v.metrics
        if (m === undefined) return null
        if (!m || typeof m !== "object" || Array.isArray(m)) return "metrics must be an object"
        for (const [key, name] of Object.entries(METRIC_KEYS)) for (const f of [name, key]) if (m[f] !== undefined && !Number.isFinite(Number(m[f]))) return `metrics.${f} must be numeric`
        return null
      }),
    }),
    control: Object.freeze({
      input: objectInput((v) => {
        if (!CONTROL_ACTIONS.includes(str(v.action))) return `action must be one of ${CONTROL_ACTIONS.join(", ")}`
        if (!str(v.runID)) return "runID must be a non-empty string"
        if (v.sha !== undefined && typeof v.sha !== "string") return "sha must be a string"
        if (v.message !== undefined && typeof v.message !== "string") return "message must be a string"
        if (str(v.action) === "nudge" && !str(v.message).trim()) return "nudge needs a message"
        return null
      }),
      output: schema((v) => (!v || typeof v !== "object" || Array.isArray(v) ? "output must be an object" : null)),
    }),
    dispatch: Object.freeze({
      input: objectInput((v) => (v.n !== undefined && (!Number.isFinite(Number(v.n)) || Number(v.n) < 1) ? "n must be a positive number" : null)),
      output: schema((v) => (!v || typeof v !== "object" || Array.isArray(v) ? "output must be an object" : null)),
    }),
  }),
})
export class RpcStatusSource {
  constructor(ctx, { timeout = RPC_TIMEOUT_MS, limits = {} } = {}) {
    this.ctx = ctx; this.timeout = timeout; this.limits = limits
  }
  #rpc() { return this.ctx.client.rpc(BeaconStatusRpc) }
  async snapshot(limits = this.limits) {
    return normalizeSnapshot(await this.#rpc().snapshot(limits ?? {}, {
      location: tuiLocation(this.ctx), signal: AbortSignal.timeout(this.timeout),
    }))
  }
  async control(input) {
    return this.#rpc().control(input, { signal: AbortSignal.timeout(this.timeout) })
  }
  async dispatch(input) {
    return this.#rpc().dispatch(input, { signal: AbortSignal.timeout(this.timeout) })
  }
}
export function createStatusSource(ctx, { root, deps = {}, limits = {}, sessionID, model } = {}) {
  // cwd is a transient fallback, never a verified root; re-resolve per call so
  // a late location replaces it without a reload and can filter the queue.
  const explicit = root ?? deps.root ?? null
  const exists = deps.existsSync ?? fsExistsSync
  let current = null, cli = null, verified = false, filterRoot = null
  const rootOf = () => {
    const located = explicit ?? projectRoot(ctx)
    const next = located ?? process.cwd()
    if (next !== current || !cli) { current = next; cli = new CliStatusSource({ root: next, deps, limits, sessionID, model }) }
    // Only a canonical root that can see docs/tasks may judge the queue. A
    // worktree directory loads the plugin but holds none of the main project's
    // cards, so filtering against it would drop every ready card. Without a
    // location, an explicit root is the only root available and is trusted.
    const info = locationInfo(ctx)
    if (info) {
      const canonical = info.canonical
      filterRoot = canonical && exists(join(canonical, "docs", "tasks")) ? canonical : null
    } else filterRoot = explicit
    verified = Boolean(filterRoot)
    return next
  }
  const rpc = typeof ctx?.client?.rpc === "function" ? new RpcStatusSource(ctx, { limits }) : null
  const snapshotView = (value, project) => {
    const view = normalizeSnapshot(value)
    if (!project) return view
    return { ...view, queue: view.queue.filter((item) => cardIsPending(project, item.id, deps)) }
  }
  const fallback = async (name, input) => {
    rootOf()
    const project = filterRoot, isVerified = verified
    // A verified root lets the CLI read this project's store on this call. An
    // RPC snapshot can be stale or partially initialised and return an empty
    // queue without throwing, so it must not hide ready cards. An unverified
    // cwd guess stays behind RPC. RPC remains the accelerator for control and
    // dispatch.
    if (name === "snapshot" && isVerified) {
      try { return snapshotView(await cli.snapshot(input), project) } catch {}
    }
    let value, usedRPC = false
    if (rpc) { try { value = await rpc[name](input); usedRPC = true } catch {} }
    if (!usedRPC) value = await cli[name](input)
    if (name !== "snapshot") return value
    return snapshotView(value, project)
  }
  return {
    snapshot: (input = limits) => fallback("snapshot", input),
    control: (input) => fallback("control", input),
    dispatch: (input) => fallback("dispatch", input), root: rootOf, verified: () => verified,
  }
}
export const errorMessage = (error) => (error && typeof error.message === "string" ? error.message : String(error))
export { cleanErrorMessage } from "./ui_error.js"
