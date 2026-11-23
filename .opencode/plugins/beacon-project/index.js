import { Plugin } from "@opencode/plugin"
import { locationInfo, projectRoot } from "./location.js"
import { registerBeaconMcp } from "./mcp.js"
import { installProjectContext } from "./context.js"

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
])

const GUARDED_TOOLS = new Set(["bash", "shell", "execute"])

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

export function findShellGuardHit(tool, input) {
  if (!GUARDED_TOOLS.has(tool)) return null
  const command = commandFromToolInput(input)
  if (!command) return null
  return SHELL_GUARD_RULES.find((rule) => rule.pattern.test(command)) ?? null
}

export function guardMode(config = {}, env = process.env) {
  const fromEnv = env.BEACON_SHELL_GUARD
  if (typeof fromEnv === "string") return fromEnv
  return config.shellGuard
}

export async function registerShellGuard(ctx, config = {}) {
  if (typeof ctx?.tool?.hook !== "function") return null
  const mode = guardMode(config)
  try {
    return await ctx.tool.hook("execute.before", (event) => {
      const hit = findShellGuardHit(event?.tool, event?.input)
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

    const mcp = await registerBeaconMcp(ctx, root)
    const context = await installProjectContext(ctx, root, locationInfo(ctx))
    const guard = await registerShellGuard(ctx, ctx?.config ?? {})

    return async () => {
      await guard?.dispose?.()
      await context?.()
      await mcp?.registration?.dispose?.()
    }
  },
})

export default beaconProject
