export const SURVIVAL_KEY = "beacon/survival-rules"

export const SURVIVAL_RULES = [
  '1) Work in your own worktree and branch. Never push directly to main.',
  '2) Edit only the files listed in Scope: of your task card.',
  '3) Run gofmt -w on changed Go files, then targeted go test for changed packages. Full go test ./... and go vet ./... run in CI on weak machines.',
  '4) Use rg -n PATTERN <explicit-directory>. Do not grep -r the whole tree or find /.',
  '5) Workers use only these five MCP tools: beacon_dispatch, beacon_watch_pr, beacon_status, beacon_wait, beacon_control. The sixth tool, beacon_task_create, is for the orchestrator only. No other Beacon tool exists.',
  '6) After opening a pull request, call beacon_watch_pr with your real run ID and PR number.',
  '7) Use beacon_wait to wait and beacon_status to inspect. No gh pr checks --watch and no sleep loop.',
  '8) Use beacon_control only for approve, unblock, cancel and retry on a run.',
  '9) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
  '10) Fix a failed check when it is in your diff. Otherwise report the blocker in your final message.',
].join("\n")


export async function ensureSurvivalRules(ctx, sessionID) {
  if (typeof sessionID !== "string" || !sessionID) return false
  const entry = ctx?.session?.instructions?.entry
  if (typeof entry?.list !== "function" || typeof entry?.put !== "function") return false

  const listed = await entry.list({ sessionID })
  const entries = Array.isArray(listed) ? listed : listed?.entries
  const current = Array.isArray(entries)
    ? entries.find((item) => item?.key === SURVIVAL_KEY)
    : null
  if (current?.value === SURVIVAL_RULES) return false

  await entry.put({ sessionID, key: SURVIVAL_KEY, value: SURVIVAL_RULES })
  return true
}

export async function registerProjectHooks(ctx, root) {
  const contextHook = await ctx.session.hook("context", async (event) => {
    try {
      await ensureSurvivalRules(ctx, event?.sessionID)
    } catch {}
    event.system.push({ type: "text", text: `beacon project root=${root}` })
  })

  return { contextHook }
}

export async function registerCompactionHook(ctx) {
  try {
    return await ctx.session.hook("compaction", async (event) => {
      try {
        await ensureSurvivalRules(ctx, event?.sessionID ?? event?.session?.id)
      } catch {}
    })
  } catch {
    return null
  }
}

export async function installProjectContext(ctx, root) {
  const hooks = await registerProjectHooks(ctx, root)
  const compactionHook = await registerCompactionHook(ctx)

  return async () => {
    await compactionHook?.dispose?.()
    await hooks.contextHook?.dispose?.()
  }
}
