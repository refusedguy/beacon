// W1-E. Компактный контекст и anti-compaction правила (ADR 0069, ADR 0072).
//
// Карточки больше не загружаются, за файлами не следим. В сессию пишется один
// стабильный ключ с фиксированной шпаргалкой: после сжатия контекста правила
// остаются в instructions и не копируются при повторах. Хук compaction
// перезаписывает тот же ключ, если хост его поддерживает.

import { join, resolve, sep } from "node:path"
import { pathToFileURL } from "node:url"

export const SURVIVAL_KEY = "beacon/survival-rules"

export const SURVIVAL_RULES = [
  '1) Работаешь строго в своём worktree и своей ветке. Коммит/пуш в main запрещён.',
  '2) Локально НЕ запускаешь go test ./..., go build ./..., beacon gates (слабый ПК, всё крутит GitHub Actions CI). Локально только gofmt -w на свои .go файлы.',
  '3) Меняешь ТОЛЬКО файлы из своего OwnerFiles. Чужие файлы и карточки docs/tasks/ не трогаешь.',
  '4) Запрещено gh pr checks --watch или sleep в цикле.',
  '5) После gh pr create вызываешь beacon_watch_pr с role: "executor" и своим реальным ses_..., завершаешь ход фразой waiting, <watchID>.',
  '6) При пробуждении читаешь beacon_get_result, чинишь ТОЛЬКО перечисленные failedChecks; если падение из-за main, вызываешь beacon_ack с decision="blocked:not_mine".',
].join("\n")

export function agentsFileURI(root) {
  return pathToFileURL(join(root, "AGENTS.md")).href
}

// Сессия относится к локации плагина по каталогу worktree или каноническому
// каталогу проекта. Сессии другого проекта не получают правила.
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

  // Метка нужна только в исходящем вызове, в историю она не попадает.
  const contextHook = await ctx.session.hook("context", (event) => {
    event.system.push({ type: "text", text: `beacon project root=${root}` })
  })

  // prompt-хук работает до admission и добавляет AGENTS.md файлом.
  // Повтор промпта не должен добавлять вторую копию файла.
  const promptHook = await ctx.session.hook("prompt", (event) => {
    event.prompt.files ??= []
    if (event.prompt.files.some((file) => file?.uri === agentsURI)) return
    event.prompt.files.push({ uri: agentsURI })
  })

  return { contextHook, promptHook }
}

// Старый хост может не знать хук compaction. Неизвестное имя должно
// проглатываться: плагин продолжает работать без перезаписи правил.
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
        // Fail-open: ошибка перезаписи не ломает сжатие.
      }
    })
  } catch {
    return null
  }
}

export async function installProjectContext(ctx, root /*, locationInfo */) {
  try {
    await syncSurvivalRules(ctx, await listSessions(ctx))
  } catch {
    // Fail-open: старый хост без ctx.location или session.list.
  }

  const hooks = await registerProjectHooks(ctx, root)
  const compactionHook = await registerCompactionHook(ctx)

  return async () => {
    await compactionHook?.dispose?.()
    await hooks.contextHook?.dispose?.()
    await hooks.promptHook?.dispose?.()
  }
}
