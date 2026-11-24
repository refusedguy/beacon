// The CLI has no MCP _meta, so the operator session and model travel in the
// child environment. Read-only calls still work when either value is absent.

export function cliEnv(root, { sessionID, model, env = process.env } = {}) {
  const out = { ...env, BEACON_PROJECT_ROOT: root }
  if (typeof sessionID === "string" && sessionID) out.BEACON_PARENT_SESSION = sessionID
  if (typeof model === "string" && model) out.BEACON_MODEL = model
  return out
}

// Plugin setup does not always receive a session ID directly. Fall back to the
// newest session, the same way currentSessionAgent resolves the agent. The
// model is optional: never invent one when the session domain cannot provide it.
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
  if (typeof ctx.session?.get !== "function") return identity
  try {
    const info = await ctx.session.get({ sessionID })
    const data = info?.data ?? info
    const ref = data?.model
    const model = ref?.id ?? ref?.modelID ?? ref?.modelId ?? data?.modelID ?? data?.modelId
    if (typeof model === "string" && model) identity.model = model
  } catch {}
  return identity
}
