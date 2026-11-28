import { existsSync as fsExistsSync } from "node:fs"
import { join } from "node:path"
import { BEACON_BUILD_SCRIPT } from "../mcp/mcp.js"
import { ORCHESTRATOR_ROLE, WORKER_ROLE, agentRole, resolveAgent, sessionIDOf } from "../state/session_identity.js"

// BEGIN BEACON DOCS: context-tools
const WORKER_TOOLS = [
  "beacon_watch_pr",
  "beacon_wait",
]
const ORCHESTRATOR_TOOLS = [
  "beacon_dispatch",
  "beacon_status",
  "beacon_recent",
  "beacon_control",
  "beacon_task_create",
  "beacon_task_delete",
  "beacon_task_update",
  "beacon_task_close",
  "beacon_task_start",
  "beacon_reconcile",
]
// END BEACON DOCS: context-tools

export const SURVIVAL_KEY = "beacon/survival-rules"
export const ORCHESTRATOR_KEY = "beacon/orchestrator-rules"
export const BUILD_REMINDER = `After a change lands on main, rebuild the local binary with bash ${BEACON_BUILD_SCRIPT}, then tell the operator to restart the MCP daemon and the OpenCode TUI.`

// Worker rules. Kept short because they are injected on every context hook.
export const SURVIVAL_RULES = [
  '1) Work in your own worktree and branch. Never push directly to main.',
  '2) Edit only the files listed in Scope: of your task card.',
  '3) Format changed files with the project formatter and run targeted tests for changed packages only. Leave full builds, full test suites and repo-wide linters to CI on weak machines.',
  '4) Use rg -n PATTERN <explicit-directory>. Do not grep -r the whole tree or find /.',
  `5) Worker tools: ${WORKER_TOOLS.join(", ")}. ${ORCHESTRATOR_TOOLS.join(", ")} are for the orchestrator only. No other Beacon tool exists.`,
  '6) After opening a pull request, call beacon_watch_pr with your real run ID and PR number.',
  '7) Use beacon_wait to wait for a verdict. The orchestrator inspects with beacon_status and beacon_recent. No gh pr checks --watch and no sleep loop.',
  '8) Only the orchestrator may use beacon_control, for approve, unblock, cancel and retry on a run.',
  '9) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
  '10) Fix a failed check when it is in your diff. Otherwise report the blocker in your final message.',
].join("\n")

export const ORCHESTRATOR_RULES = [
  '1) Stay on the main checkout. Workers own their worktrees; do not edit their files.',
  '2) Decompose work into task cards with a narrow Scope: and explicit Depends:.',
  '3) Check the board with beacon_status and beacon_recent before beacon_dispatch, so you do not duplicate active work.',
  `4) You may use ${ORCHESTRATOR_TOOLS.join(", ")}. Workers may not. beacon_task_create accepts {"tasks": [...]}; @N in depends means the Nth card in that batch (1-based).`,
  '5) Use beacon_reconcile with {"pr": N} to force one reconciler tick on a waiting PR and diagnose its decision. The result carries the decision, state and error, and a failed tick never stops the daemon loop.',
  '6) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
  '7) Do not re-dispatch a task that failed with workspace conflict or dirty worktree until you find and remove the cause.',
  '8) Before beacon_task_create, check git log -n 10 --oneline, beacon_status and beacon_recent. Never duplicate work that is already merged or in flight.',
  '9) Give each card a narrow, disjoint Scope. Never run two cards that touch ui.js, ui_view.js or reconciler.go at the same time.',
  '10) On any operator complaint or stop, run beacon_status, beacon status --daemon and git status --short before acting. Never dispatch blindly when there are no ready tasks.',
].join("\n")

// Plugin options can arrive under options or the full config object.
function contextOptions(config = {}) {
  const opts = config && typeof config === "object" ? config : {}
  return {
    enabled: opts.survivalRulesEnabled !== false,
    orchestratorRules: typeof opts.orchestratorRules === "string" && opts.orchestratorRules
      ? opts.orchestratorRules : ORCHESTRATOR_RULES,
    workerRules: typeof opts.workerRules === "string" && opts.workerRules
      ? opts.workerRules : SURVIVAL_RULES,
  }
}

// Rules follow the same classifier as the shell guard and MCP. Read-only
// research agents and unknown names get no commit or card rules. The default
// OpenCode agent is `build`, which is the operator's orchestration session.
export function roleFor(agent, config = {}) {
  return agentRole(agent, config)
}

// The build script only exists in the Beacon repo. Other projects get the same
// universal rules without a reminder to run a script they do not have. The
// context hook fires on every event, so keep the existence answer for one root
// and stat again only when the root changes.
let cachedRoot = null
let cachedBuildScript = false

function hasBuildScript(root, existsSync) {
  if (cachedRoot === root) return cachedBuildScript
  cachedRoot = root
  cachedBuildScript = !!existsSync(join(root, ...BEACON_BUILD_SCRIPT.split("/")))
  return cachedBuildScript
}

export async function sessionInstructions(ctx, event, config = {}, root = "", deps = {}) {
  const options = contextOptions(config)
  if (!options.enabled) return null
  const role = roleFor(await resolveAgent(ctx, event), config)
  if (role !== WORKER_ROLE && role !== ORCHESTRATOR_ROLE) return null
  let value = role === ORCHESTRATOR_ROLE ? options.orchestratorRules : options.workerRules
  const existsSync = deps.existsSync ?? fsExistsSync
  if (role === ORCHESTRATOR_ROLE && typeof root === "string" && root
      && hasBuildScript(root, existsSync)) {
    value = `${value}\n${BUILD_REMINDER}`
  }
  return {
    key: role === ORCHESTRATOR_ROLE ? ORCHESTRATOR_KEY : SURVIVAL_KEY,
    value,
    sessionID: sessionIDOf(event),
  }
}

async function ensureRules(ctx, wanted, key) {
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

async function registerProjectHooks(ctx, root, config = {}) {
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

async function registerCompactionHook(ctx, root, config = {}) {
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
