// Beacon status panel for the OpenCode TUI sidebar.
//
// The panel depends only on the BeaconStatusSource contract below. Three sources
// exist here: CliStatusSource spawns the beacon binary, RpcStatusSource reaches
// the server plugin, and createStatusSource combines them so a missing RPC
// never blanks the panel. External modules are imported lazily inside setup so
// this file stays loadable by plain `node --test` without OpenCode's peer
// dependencies installed.

import { execFile as nodeExecFile } from "node:child_process"
import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { join } from "node:path"
import { promisify } from "node:util"
import { beaconCommand } from "./mcp.js"
import { projectRoot } from "./location.js"

const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"

const execFileAsync = promisify(nodeExecFile)

export const REFRESH_MS = 5000
export const RPC_TIMEOUT_MS = 10000
export const BEACON_STATUS_RPC_ID = "beacon.project.status"

// Fixed widths keep every rendered line inside the sidebar.
export const PANEL_WIDTH = 44
export const TITLE_WIDTH = 30
export const REASON_WIDTH = 30

/**
 * @typedef {Object} Run
 * @property {string} id
 * @property {string} taskID
 * @property {string} state
 * @property {string} title     Human readable task title, or the raw task ID.
 * @property {number} pr
 * @property {string} sha
 * @property {string} reason
 * @property {number} rebaseAttempts
 * @property {number} fixAttempts
 * @property {number} maxAttempts
 * @property {unknown} updatedAt
 */

/**
 * @typedef {Object} StatusSnapshot
 * @property {Run[]} active
 * @property {Run[]} recent
 */

/**
 * The only contract the UI knows about. Any source can implement it.
 * @typedef {Object} BeaconStatusSource
 * @property {() => Promise<StatusSnapshot>} snapshot
 */

export const EMPTY_SNAPSHOT = Object.freeze({ active: Object.freeze([]), recent: Object.freeze([]) })

const defaultDeps = Object.freeze({
  execFile: execFileAsync,
  existsSync: fsExistsSync,
  readFileSync: fsReadFileSync,
})

function str(value) {
  if (typeof value === "string") return value
  return value == null ? "" : String(value)
}

function numberOrZero(value) {
  const n = Number(value)
  return Number.isFinite(n) ? n : 0
}

function pick(source, names) {
  for (const name of names) {
    const value = source?.[name]
    if (value !== undefined && value !== null) return value
  }
  return undefined
}

export function normalizeRun(value) {
  const source = value && typeof value === "object" ? value : {}
  const taskID = str(pick(source, ["TaskID", "task_id", "taskID", "taskId"]))
  const title = str(pick(source, ["title", "Title"]))
  return {
    id: str(pick(source, ["ID", "id"])),
    taskID,
    state: str(pick(source, ["State", "state"])) || "unknown",
    title: title || taskID,
    pr: numberOrZero(pick(source, ["PRNumber", "pr", "pr_number", "prNumber"])),
    sha: str(pick(source, ["HeadSHA", "head_sha", "headSHA", "sha"])),
    reason: str(pick(source, ["Reason", "reason"])),
    rebaseAttempts: numberOrZero(pick(source, ["RebaseAttempts", "rebase_attempts", "rebaseAttempts"])),
    fixAttempts: numberOrZero(pick(source, ["FixAttempts", "fix_attempts", "fixAttempts"])),
    maxAttempts: numberOrZero(pick(source, ["MaxAttempts", "max_attempts", "maxAttempts"])),
    updatedAt: pick(source, ["UpdatedAt", "updated_at", "updatedAt"]),
  }
}

export function normalizeSnapshot(value) {
  const source = value && typeof value === "object" ? value : {}
  const list = (v) => (Array.isArray(v) ? v : []).map(normalizeRun)
  return { active: list(source.active), recent: list(source.recent) }
}

// A nil Go slice is printed as JSON `null`, which is a normal empty result.
export function parseRunList(text) {
  if (text == null || String(text).trim() === "") return []
  let value
  try {
    value = JSON.parse(String(text))
  } catch {
    return []
  }
  return Array.isArray(value) ? value : []
}

function stripQuotes(value) {
  const text = value.trim()
  if (text.length >= 2 && ((text.startsWith('"') && text.endsWith('"')) || (text.startsWith("'") && text.endsWith("'")))) {
    return text.slice(1, -1).trim()
  }
  return text
}

// Read the human readable title from the card frontmatter. A missing file or
// missing title must never fail the snapshot, so both fall back to the task ID.
export function readTitle(root, taskID, deps = defaultDeps) {
  const id = str(taskID)
  if (!id) return ""
  const path = join(root, "docs", "tasks", `${id}.md`)
  let text
  try {
    if (!deps.existsSync(path)) return id
    text = deps.readFileSync(path, "utf8")
  } catch {
    return id
  }
  const frontmatter = /^---\s*\r?\n([\s\S]*?)\r?\n---/.exec(String(text))
  if (!frontmatter) return id
  const match = /^title:\s*(.+?)\s*$/m.exec(frontmatter[1])
  if (!match) return id
  return stripQuotes(match[1]) || id
}

