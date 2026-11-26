import { Plugin } from "@opencode/plugin"
import { projectRoot } from "./state/location.js"
import { createMcpRegistrar, noopMcpRegistration, registerBeaconMcp } from "./mcp/mcp.js"
import { installProjectContext } from "./rules/context.js"
import { agentFromSession, agentIdentity, latestSession, sessionIdentity } from "./state/session_identity.js"
import {
  pluginOptions, pollIntervalMs, snapshotLimits,
  statusPanelEnabled, watcherEnabled,
} from "./state/options.js"
import {
  BeaconStatusRpc, RUN_CHANGED_EVENT,
  createCliStatusSource, elapsedMs, normalizeSnapshot,
  timingFromContext,
} from "./ui/status_rpc.js"
import { bodyHash, requestId } from "./protocol/body_hash.js"
export { bodyHash, requestId }
import { createRunChangeWatcher } from "./run_watcher.js"
import { isWorkerAgent, registerShellGuard } from "./shell_guard.js"

// ui_test_helpers.js re-exports ui/ui.js, which imports ui_runtime.js and its
// @opentui/solid peer. Importing it here makes the server plugin fail to load,
// so no status RPC is registered and every RPC call answers 400. Keep the
// teardown wrapper local to the server half.
export function cleanupWrapper(prefix) {
  return (label, dispose) => {
    const failed = (error) => console.debug(`${prefix}${label} cleanup failed: ${error?.message ?? error}`)
    try {
      const result = dispose?.()
      if (result && typeof result.then === "function") return result.catch(failed)
    } catch (error) {
      failed(error)
    }
  }
}

// Normalize at the handler boundary so a malformed n never reaches the CLI.
// The RPC schema still rejects it first; this keeps a direct caller safe.
export function dispatchInput(input) {
  const n = Number(input?.n)
  return { ...(input ?? {}), n: Number.isFinite(n) && n > 0 ? Math.floor(n) : 1 }
}

// Log each RPC with one correlation id and its elapsed time. The TUI carries
// the id and click start in the request headers, so the click, RPC, CLI and
// refresh lines can be read as one chain. The request body is never logged.
// The TUI swallows RPC errors before falling back to the CLI, so the server is
// the only place the 400 rejection reason is still visible.
export function createStatusRpcHandlers(source, { limits = {}, log = console.debug, now = () => Date.now() } = {}) {
  const call = async (method, input, run, context) => {
    const meta = timingFromContext(context)
    const id = meta.id ?? requestId(method, input)
    const clickStart = meta.startedAt
    const startedAt = now()
    log(`beacon status RPC ${method} id=${id} phase=start`)
    try {
      const result = await run({ id, startedAt: clickStart ?? startedAt })
      const chain = clickStart == null ? "" : ` elapsedMs=${elapsedMs(now(), clickStart)}`
      log(`beacon status RPC ${method} ok id=${id} durationMs=${elapsedMs(now(), startedAt)}${chain}`)
      return result
    } catch (error) {
      const chain = clickStart == null ? "" : ` elapsedMs=${elapsedMs(now(), clickStart)}`
      const body = error && typeof error === "object"
        ? { type: error.type, status: error.status, message: error.message, data: error.data }
        : { message: String(error) }
      log(`beacon status RPC ${method} failed id=${id} durationMs=${elapsedMs(now(), startedAt)}${chain} body=${JSON.stringify(body)}`)
      throw error
    }
  }
  return {
    snapshot: (input, context) => call("snapshot", input, async () => normalizeSnapshot(await source.snapshot(input ?? limits)), context),
    control: (input, context) => call("control", input, async () => source.control(input ?? {}), context),
    dispatch: (input, context) => call("dispatch", input, async (timing) => source.dispatch(dispatchInput(input), timing), context),
  }
}

// OpenCode does not hand a session ID to plugin setup. Read the agent from the
// setup context through the shared adapter, then from the session domain. An
// unknown agent falls back to registering, which is the safe path.
export async function currentSessionAgent(ctx) {
  if (!ctx || typeof ctx !== "object") return null
  const direct = agentIdentity(ctx)
  if (direct) return direct

  const sessionID = ctx.sessionID ?? ctx.session?.id ?? ctx.session?.sessionID
  try {
    return agentFromSession(ctx, await latestSession(ctx, sessionID))
  } catch (error) {
    console.debug(`beacon plugin: session lookup failed: ${error?.message ?? error}`)
    return null
  }
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
        statusRpc = await ctx.rpc.register(BeaconStatusRpc, createStatusRpcHandlers(statusSource, { limits }))
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
      const cleanup = cleanupWrapper("beacon plugin: ")
      await cleanup("run watcher", () => watcher?.stop())
      await cleanup("shell guard", () => guard?.dispose?.())
      await cleanup("project context", context)
      await cleanup("MCP registration", () => mcp?.dispose?.())
      await cleanup("status RPC", () => statusRpc?.dispose?.())
    }
  },
}

// The host supplies Plugin here, so its own OpenTUI and Solid instances are used.
export const beaconProject = Plugin.define(beaconProjectDefinition)

export { agentIdentity } from "./state/session_identity.js"
export {
  SHELL_GUARD_RULES, buildShellGuardRules, commandFromToolInput, commandSegments,
  commandSubstitutions, findShellGuardHit, guardedAgents, guardMode,
  isWorkerAgent, registerShellGuard,
} from "./shell_guard.js"
export { createRunChangeWatcher } from "./run_watcher.js"
export { pluginOptions, pollIntervalMs, snapshotLimits, statusPanelEnabled, watcherEnabled } from "./state/options.js"
export default beaconProject
