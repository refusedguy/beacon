// The CLI has no MCP _meta, so the operator session and model travel in the
// child environment. Read-only calls still work when either value is absent.

export function cliEnv(root, { sessionID, model, env = process.env } = {}) {
  const out = { ...env, BEACON_PROJECT_ROOT: root }
  if (typeof sessionID === "string" && sessionID) out.BEACON_PARENT_SESSION = sessionID
  if (typeof model === "string" && model) out.BEACON_MODEL = model
  return out
}

// OpenCode exposes the session agent under different keys at setup, on hook
// events and in session records. Read the union of those keys in one place so
// the shell guard and MCP registration classify the same event identically.
// The event wins over the setup context because it names the current session.
export function agentIdentity(ctx, event) {
  for (const source of [event, ctx]) {
    const candidates = [
      source?.agent, source?.agentName, source?.agentID,
      source?.session?.agent, source?.session?.agentName, source?.session?.agentID,
      source?.data?.agent,
    ]
    for (const value of candidates) {
      if (typeof value === "string" && value) return value
    }
  }
  return null
}

// One latest-session lookup for identity, rules and MCP. A known ID uses
// session.get and falls back to the newest entry when that fails. Records come
// bare from session.list and under `data` from session.get, so unwrap here.
export async function latestSession(ctx, sessionID = null) {
  if (!ctx || typeof ctx !== "object") return null
  if (typeof sessionID === "string" && sessionID && typeof ctx.session?.get === "function") {
    try {
      const info = await ctx.session.get({ sessionID })
      return info?.data ?? info ?? null
    } catch {}
  }
  if (typeof ctx.session?.list === "function") {
    const listed = await ctx.session.list({ order: "desc", limit: 1 })
    const sessions = Array.isArray(listed) ? listed : listed?.data ?? []
    return sessions[0] ?? null
  }
  return null
}

// One agent extraction from a session record. The agent travels under several
// keys, so read the union in one place and let every caller agree on the id.
export function agentFromSession(ctx, data) {
  return agentIdentity(ctx, data) ?? data?.agent?.id ?? data?.agentID ?? null
}

export const WORKER_ROLE = "worker"
export const ORCHESTRATOR_ROLE = "orchestrator"
export const NO_ROLE = "none"

const DEFAULT_WORKER_AGENTS = Object.freeze(["worker"])

// Config objects come from plugin setup and stay stable for the session. Cache
// the frozen list by object identity so repeated role checks do not rebuild it.
const guardedAgentsCache = new WeakMap()

const isCacheableConfig = (config) => config !== null && (typeof config === "object" || typeof config === "function")

// Only a configured name is a worker. A name that merely contains "worker",
// such as "networker", is not one. Project variants must be named explicitly.
function resolveGuardedAgents(config) {
  const value = config?.shellGuardAgents ?? config?.shellGuardAgent
  if (Array.isArray(value)) {
    const agents = value.filter((agent) => typeof agent === "string" && agent)
    return agents.length ? Object.freeze(agents) : DEFAULT_WORKER_AGENTS
  }
  if (typeof value === "string" && value) return Object.freeze([value])
  return DEFAULT_WORKER_AGENTS
}

export function guardedAgents(config = {}) {
  if (!isCacheableConfig(config)) return resolveGuardedAgents(config)
  let agents = guardedAgentsCache.get(config)
  if (!agents) {
    agents = resolveGuardedAgents(config)
    guardedAgentsCache.set(config, agents)
  }
  return agents
}

// One classification for rules, the shell guard and MCP. Callers ask the same
// question: is this agent a worker, the operator's orchestrator, or neither?
// Worker names come from config and match exactly, as do the two orchestrator
// names. A configured worker name wins so a project can rename a worker.
export function agentRole(agent, config = {}) {
  if (typeof agent !== "string" || !agent.trim()) return null
  const name = agent.trim().toLowerCase()
  if (guardedAgents(config).some((worker) => worker.trim().toLowerCase() === name)) {
    return WORKER_ROLE
  }
  if (name === "build" || name === "orchestrator") return ORCHESTRATOR_ROLE
  if (name === "issue-scout") return NO_ROLE
  return null
}

// A session's identity is fixed for its lifetime. The model and agent come
// from a session lookup, so reuse the first result for every later caller.
const identityCache = new Map()

// Plugin setup does not always receive a session ID directly. Fall back to the
// newest session, the same way the agent resolver does. The model is optional:
// never invent one when the session domain cannot provide it.
export async function sessionIdentity(ctx) {
  if (!ctx || typeof ctx !== "object") return {}
  let sessionID = ctx.sessionID ?? ctx.session?.id ?? ctx.session?.sessionID
  if (typeof sessionID !== "string" || !sessionID) {
    try {
      const latest = await latestSession(ctx)
      sessionID = latest?.id ?? latest?.sessionID
    } catch { return {} }
  }
  if (typeof sessionID !== "string" || !sessionID) return {}
  if (identityCache.has(sessionID)) return identityCache.get(sessionID)
  const identity = { sessionID }
  const direct = agentIdentity(ctx)
  if (direct) identity.agent = direct
  try {
    const data = await latestSession(ctx, sessionID)
    if (data) {
      const ref = data.model
      const model = ref?.id ?? ref?.modelID ?? ref?.modelId ?? data.modelID ?? data.modelId
      if (typeof model === "string" && model) identity.model = model
      if (!identity.agent) {
        const agent = agentFromSession(ctx, data)
        if (agent) identity.agent = agent
      }
    }
  } catch {}
  identityCache.set(sessionID, identity)
  return identity
}