export function enrichRuns(root, runs, deps = defaultDeps) {
  return (Array.isArray(runs) ? runs : []).map((run) => {
    const normalized = normalizeRun(run)
    if (!str(pick(run, ["title", "Title"]))) {
      normalized.title = readTitle(root, normalized.taskID, deps)
    }
    return normalized
  })
}

// beaconCommand resolves the binary plus its `mcp` subcommand. Drop that
// subcommand; the source adds `status` (and optionally `--recent`) per call.
function baseCommand(root) {
  const parts = beaconCommand(root)
  if (parts[parts.length - 1] === "mcp") parts.pop()
  return parts
}

/** BeaconStatusSource backed by the beacon CLI. Used on the server side. */
export class CliStatusSource {
  constructor({ root, deps = {} }) {
    this.root = root
    this.deps = { ...defaultDeps, ...deps }
  }

  async #list(base, recent) {
    const [program, ...rest] = base
    const args = recent ? [...rest, "status", "--recent"] : [...rest, "status"]
    const { stdout } = await this.deps.execFile(program, args, {
      cwd: this.root,
      encoding: "utf8",
      env: { ...process.env, [BEACON_PROJECT_ROOT_ENV]: this.root },
      timeout: RPC_TIMEOUT_MS,
    })
    return parseRunList(stdout)
  }

  async snapshot() {
    // Resolve once so a stale local binary is rebuilt at most once per poll.
    const base = this.deps.command ? this.deps.command() : baseCommand(this.root)
    const [activeRaw, recentRaw] = await Promise.all([
      this.#list(base, false),
      this.#list(base, true),
    ])
    return {
      active: enrichRuns(this.root, activeRaw, this.deps),
      recent: enrichRuns(this.root, recentRaw, this.deps),
    }
  }
}

export function createCliStatusSource({ root, deps = {} }) {
  return new CliStatusSource({ root, deps })
}

function standardSchema(check) {
  return {
    "~standard": {
      version: 1,
      vendor: "beacon",
      validate: (value) => {
        const issue = check(value)
        return issue ? { issues: [{ message: issue }] } : { value }
      },
    },
  }
}

// Shared by the server plugin that registers the handler and the TUI that calls it.
export const BeaconStatusRpc = Object.freeze({
  id: BEACON_STATUS_RPC_ID,
  events: Object.freeze({}),
  methods: Object.freeze({
    snapshot: Object.freeze({
      input: standardSchema(() => null),
      output: standardSchema((value) =>
        value && typeof value === "object" && !Array.isArray(value) ? null : "output must be an object"),
    }),
  }),
})

/** BeaconStatusSource that reaches the CLI through the server plugin RPC. */
export class RpcStatusSource {
  constructor(context, { timeout = RPC_TIMEOUT_MS } = {}) {
    this.context = context
    this.timeout = timeout
  }

  async snapshot() {
    // Build the client per call; the server plugin may register after TUI startup.
    const rpc = this.context.client.rpc(BeaconStatusRpc)
    const location = this.context.location ?? this.context.data?.location?.default?.()
    const output = await rpc.snapshot({}, {
      location,
      signal: AbortSignal.timeout(this.timeout),
    })
    return normalizeSnapshot(output)
  }
}

// The server plugin may run in another process or may never register the RPC.
// Trying the RPC first keeps the cheap path, while the local CLI keeps the
// first render live when the RPC call is missing or fails.
export function createStatusSource(ctx, { root, deps = {} } = {}) {
  const resolvedRoot = root ?? projectRoot(ctx) ?? process.cwd()
  const cli = createCliStatusSource({ root: resolvedRoot, deps })
  const rpc = typeof ctx?.client?.rpc === "function" ? new RpcStatusSource(ctx) : null
  return {
    async snapshot() {
      if (rpc) {
        try {
          return await rpc.snapshot()
        } catch {
          // Fall through to the CLI. The panel shows runs, not an RPC error.
        }
      }
      return cli.snapshot()
    },
  }
}

export function errorMessage(error) {
  if (error instanceof Error) return error.message
  if (error && typeof error === "object" && typeof error.message === "string") return error.message
  return String(error)
}

