// OpenCode provides @opencode/plugin at runtime. Load it defensively so the
// guard logic in this module stays importable in plain Node for tests.
let Plugin
try {
  ;({ Plugin } = await import("@opencode/plugin"))
} catch {
  Plugin = { define: (definition) => definition }
}

import { projectRoot } from "./state/location.js"
import { createMcpRegistrar, noopMcpRegistration, registerBeaconMcp } from "./mcp/mcp.js"
import { installProjectContext } from "./rules/context.js"
import { agentIdentity, sessionIdentity } from "./state/session_identity.js"
import {
  pluginOptions, pollIntervalMs, snapshotLimits,
  statusPanelEnabled, watcherEnabled,
} from "./state/options.js"
import {
  BeaconStatusRpc, REFRESH_MS, RUN_CHANGED_EVENT, changedRuns,
  createCliStatusSource, normalizeSnapshot,
} from "./ui/ui.js"

// Advisory guard at the tool boundary. It catches the command shapes the
// project documents for workers, and it can be relaxed with
// BEACON_SHELL_GUARD=warn. This is not a security boundary: quoting, aliases
// and scripts can hide a command. Keep each rule narrow so ordinary work, such
// as pushing a branch named feature/main-fix, is never blocked.
export const SHELL_GUARD_RULES = Object.freeze([
  {
    id: "recursive grep",
    pattern: /\bgrep\b[^\n]*\s-(?:-recursive\b|[A-Za-z]*[rR][A-Za-z]*\b)/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "root find",
    pattern: /\bfind\b\s+\/+(?=\s|$)/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "direct push to main",
    pattern: /\bgit\s+push\b[^\n]*(?:^|[\s"':])(?:main|master)(?=$|[\s"':])/,
    replacement: "push to your own branch and open a pull request",
  },
  {
    id: "full go build",
    pattern: /\bgo\s+build\b[^\n]*\.\/\.\.\./,
    replacement: "a targeted go build -o <path> <package>, or leave the full build to CI",
  },
  {
    id: "full go test suite",
    pattern: /\bgo\s+test\b[^\n]*\.\/\.\.\./,
    replacement: "go test for the changed packages, or leave the full suite to CI",
  },
  {
    id: "full go vet",
    pattern: /\bgo\s+vet\b[^\n]*\.\/\.\.\./,
    replacement: "go vet on the changed packages, or leave the full vet to CI",
  },
  {
    id: "full lint run",
    pattern: /\bgolangci-lint\s+run\b/,
    replacement: "the linter in CI, or lint only the changed packages",
  },
  {
    id: "npm ci",
    pattern: /\bnpm\s+ci\b/,
    replacement: "npm ci in CI, or install only the packages you need",
  },
  {
    id: "npm run build",
    pattern: /\bnpm\s+run\s+build\b/,
    replacement: "npm run build in CI, or build a single package",
  },
])

const GUARDED_TOOLS = new Set(["bash", "shell", "execute"])
const DEFAULT_GUARDED_AGENTS = Object.freeze(["worker"])

export function commandFromToolInput(input) {
  if (typeof input === "string") return input
  if (!input || typeof input !== "object") return ""
  for (const key of ["command", "cmd", "code", "script"]) {
    if (typeof input[key] === "string") return input[key]
  }
  const args = input.args ?? input.arguments
  if (typeof args === "string") return args
  if (Array.isArray(args)) return args.filter((part) => typeof part === "string").join(" ")
  return ""
}

const SHELL_SEPARATORS = /[;&|\n]+/
const ASSIGNMENT_PREFIX = /^(?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s]*)?\s+)/
const ENV_PREFIX = /^env(?:\s+[A-Za-z_][A-Za-z0-9_]*=[^\s]+)*\s+/
const SUDO_PREFIX = /^sudo(?:\s+-(?:u|g)(?:[=\s]\S+)?|\s+-[A-Za-z0-9]+)*\s+/
const BACKTICK_SUBSTITUTION = /`([^`]*)`/g

// A command often hides the interesting call behind assignments, `env` or
// `sudo`. Remove those wrappers repeatedly so nested forms such as
// `PATH=/bin sudo -u admin find /` reduce to the command being run.
export function stripCommandPrefix(part) {
  let text = typeof part === "string" ? part.trim() : ""
  for (;;) {
    const before = text
    const assignment = text.match(ASSIGNMENT_PREFIX)
    if (assignment) text = text.slice(assignment[0].length)
    const env = text.match(ENV_PREFIX)
    if (env) text = text.slice(env[0].length)
    const sudo = text.match(SUDO_PREFIX)
    if (sudo) text = text.slice(sudo[0].length)
    if (text === before) break
  }
  return text
}

// Both substitution forms are walked with balanced parentheses for $(...) so
// a nested $(...) does not cut the outer command short.
export function commandSubstitutions(command) {
  const found = []
  if (typeof command !== "string") return found
  for (let i = 0; i < command.length; i++) {
    if (command[i] !== "$" || command[i + 1] !== "(") continue
    let depth = 1
    let end = i + 2
    for (; end < command.length && depth > 0; end++) {
      if (command[end] === "(") depth++
      else if (command[end] === ")") depth--
    }
    if (depth === 0) found.push(command.slice(i + 2, end - 1))
  }
  BACKTICK_SUBSTITUTION.lastIndex = 0
  let match
  while ((match = BACKTICK_SUBSTITUTION.exec(command)) !== null) found.push(match[1])
  return found
}

// Reduce one tool input to the individual commands a shell would run. Split on
// operators, strip prefixes, then recurse into substitutions. The depth cap
// keeps a pathological input from looping forever.
export function commandSegments(command, depth = 0) {
  if (typeof command !== "string" || !command || depth > 8) return []
  const segments = []
  for (const raw of command.split(SHELL_SEPARATORS)) {
    const stripped = stripCommandPrefix(raw)
    if (stripped) segments.push(stripped)
  }
  for (const inner of commandSubstitutions(command)) {
    segments.push(...commandSegments(inner, depth + 1))
  }
  return segments
}

export function compileShellGuardRule(entry, log = console.warn) {
  if (!entry || typeof entry !== "object") return null
  const { id, replacement } = entry
  if (typeof id !== "string" || !id || typeof replacement !== "string") return null
  let pattern
  try {
    if (entry.pattern instanceof RegExp) {
      const flags = entry.pattern.flags.replace(/[gy]/g, "")
      pattern = new RegExp(entry.pattern.source, flags)
    } else if (typeof entry.pattern === "string") {
      pattern = new RegExp(entry.pattern)
    } else {
      return null
    }
  } catch (error) {
    log(`beacon shell guard: ignoring rule "${id}" with invalid pattern: ${error.message}`)
    return null
  }
  return Object.freeze({ id, pattern, replacement })
}

export function buildShellGuardRules(config = {}, log = console.warn) {
  const configured = config.shellGuardRules
  if (!Array.isArray(configured)) return SHELL_GUARD_RULES
  const additions = configured
    .map((entry) => compileShellGuardRule(entry, log))
    .filter(Boolean)
  if (!additions.length) return SHELL_GUARD_RULES
  return Object.freeze([...SHELL_GUARD_RULES, ...additions])
}

export function findShellGuardHit(tool, input, rules = SHELL_GUARD_RULES) {
  if (!GUARDED_TOOLS.has(tool)) return null
  const command = commandFromToolInput(input)
  if (!command) return null
  for (const segment of commandSegments(command)) {
    for (const rule of rules) {
      rule.pattern.lastIndex = 0
      if (rule.pattern.test(segment)) return rule
    }
  }
  return null
}

export function guardedAgents(config = {}) {
  const value = config.shellGuardAgents ?? config.shellGuardAgent
  if (Array.isArray(value)) {
    const agents = value.filter((agent) => typeof agent === "string" && agent)
    return agents.length ? Object.freeze(agents) : DEFAULT_GUARDED_AGENTS
  }
  if (typeof value === "string" && value) return Object.freeze([value])
  return DEFAULT_GUARDED_AGENTS
}

export function guardAppliesToAgent(event, config = {}) {
  const agent = agentIdentity(null, event)
  // When the event carries no agent, keep the guard as a safety net.
  if (!agent) return true
  return guardedAgents(config).includes(agent)
}

export function guardMode(config = {}, env = process.env) {
  const fromEnv = env.BEACON_SHELL_GUARD
  if (typeof fromEnv === "string") return fromEnv
  return config.shellGuard
}

// Workers inherit the pooled MCP daemon from the parent session. Only an agent
// named exactly in the configured guard list is a worker. A name that merely
// contains "worker", such as "networker", must not inherit that behavior. A
// project variant has to be configured explicitly.
export function isWorkerAgent(agent, config = {}) {
  if (typeof agent !== "string" || !agent.trim()) return false
  const name = agent.trim().toLowerCase()
  return guardedAgents(config).some((configured) => configured.trim().toLowerCase() === name)
}

// OpenCode does not hand a session ID to plugin setup. Read the agent from the
// setup context through the shared adapter, then from the session domain. An
// unknown agent falls back to registering, which is the safe path.
export async function currentSessionAgent(ctx) {
  if (!ctx || typeof ctx !== "object") return null
  const direct = agentIdentity(ctx)
  if (direct) return direct

  const sessionID = ctx.sessionID ?? ctx.session?.id ?? ctx.session?.sessionID
  if (typeof sessionID === "string" && sessionID && typeof ctx.session?.get === "function") {
    try {
      const info = await ctx.session.get({ sessionID })
      const data = info?.data ?? info
      return agentIdentity(ctx, data) ?? data?.agent?.id ?? data?.agentID ?? null
    } catch {}
  }

  if (typeof ctx.session?.list === "function") {
    try {
      const listed = await ctx.session.list({ order: "desc", limit: 1 })
      const sessions = Array.isArray(listed) ? listed : listed?.data ?? []
      const data = sessions[0]
      if (data) return agentIdentity(ctx, data) ?? data?.agent?.id ?? data?.agentID ?? null
    } catch {}
  }
  return null
}

// Resolve once. Workers get a no-op so no command resolution runs and no config
// is registered. Every other agent keeps the registration behavior as it was.
export async function resolveMcpRegistration(ctx, root, { agent, registrar, config } = {}) {
  const resolved = agent ?? (await currentSessionAgent(ctx))
  const options = pluginOptions(ctx)
  if (isWorkerAgent(resolved, config ?? options)) {
    return typeof options.noopMcpRegistration === "function"
      ? options.noopMcpRegistration()
      : noopMcpRegistration()
  }
  if (registrar) return registrar.registerBeaconMcp(ctx, root)
  return registerBeaconMcp(ctx, root, options.mcpDeps ?? {})
}

export async function registerShellGuard(ctx, config = {}, env = process.env) {
  if (typeof ctx?.tool?.hook !== "function") return null
  const mode = guardMode(config, env)
  const rules = buildShellGuardRules(config)
  try {
    return await ctx.tool.hook("execute.before", (event) => {
      if (!guardAppliesToAgent(event, config)) return
      const hit = findShellGuardHit(event?.tool, event?.input, rules)
      if (!hit) return
      const message = `${hit.id} is blocked here. Use ${hit.replacement}.`
      console.warn(message)
      if (mode === "warn") return
      throw new Error(message)
    })
  } catch {
    return null
  }
}

// The Beacon daemon runs in the background, so this plugin cannot read the
// reconciler's event stream. Watch successful snapshot diffs instead and emit
// one notification per changed run. The key dedupes repeated emissions.
export function createRunChangeWatcher(source, { emit, deps = {} } = {}) {
  const setTimer = deps.setInterval ?? globalThis.setInterval.bind(globalThis)
  const clearTimer = deps.clearInterval ?? globalThis.clearInterval.bind(globalThis)
  const intervalMs = deps.intervalMs ?? REFRESH_MS
  let previous = null
  let timer = null
  let stopped = false
  let inFlight = false
  const tick = async () => {
    if (stopped || inFlight) return
    inFlight = true
    try {
      const next = normalizeSnapshot(await source.snapshot())
      if (stopped) return
      const changes = previous ? changedRuns(previous, next) : []
      previous = next
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

export const beaconProject = Plugin.define({
  id: "beacon-project",
  async setup(ctx) {
    const root = projectRoot(ctx)
    if (!root) return

    // Config values may arrive under options or the full config object.
    const options = pluginOptions(ctx)
    const agent = agentIdentity(ctx) ?? (await currentSessionAgent(ctx))
    const isWorker = isWorkerAgent(agent, options)

    // Record the server config for the operator's manual attach. No daemon
    // starts per session. A worker skips this because it already inherits the
    // parent's pooled connection and re-registering reconnects the operator's
    // TUI. The no-op keeps the cleanup path identical.
    const registrar = createMcpRegistrar(options.mcpDeps ?? {})
    const mcp = await resolveMcpRegistration(ctx, root, { agent, registrar })
    const context = await installProjectContext(ctx, root, options)
    const guard = await registerShellGuard(ctx, options)

    // The status panel and its watcher are for the operator's TUI. Building
    // their source in a worker would resolve the MCP command through
    // CliStatusSource and start a new poll loop, so workers get neither.
    const panelEnabled = !isWorker && statusPanelEnabled(options)
    let statusRpc = null
    let watcher = null
    if (panelEnabled && typeof ctx.rpc?.register === "function") {
      // One CLI snapshot feeds every panel. The handler normalizes the same
      // shape the TUI source uses, so old fields stay available to both.
      // The CLI has no MCP _meta, so carry the operator session and model in
      // the child environment. Missing values leave read-only calls intact.
      const { sessionID, model } = await sessionIdentity(ctx)
      const limits = snapshotLimits(options)
      const statusSource = createCliStatusSource({ root, limits, sessionID, model })
      try {
        statusRpc = await ctx.rpc.register(BeaconStatusRpc, {
          snapshot: async (input) => normalizeSnapshot(await statusSource.snapshot(input ?? limits)),
          control: async (input) => statusSource.control(input ?? {}),
          dispatch: async (input) => statusSource.dispatch(input ?? {}),
        })
      } catch (error) {
        console.warn(`beacon status panel: RPC registration failed: ${error.message}`)
      }

      const pollMs = pollIntervalMs(options.statusPollMs)
      watcher = statusRpc && watcherEnabled(options) && pollMs > 0
        ? createRunChangeWatcher(statusSource, {
          emit: (change) => statusRpc.events?.emit?.(RUN_CHANGED_EVENT, change),
          deps: { intervalMs: pollMs },
        }) : null
      watcher?.start()
    }

    return async () => {
      watcher?.stop()
      await guard?.dispose?.()
      await context?.()
      await mcp?.dispose?.()
      await statusRpc?.dispose?.()
    }
  },
})

export { agentIdentity } from "./state/session_identity.js"
export { pluginOptions, pollIntervalMs, snapshotLimits, statusPanelEnabled, watcherEnabled } from "./state/options.js"
export default beaconProject
