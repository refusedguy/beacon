import { Plugin } from "@opencode/plugin"
import { projectRoot } from "./state/location.js"
import { createMcpRegistrar, noopMcpRegistration, registerBeaconMcp } from "./mcp/mcp.js"
import { installProjectContext } from "./rules/context.js"
import { agentIdentity, sessionIdentity } from "./state/session_identity.js"
import {
  pluginOptions, pollIntervalMs, snapshotLimits,
  statusPanelEnabled, watcherEnabled,
} from "./state/options.js"
import {
  BeaconStatusRpc, RUN_CHANGED_EVENT,
  createCliStatusSource, normalizeSnapshot, createRunChangeWatcher,
} from "./ui/status_rpc.js"
import { isWorkerAgent, registerShellGuard } from "./shell_guard.js"

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
    } catch (error) {
      console.debug(`beacon plugin: session lookup failed: ${error?.message ?? error}`)
    }
  }

  if (typeof ctx.session?.list === "function") {
    try {
      const listed = await ctx.session.list({ order: "desc", limit: 1 })
      const sessions = Array.isArray(listed) ? listed : listed?.data ?? []
      const data = sessions[0]
      if (data) return agentIdentity(ctx, data) ?? data?.agent?.id ?? data?.agentID ?? null
    } catch (error) {
      console.debug(`beacon plugin: session list failed: ${error?.message ?? error}`)
    }
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

export const beaconProjectDefinition = {
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
      const cleanup = async (label, dispose) => {
        try { await dispose?.() } catch (error) {
          console.debug(`beacon plugin: ${label} cleanup failed: ${error?.message ?? error}`)
        }
      }
      await cleanup("run watcher", () => watcher?.stop())
      await cleanup("shell guard", () => guard?.dispose?.())
      await cleanup("project context", context)
      await cleanup("MCP registration", () => mcp?.dispose?.())
      await cleanup("status RPC", () => statusRpc?.dispose?.())
    }
  },
}

// The caller supplies define so tests can build the plugin with a double. The
// host supplies Plugin here, so its own OpenTUI and Solid instances are used.
export function defineBeaconProject(define) { return define(beaconProjectDefinition) }
export const beaconProject = Plugin.define(beaconProjectDefinition)

export { agentIdentity } from "./state/session_identity.js"
export {
  SHELL_GUARD_RULES, buildShellGuardRules, commandFromToolInput, commandSegments,
  commandSubstitutions, compileShellGuardRule, findShellGuardHit, guardAppliesToAgent,
  guardedAgents, guardMode, isWorkerAgent, registerShellGuard, stripCommandPrefix,
} from "./shell_guard.js"
export { createRunChangeWatcher } from "./ui/status_rpc.js"
export { pluginOptions, pollIntervalMs, snapshotLimits, statusPanelEnabled, watcherEnabled } from "./state/options.js"
export default beaconProject
