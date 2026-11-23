// OpenCode provides @opencode/plugin at runtime. Load it defensively so the
// guard logic in this module stays importable in plain Node for tests.
let Plugin
try {
  ;({ Plugin } = await import("@opencode/plugin"))
} catch {
  Plugin = { define: (definition) => definition }
}

import { locationInfo, projectRoot } from "./location.js"
import { registerBeaconMcp } from "./mcp.js"
import { installProjectContext } from "./context.js"
import { BeaconStatusRpc, createCliStatusSource, normalizeSnapshot } from "./ui.js"

export const SHELL_GUARD_RULES = Object.freeze([
  {
    id: "recursive grep",
    pattern: /(?:^|[;&|]\s*)grep\s+-(?:[A-Za-z]*[rR][A-Za-z]*)\b/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "root find",
    pattern: /(?:^|[;&|]\s*)find\s+\/(?=\s|$|[;&|])/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "direct push to main",
    pattern: /\bgit\s+push\b[^\n|;&]*\b(?:main|master)\b/,
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
  for (const rule of rules) {
    rule.pattern.lastIndex = 0
    if (rule.pattern.test(command)) return rule
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

export function eventAgent(event) {
  const candidates = [event?.agent, event?.session?.agent, event?.agentName]
  for (const value of candidates) {
    if (typeof value === "string" && value) return value
  }
  return null
}

export function guardAppliesToAgent(event, config = {}) {
  const agent = eventAgent(event)
  // When the event carries no agent, keep the guard as a safety net.
  if (!agent) return true
  return guardedAgents(config).includes(agent)
}

export function guardMode(config = {}, env = process.env) {
  const fromEnv = env.BEACON_SHELL_GUARD
  if (typeof fromEnv === "string") return fromEnv
  return config.shellGuard
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

export const beaconProject = Plugin.define({
  id: "beacon-project",
  async setup(ctx) {
    const root = projectRoot(ctx)
    if (!root) return

    // Config values may arrive under options or the full config object.
    const guardConfig = {
      ...(ctx?.options && typeof ctx.options === "object" ? ctx.options : {}),
      ...(ctx?.config && typeof ctx.config === "object" ? ctx.config : {}),
    }

    // Record the server config for the operator's manual attach. This does not
    // register an MCP server with OpenCode, so no daemon starts per session.
    const mcp = await registerBeaconMcp(ctx, root)
    const context = await installProjectContext(ctx, root, locationInfo(ctx))
    const guard = await registerShellGuard(ctx, guardConfig)

    // One CLI snapshot feeds every panel. The handler normalizes the same
    // shape the TUI source uses, so old fields stay available to both.
    const statusSource = createCliStatusSource({ root })
    let statusRpc = null
    try {
      statusRpc = await ctx.rpc.register(BeaconStatusRpc, {
        snapshot: async () => normalizeSnapshot(await statusSource.snapshot()),
      })
    } catch (error) {
      console.warn(`beacon status panel: RPC registration failed: ${error.message}`)
    }

    return async () => {
      await guard?.dispose?.()
      await context?.()
      await mcp?.registration?.dispose?.()
      await statusRpc?.dispose?.()
    }
  },
})

export default beaconProject
