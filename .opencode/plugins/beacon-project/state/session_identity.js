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

export const WORKER_ROLE = "worker"
export const ORCHESTRATOR_ROLE = "orchestrator"
export const NO_ROLE = "none"

const DEFAULT_WORKER_AGENTS = Object.freeze(["worker"])

// Only a configured name is a worker. A name that merely contains "worker",
// such as "networker", is not one. Project variants must be named explicitly.
export function guardedAgents(config = {}) {
  const value = config?.shellGuardAgents ?? config?.shellGuardAgent
  if (Array.isArray(value)) {
    const agents = value.filter((agent) => typeof agent === "string" && agent)
    return agents.length ? Object.freeze(agents) : DEFAULT_WORKER_AGENTS
  }
  if (typeof value === "string" && value) return Object.freeze([value])
  return DEFAULT_WORKER_AGENTS
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

// Plugin setup does not always receive a session ID directly. Fall back to the
// newest session, the same way the agent resolver does. The model is optional:
// never invent one when the session domain cannot provide it.
export async function sessionIdentity(ctx) {
  if (!ctx || typeof ctx !== "object") return {}
  let sessionID = ctx.sessionID ?? ctx.session?.id ?? ctx.session?.sessionID
  if (typeof sessionID !== "string" || !sessionID) {
    if (typeof ctx.session?.list !== "function") return {}
    try {
      const listed = await ctx.session.list({ order: "desc", limit: 1 })
      const sessions = Array.isArray(listed) ? listed : listed?.data ?? []
      sessionID = sessions[0]?.id ?? sessions[0]?.sessionID
    } catch { return {} }
  }
  if (typeof sessionID !== "string" || !sessionID) return {}
  const identity = { sessionID }
  const direct = agentIdentity(ctx)
  if (direct) identity.agent = direct
  if (typeof ctx.session?.get !== "function") return identity
  try {
    const info = await ctx.session.get({ sessionID })
    const data = info?.data ?? info
    const ref = data?.model
    const model = ref?.id ?? ref?.modelID ?? ref?.modelId ?? data?.modelID ?? data?.modelId
    if (typeof model === "string" && model) identity.model = model
    if (!identity.agent) {
      const agent = agentIdentity(ctx, data) ?? data?.agent?.id ?? data?.agentID ?? null
      if (agent) identity.agent = agent
    }
  } catch {}
  return identity
}
