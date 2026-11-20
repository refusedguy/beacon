// T3.178. Проектный контекст сессий через инструкции и хуки (ADR 0069).
//
// Постоянный контекст лежит в ctx.session.instructions.entry.put под
// фиксированными ключами: повтор промпта не создаёт вторую копию. Хук context
// меняет только исходящий вызов, хук prompt работает до admission и добавляет
// AGENTS.md файлом без побочных эффектов. ctx.location описывает плагин, а не
// сессию, поэтому принадлежность сессии проверяется отдельно.

import { readFileSync, readdirSync, watch } from "node:fs"
import { join, resolve, sep } from "node:path"
import { pathToFileURL } from "node:url"

export const CARD_KEY = "beacon/open-cards"
export const AGENTS_KEY = "beacon/agents-md"

export function readOpenCards(root) {
  const dir = join(root, "docs", "tasks")
  let names
  try {
    names = readdirSync(dir)
  } catch {
    return ""
  }
  const lines = []
  for (const name of names.filter((item) => item.endsWith(".md")).sort()) {
    let text
    try {
      text = readFileSync(join(dir, name), "utf8")
    } catch {
      continue
    }
    const status = text.match(/^Статус:\s*(\S+)\s*$/m)
    if (!status || status[1] !== "todo") continue
    const title = (text.match(/^#\s+(.+)$/m)?.[1] ?? name).trim()
    lines.push(`- ${name}: ${title}`)
  }
  return lines.join("\n")
}

export function readAgents(root) {
  try {
    return readFileSync(join(root, "AGENTS.md"), "utf8")
  } catch {
    return ""
  }
}

export function projectContext(root) {
  return Object.freeze({
    [CARD_KEY]: readOpenCards(root),
    [AGENTS_KEY]: readAgents(root),
  })
}

// Сессия относится к экземпляру плагина, когда её каталог совпадает с
// каталогом локации или лежит внутри него. Сессии другого проекта отсекаются.
export function sessionBelongsToLocation(ctx, session) {
  const location = ctx?.location
  const expected = location?.directory || location?.project?.canonical
  const actual = session?.directory
  if (typeof expected !== "string" || !expected) return false
  if (typeof actual !== "string" || !actual) return false
  const base = resolve(expected)
  const dir = resolve(actual)
  return dir === base || dir.startsWith(base + sep)
}

export async function listLocationSessions(ctx) {
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

export async function syncSessionInstructions(ctx, root, sessions) {
  const entries = projectContext(root)
  let written = 0
  for (const item of sessions ?? []) {
    const session =
      typeof item === "string" ? await ctx.session.get({ sessionID: item }) : item
    if (!sessionBelongsToLocation(ctx, session)) continue
    const sessionID = session.id
    if (typeof sessionID !== "string" || !sessionID) continue
    for (const [key, value] of Object.entries(entries)) {
      await ctx.session.instructions.entry.put({ sessionID, key, value })
      written++
    }
  }
  return written
}

export async function registerProjectHooks(ctx, root) {
  const agentsURI = pathToFileURL(join(root, "AGENTS.md")).href

  const contextHook = await ctx.session.hook("context", (event) => {
    // Метка нужна только в исходящем вызове: в историю она не попадает.
    // Карточки и AGENTS.md уже лежат в instructions.entry под своими ключами.
    event.system.push({ type: "text", text: `beacon project root=${root}` })
  })

  const promptHook = await ctx.session.hook("prompt", (event) => {
    event.prompt.files ??= []
    // Повтор промпта не должен добавлять вторую копию файла.
    if (event.prompt.files.some((file) => file?.uri === agentsURI)) return
    event.prompt.files.push({ uri: agentsURI })
  })

  return { contextHook, promptHook }
}

// Обновление записей при изменении файлов проекта. Возвращает функцию
// остановки: watcher не должен удерживать процесс или плагин после unload.
export function watchProjectContext(root, refresh) {
  const watchers = []
  let timer = null
  const changed = () => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      timer = null
      void refresh()
    }, 50)
    timer.unref?.()
  }
  for (const target of [join(root, "AGENTS.md"), join(root, "docs", "tasks")]) {
    try {
      const watcher = watch(target, changed)
      watcher.unref?.()
      watchers.push(watcher)
    } catch {
      // Fail-open: отсутствие файла не ломает плагин.
    }
  }
  return () => {
    if (timer) clearTimeout(timer)
    for (const watcher of watchers) watcher.close()
  }
}

export async function installProjectContext(ctx, root) {
  const sync = async () =>
    syncSessionInstructions(ctx, root, await listLocationSessions(ctx))

  await sync()
  const hooks = await registerProjectHooks(ctx, root)
  const stopWatch = watchProjectContext(root, () => {
    void sync().catch(() => {})
  })

  return async () => {
    stopWatch()
    await hooks.contextHook?.dispose?.()
    await hooks.promptHook?.dispose?.()
  }
}
