import { existsSync as fsExistsSync } from "node:fs"
import { join } from "node:path"

export const SURVIVAL_KEY = "beacon/survival-rules"
export const ORCHESTRATOR_KEY = "beacon/orchestrator-rules"
export const BUILD_REMINDER = 'After a change lands on main, rebuild the local binary with bash scripts/build-beacon.sh, then tell the operator to restart the MCP daemon and the OpenCode TUI.'

// Worker rules. Kept short because they are injected on every context hook.
export const SURVIVAL_RULES = [
  '1) Work in your own worktree and branch. Never push directly to main.',
  '2) Edit only the files listed in Scope: of your task card.',
  '3) Format changed files with the project formatter and run targeted tests for changed packages only. Leave full builds, full test suites and repo-wide linters to CI on weak machines.',
  '4) Use rg -n PATTERN <explicit-directory>. Do not grep -r the whole tree or find /.',
  '5) Worker tools: beacon_dispatch, beacon_watch_pr, beacon_status, beacon_recent, beacon_wait and beacon_control. beacon_task_create, beacon_task_delete, beacon_task_update and beacon_reconcile are for the orchestrator only. No other Beacon tool exists.',
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
  '4) You may use beacon_task_create, beacon_task_delete, beacon_task_update and beacon_reconcile. Workers may not. beacon_task_create accepts {"tasks": [...]}; @N in depends means the Nth card in that batch (1-based).',
  '5) Use beacon_reconcile with {"pr": N} to force one reconciler tick on a waiting PR and diagnose its decision. The result carries the decision, state and error, and a failed tick never stops the daemon loop.',
  '6) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
  '7) Do not re-dispatch a task that failed with workspace conflict or dirty worktree until you find and remove the cause.',
  '8) Before beacon_task_create, check git log -n 10 --oneline, beacon_status and beacon_recent. Never duplicate work that is already merged or in flight.',
  '9) Give each card a narrow, disjoint Scope. Never run two cards that touch ui.js, ui_view.js or reconciler.go at the same time.',
  '10) On any operator complaint or stop, run beacon_status, beacon status --daemon and git status --short before acting. Never dispatch blindly when there are no ready tasks.',
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

// Read-only research agents need no commit or card rules. An unknown agent
// gets nothing rather than the worker rules, because the default OpenCode
// agent is `build`, which is the operator's orchestration session.
export function roleFor(agent) {
  if (typeof agent !== "string" || !agent.trim()) return null
  const name = agent.trim().toLowerCase()
  if (name.includes("issue-scout")) return "none"
  if (name.includes("worker")) return "worker"
  if (name.includes("orchestrator") || name.includes("build")) return "orchestrator"
  return null
}

// The build script only exists in the Beacon repo. Other projects get the same
// universal rules without a reminder to run a script they do not have.
export async function sessionInstructions(ctx, event, config = {}, root = "") {
  const options = contextOptions(config)
  if (!options.enabled) return null
  const role = roleFor(await resolvedAgent(ctx, event))
  if (role !== "worker" && role !== "orchestrator") return null
  let value = role === "orchestrator" ? options.orchestratorRules : options.workerRules
  if (role === "orchestrator" && typeof root === "string" && root
      && fsExistsSync(join(root, "scripts", "build-beacon.sh"))) {
    value = `${value}\n${BUILD_REMINDER}`
  }
  return {
    key: role === "orchestrator" ? ORCHESTRATOR_KEY : SURVIVAL_KEY,
    value,
    sessionID: event?.sessionID ?? event?.session?.id,
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
      const wanted = await sessionInstructions(ctx, event, config, root)
      await ensureRules(ctx, wanted, wanted?.key)
    } catch {}
    if (Array.isArray(event?.system)) {
      event.system.push({ type: "text", text: `beacon project root=${root}` })
    }
  })

  return { contextHook }
}

export async function registerCompactionHook(ctx, root, config = {}) {
  try {
    return await ctx.session.hook("compaction", async (event) => {
      try {
        const wanted = await sessionInstructions(ctx, event, config, root)
        await ensureRules(ctx, wanted, wanted?.key)
      } catch {}
    })
  } catch {
    return null
  }
}

export async function installProjectContext(ctx, root, config = {}) {
  const hooks = await registerProjectHooks(ctx, root, config)
  const compactionHook = await registerCompactionHook(ctx, root, config)

  return async () => {
    await compactionHook?.dispose?.()
    await hooks.contextHook?.dispose?.()
  }
}
