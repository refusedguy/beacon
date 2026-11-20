// W1-E. Компактный проектный контекст сессий (ADR 0069).
//
// Раньше плагин перебирал все открытые карточки проекта и писал каждую в
// постоянные инструкции каждой сессии. На проекте с сотнями карточек это
// раздувало контекст бесплатных моделей и тормозило слабый ПК.
// Теперь сессия получает только метку `beacon project root=${root}` в исходящем
// вызове и привязку к AGENTS.md файлом. Ни записей в сессию, ни слежения за
// файлами нет: контекст дешёвый и не зависит от изменений на диске.
//
// ctx.location описывает плагин, а не сессию. Хуки не пишут состояние сессии,
// поэтому проверка принадлежности больше не нужна.

import { join } from "node:path"
import { pathToFileURL } from "node:url"

export function agentsFileURI(root) {
  return pathToFileURL(join(root, "AGENTS.md")).href
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

export async function installProjectContext(ctx, root /*, locationInfo */) {
  const hooks = await registerProjectHooks(ctx, root)
  return async () => {
    await hooks.contextHook?.dispose?.()
    await hooks.promptHook?.dispose?.()
  }
}