// Each state gets one marker and one color. The view applies the color to the
// whole line, so the marker reads as a colored status indicator.
const STATE_STYLE = Object.freeze({
  pending: Object.freeze({ marker: "○", fg: "#9ca3af" }),
  running: Object.freeze({ marker: "●", fg: "#4ade80" }),
  waiting_ci: Object.freeze({ marker: "◐", fg: "#60a5fa" }),
  needs_rebase: Object.freeze({ marker: "◆", fg: "#fbbf24" }),
  needs_agent_fix: Object.freeze({ marker: "◆", fg: "#fbbf24" }),
  blocked: Object.freeze({ marker: "▲", fg: "#f87171" }),
  merged: Object.freeze({ marker: "✔", fg: "#4ade80" }),
  closed: Object.freeze({ marker: "✖", fg: "#9ca3af" }),
})
const DEFAULT_STYLE = Object.freeze({ marker: "·", fg: "#9ca3af" })
const ERROR_FG = "#f87171"

function styleFor(state) {
  return STATE_STYLE[str(state).toLowerCase()] ?? DEFAULT_STYLE
}

export function truncate(value, width) {
  const text = str(value)
  if (text.length <= width) return text
  if (width <= 1) return text.slice(0, width)
  return `${text.slice(0, width - 1)}…`
}

export function shortTaskID(value) {
  const id = str(value)
  if (!id) return "-"
  return id.length > 10 ? id.slice(-8) : id
}

function runLineItems(run) {
  const style = styleFor(run.state)
  const title = run.title && run.title !== run.taskID ? truncate(run.title, TITLE_WIDTH) : ""
  const head = [`${style.marker} ${shortTaskID(run.taskID)}`, title].filter(Boolean).join(" ")
  const pr = run.pr ? `PR #${run.pr}` : ""
  const reason = run.reason ? truncate(run.reason, REASON_WIDTH) : ""
  const detail = [pr, reason].filter(Boolean).join("  ")
  const items = [{ text: head, fg: style.fg }]
  if (detail) items.push({ text: `  ${detail}` })
  return items
}

// Pure view model. Each item carries the text and an optional foreground color.
// The TUI renders the items; tests assert on the exact text of every line.
export function snapshotLineItems(snapshot = EMPTY_SNAPSHOT, error = null) {
  if (error) return [{ text: `Beacon status unavailable: ${error}`, fg: ERROR_FG }]
  const view = normalizeSnapshot(snapshot)
  const rule = "─".repeat(PANEL_WIDTH)
  const items = [{ text: "Beacon" }, { text: rule }]
  if (view.active.length === 0) items.push({ text: "No active runs" })
  else view.active.forEach((run) => items.push(...runLineItems(run)))
  items.push({ text: rule }, { text: "Recent" })
  if (view.recent.length === 0) items.push({ text: "No recent runs" })
  else view.recent.forEach((run) => items.push(...runLineItems(run)))
  return items
}

export function renderSnapshotLines(snapshot = EMPTY_SNAPSHOT, error = null) {
  return snapshotLineItems(snapshot, error).map((item) => item.text)
}

async function setup(ctx) {
  const [{ createComponent, createElement, insert, setProp }, { createSignal, onCleanup }] = await Promise.all([
    import("@opentui/solid"),
    import("solid-js"),
  ])
  const source = createStatusSource(ctx)

  function BeaconView() {
    const [items, setItems] = createSignal(snapshotLineItems(EMPTY_SNAPSHOT))
    let stopped = false

    const refresh = async () => {
      if (stopped) return
      try {
        const next = await source.snapshot()
        if (!stopped) setItems(snapshotLineItems(next ?? EMPTY_SNAPSHOT))
      } catch (cause) {
        if (!stopped) setItems(snapshotLineItems(EMPTY_SNAPSHOT, errorMessage(cause)))
      }
    }
    void refresh()
    const timer = setInterval(() => void refresh(), REFRESH_MS)
    onCleanup(() => {
      stopped = true
      clearInterval(timer)
    })

    const box = createElement("box")
    setProp(box, "flexDirection", "column")
    insert(box, () => items().map((item) => {
      const text = createElement("text")
      setProp(text, "wrapMode", "none")
      setProp(text, "selectable", false)
      if (item.fg) setProp(text, "fg", item.fg)
      insert(text, item.text)
      return text
    }))
    return box
  }

  const render = () => createComponent(BeaconView, {})
  const disposeSidebar = ctx.ui.slot({ append: "sidebar.content", render })
  const disposeFooter = ctx.ui.slot({ append: "home.footer.status", render })
  return () => {
    disposeSidebar?.()
    disposeFooter?.()
  }
}

export const beaconStatusDefinition = { id: "beacon-project-status", setup }

export function defineBeaconStatusPlugin(Plugin) {
  return Plugin.define(beaconStatusDefinition)
}

// @opencode/plugin/tui defines Plugin.define. The dynamic import with an
// identity fallback keeps this module loadable by node --test, where the TUI
// peer dependencies are not installed.
let Plugin
try {
  ;({ Plugin } = await import("@opencode/plugin/tui"))
} catch {
  Plugin = { define: (definition) => definition }
}

export default defineBeaconStatusPlugin(Plugin)
