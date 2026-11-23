import { join, resolve, sep } from "node:path"
import { pathToFileURL } from "node:url"

export const SURVIVAL_KEY = "beacon/survival-rules"

export const SURVIVAL_RULES = [
  '1) Work in your own worktree and branch. Never push directly to main.',
  '2) Edit only the files listed in Scope: of your task card.',
  '3) Run gofmt -w on changed Go files, then targeted go test for changed packages. Full go test ./... and go vet ./... run in CI on weak machines.',
  '4) Use rg -n PATTERN <explicit-directory>. Do not grep -r the whole tree or find /.',
  '5) Use only these five MCP tools: beacon_dispatch, beacon_watch_pr, beacon_status, beacon_wait, beacon_control. No other Beacon tool exists.',
  '6) After opening a pull request, call beacon_watch_pr with your real run ID and PR number.',
  '7) Use beacon_wait to wait and beacon_status to inspect. No gh pr checks --watch and no sleep loop.',
  '8) Use beacon_control only for approve, unblock, cancel and retry on a run.',
  '9) Write prose with unslop: no em dashes, simple words, active voice, no filler.',
  '10) Fix a failed check when it is in your diff. Otherwise report the blocker in your final message.',
].join("\n")

export function agentsFileURI(root) {
  return pathToFileURL(join(root, "AGENTS.md")).href
}

export function belongsToLocation(ctx, session) {
  const location = ctx?.location
  const expected = location?.directory || location?.project?.canonical
  const actual = session?.directory
  if (typeof expected !== "string" || !expected) return false
  if (typeof actual !== "string" || !actual) return false
  const base = resolve(expected)
  const dir = resolve(actual)
  return dir === base || dir.startsWith(base + sep)
}

export async function listSessions(ctx) {
  const sessions = []
  let cursor = null
  for (;;) {
    const input = { directory: ctx.location.directory, limit: 100 }
    if (cursor) input.cursor = cursor
    const page = await ctx.session.list(input)
    sessions.push(...(page?.sessions ?? []))
    if (!page?.cursor?.next) break
    cursor = page.cursor.next
  }
  return sessions
}

async function putSurvivalRules(ctx, sessionID) {
  if (typeof sessionID !== "string" || !sessionID) return false
  const entry = ctx?.session?.instructions?.entry
  if (typeof entry?.put !== "function") return false
  await entry.put({ sessionID, key: SURVIVAL_KEY, value: SURVIVAL_RULES })
  return true
}

export async function syncSurvivalRules(ctx, sessions) {
  let written = 0
  for (const item of sessions ?? []) {
    const session =
      typeof item === "string" ? await ctx.session.get({ sessionID: item }) : item
    if (!belongsToLocation(ctx, session)) continue
    if (await putSurvivalRules(ctx, session?.id)) written++
  }
  return written
}

export async function registerProjectHooks(ctx, root) {
  const agentsURI = agentsFileURI(root)

  const contextHook = await ctx.session.hook("context", (event) => {
    event.system.push({ type: "text", text: `beacon project root=${root}` })
  })

  const promptHook = await ctx.session.hook("prompt", (event) => {
    event.prompt.files ??= []
    if (event.prompt.files.some((file) => file?.uri === agentsURI)) return
    event.prompt.files.push({ uri: agentsURI })
  })

  return { contextHook, promptHook }
}

export async function registerCompactionHook(ctx) {
  try {
    return await ctx.session.hook("compaction", async (event) => {
      try {
        const sessionID = event?.sessionID ?? event?.session?.id
        if (sessionID) {
          await putSurvivalRules(ctx, sessionID)
          return
        }
        await syncSurvivalRules(ctx, await listSessions(ctx))
      } catch {
        return
      }
    })
  } catch {
    return null
  }
}

export async function installProjectContext(ctx, root) {
  try {
    await syncSurvivalRules(ctx, await listSessions(ctx))
  } catch {}

  const hooks = await registerProjectHooks(ctx, root)
  const compactionHook = await registerCompactionHook(ctx)

  return async () => {
    await compactionHook?.dispose?.()
    await hooks.contextHook?.dispose?.()
    await hooks.promptHook?.dispose?.()
  }
}
