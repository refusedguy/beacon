export const SURVIVAL_KEY = "beacon/survival-rules"
export const ORCHESTRATOR_KEY = "beacon/orchestrator-rules"

// Worker rules. Kept short because they are injected on every context hook.
export const SURVIVAL_RULES = [
  '1) Work in your own worktree and branch. Never push directly to main.',
  '2) Edit only the files listed in Scope: of your task card.',
  '3) Run gofmt -w on changed Go files, then targeted go test for changed packages. Full go test ./... and go vet ./... run in CI on weak machines.',
  '4) Use rg -n PATTERN <explicit-directory>. Do not grep -r the whole tree or find /.',
  '5) Worker tools: beacon_dispatch, beacon_watch_pr, beacon_status, beacon_recent, beacon_wait and beacon_control. beacon_task_create, beacon_task_delete and beacon_task_update are for the orchestrator only. No other Beacon tool exists.',
  '6) After opening a pull request, call beacon_watch_pr with your real run ID and PR number.',
  '7) Use beacon_wait to wait and beacon_status or beacon_recent to inspect. No gh pr checks --watch and no sleep loop.',
  '8) Use beacon_control only for approve, unblock, cancel and retry on a run.',
  '9) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
  '10) Fix a failed check when it is in your diff. Otherwise report the blocker in your final message.',
].join("\n")

export const ORCHESTRATOR_RULES = [
  '1) Stay on the main checkout. Workers own their worktrees; do not edit their files.',
  '2) Decompose work into task cards with a narrow Scope: and explicit Depends:.',
  '3) Check the board with beacon_status and beacon_recent before beacon_dispatch, so you do not duplicate active work.',
  '4) After a Go change lands on main, rebuild the local binary with bash scripts/build-beacon.sh before the next dispatch, then tell the operator to restart the MCP daemon and OpenCode TUI.',
  '5) You may use beacon_task_create, beacon_task_delete, beacon_task_update and beacon_reconcile. Workers may not. beacon_task_create accepts {"tasks": [...]}; @N in depends means the Nth card in that batch (1-based).',
  '6) Use beacon_reconcile with {"pr": N} to force one reconciler tick on a waiting PR and diagnose its decision. The result carries the decision, state and error, and a failed tick never stops the daemon loop.',
  '7) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
].join("\n")

// Plugin options can arrive under options or the full config object.
export function contextOptions(config = {}) {
  const opts = config && typeof config === "object" ? config : {}
  return {
    enabled: opts.survivalRulesEnabled !== false,
    orchestratorRules: typeof opts.orchestratorRules === "string" && opts.orchestratorRules
      ? opts.orchestratorRules : ORCHESTRATOR_RULES,
    workerRules: typeof opts.workerRules === "string" && opts.workerRules
      ? opts.workerRules : SURVIVAL_RULES,
  }
}

// OpenCode puts the agent on the context event when it is available. Some
// versions only expose a session ID, so fall back to ctx.session.get.
export function eventAgent(event) {
  const candidates = [
    event?.agent, event?.agentName, event?.agentID,
    event?.session?.agent, event?.session?.agentName, event?.session?.agentID,
    event?.data?.agent,
  ]
  for (const value of candidates) {
    if (typeof value === "string" && value) return value
  }
  return null
}

async function resolvedAgent(ctx, event) {
  const direct = eventAgent(event)
  if (direct) return direct
  const sessionID = event?.sessionID ?? event?.session?.id
  if (typeof sessionID !== "string" || !sessionID) return null
  if (typeof ctx?.session?.get !== "function") return null
  try {
    const info = await ctx.session.get({ sessionID })
    const data = info?.data ?? info
    return eventAgent(data) ?? data?.agent?.id ?? data?.agentID ?? null
  } catch {
    return null
  }
}

export async function sessionInstructions(ctx, event, config = {}) {
  const options = contextOptions(config)
  if (!options.enabled) return null
  const agent = await resolvedAgent(ctx, event)
  const sessionID = event?.sessionID ?? event?.session?.id
  const orchestrator = typeof agent === "string" && /orchestrator/i.test(agent)
  return {
    key: orchestrator ? ORCHESTRATOR_KEY : SURVIVAL_KEY,
    value: orchestrator ? options.orchestratorRules : options.workerRules,
    sessionID,
  }
}

export async function ensureRules(ctx, wanted, key) {
  if (!wanted || typeof wanted.sessionID !== "string" || !wanted.sessionID) return false
  const entry = ctx?.session?.instructions?.entry
  if (typeof entry?.list !== "function" || typeof entry?.put !== "function") return false

  const listed = await entry.list({ sessionID: wanted.sessionID })
  const entries = Array.isArray(listed) ? listed : listed?.entries
  const current = Array.isArray(entries)
    ? entries.find((item) => item?.key === key)
    : null
  if (current?.value === wanted.value) return false

  await entry.put({ sessionID: wanted.sessionID, key, value: wanted.value })
  return true
}

export async function ensureSurvivalRules(ctx, sessionID, rules = SURVIVAL_RULES) {
  return ensureRules(ctx, { sessionID, value: rules }, SURVIVAL_KEY)
}

export async function registerProjectHooks(ctx, root, config = {}) {
  const contextHook = await ctx.session.hook("context", async (event) => {
    try {
      const wanted = await sessionInstructions(ctx, event, config)
      await ensureRules(ctx, wanted, wanted?.key)
    } catch {}
    if (Array.isArray(event?.system)) {
      event.system.push({ type: "text", text: `beacon project root=${root}` })
    }
  })

  return { contextHook }
}

export async function registerCompactionHook(ctx, config = {}) {
  try {
    return await ctx.session.hook("compaction", async (event) => {
      try {
        const wanted = await sessionInstructions(ctx, event, config)
        await ensureRules(ctx, wanted, wanted?.key)
      } catch {}
    })
  } catch {
    return null
  }
}

export async function installProjectContext(ctx, root, config = {}) {
  const hooks = await registerProjectHooks(ctx, root, config)
  const compactionHook = await registerCompactionHook(ctx, config)

  return async () => {
    await compactionHook?.dispose?.()
    await hooks.contextHook?.dispose?.()
  }
}
