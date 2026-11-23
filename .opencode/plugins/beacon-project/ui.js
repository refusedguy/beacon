// Beacon status panel for the OpenCode TUI sidebar.
//
// The panel depends only on the BeaconStatusSource contract below. Two sources
// implement it here: CliStatusSource spawns the beacon binary and is used by the
// server plugin, while RpcStatusSource reaches that server code from the TUI.
// External modules are imported lazily inside setup so this file stays loadable
// by plain `node --test` without OpenCode's peer dependencies installed.

import { execFile as nodeExecFile } from "node:child_process"
import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { join } from "node:path"
import { promisify } from "node:util"
import { beaconCommand } from "./mcp.js"

const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"

const execFileAsync = promisify(nodeExecFile)

export const REFRESH_MS = 5000
export const RPC_TIMEOUT_MS = 10000
export const BEACON_STATUS_RPC_ID = "beacon.project.status"

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

export function errorMessage(error) {
  if (error instanceof Error) return error.message
  if (error && typeof error === "object" && typeof error.message === "string") return error.message
  return String(error)
}

function shortSha(sha) {
  return sha ? sha.slice(0, 7) : ""
}

export function formatAge(value, now = Date.now()) {
  if (!value) return "?"
  const time = Date.parse(value)
  if (Number.isNaN(time)) return "?"
  const seconds = Math.max(0, Math.floor((now - time) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

function formatAttempts(run) {
  const parts = []
  if (run.maxAttempts) parts.push(`max ${run.maxAttempts}`)
  if (run.rebaseAttempts) parts.push(`rebase ${run.rebaseAttempts}`)
  if (run.fixAttempts) parts.push(`fix ${run.fixAttempts}`)
  return parts.length ? parts.join(" ") : "0"
}

function formatRunLines(run) {
  const pr = run.pr ? ` PR #${run.pr}` : ""
  const sha = shortSha(run.sha)
  const lines = [`${run.state}  ${run.taskID}  ${run.title}${pr}${sha ? ` ${sha}` : ""}`]
  lines.push(`  task ${run.taskID || "-"}  ·  attempts ${formatAttempts(run)}  ·  age ${formatAge(run.updatedAt)}`)
  if (run.reason) lines.push(`  reason: ${run.reason}`)
  return lines
}

// Pure view model. The TUI renders these lines; tests assert on them directly.
export function renderSnapshotLines(snapshot = EMPTY_SNAPSHOT, error = null) {
  if (error) return [`Beacon status unavailable: ${error}`]
  const view = normalizeSnapshot(snapshot)
  const rule = "─".repeat(44)
  const lines = ["Beacon", rule]
  if (view.active.length === 0) lines.push("No active runs")
  else view.active.forEach((run) => lines.push(...formatRunLines(run)))
  lines.push(rule, "Recent")
  if (view.recent.length === 0) lines.push("No recent runs")
  else view.recent.forEach((run) => lines.push(...formatRunLines(run)))
  return lines
}

// Definition compatible with @opencode/plugin/tui. Plugin.define is an identity
// function, so the plain object is already a valid Definition.
export default {
  id: "beacon-project-status",
  async setup(ctx) {
    const [{ createComponent, createElement, insert, setProp }, { createSignal, onCleanup }] = await Promise.all([
      import("@opentui/solid"),
      import("solid-js"),
    ])
    const source = new RpcStatusSource(ctx)

    function BeaconSidebarView() {
      const [snapshot, setSnapshot] = createSignal(EMPTY_SNAPSHOT)
      const [error, setError] = createSignal(null)
      let stopped = false

      const refresh = async () => {
        if (stopped) return
        try {
          const next = await source.snapshot()
          if (!stopped) {
            setSnapshot(next ?? EMPTY_SNAPSHOT)
            setError(null)
          }
        } catch (cause) {
          if (!stopped) setError(errorMessage(cause))
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
      insert(box, () => renderSnapshotLines(snapshot(), error()).map((line) => {
        const text = createElement("text")
        setProp(text, "wrapMode", "none")
        insert(text, line)
        return text
      }))
      return box
    }

    const dispose = ctx.ui.slot({
      append: "sidebar.content",
      render: () => createComponent(BeaconSidebarView, {}),
    })
    return () => dispose?.()
  },
}
