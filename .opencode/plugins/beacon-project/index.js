// T3.176. Точка входа локального плагина Beacon (ADR 0069, решение 1).
// Плагин лежит в .opencode/plugins и загружается автоматически, без записи
// в глобальный opencode.json. Серверная часть объявляется через Plugin.define
// и экспортируется по умолчанию.
//
// BCX-82. Запрет рекурсивного поиска и локальных гейтов стоит на границе
// инструмента. Текст в AGENTS.md ничего не блокирует, поэтому решение живёт
// здесь, в хуке execute.before.

import { Plugin } from "@opencode/plugin"
import { locationInfo, projectRoot } from "./location.js"
import { registerBeaconMcp } from "./mcp.js"
import { installProjectContext } from "./context.js"

export const SHELL_GUARD_RULES = Object.freeze([
  {
    id: "recursive grep",
    pattern: /(?:^|[;&|]\s*)grep\s+-(?:[A-Za-z]*[rR][A-Za-z]*)\b/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "root find",
    pattern: /(?:^|[;&|]\s*)find\s+\/(?=\s|$|[;&|])/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "local go test",
    pattern: /(?:^|[;&|]\s*)go\s+test\b/,
    replacement: "GitHub Actions CI on the PR",
  },
  {
    id: "local go build",
    pattern: /(?:^|[;&|]\s*)go\s+build\b/,
    replacement: "GitHub Actions CI on the PR",
  },
  {
    id: "local beacon gates",
    pattern: /(?:^|[;&|]\s*)beacon\s+gates\b/,
    replacement: "GitHub Actions CI on the PR",
  },
])

const GUARDED_TOOLS = new Set(["bash", "shell", "execute"])

export function commandFromToolInput(input) {
  if (typeof input === "string") return input
  if (!input || typeof input !== "object") return ""
  for (const key of ["command", "cmd", "code", "script"]) {
    if (typeof input[key] === "string") return input[key]
  }
  const args = input.args ?? input.arguments
  if (typeof args === "string") return args
  if (Array.isArray(args)) return args.filter((part) => typeof part === "string").join(" ")
  return ""
}

export function findShellGuardHit(tool, input) {
  if (!GUARDED_TOOLS.has(tool)) return null
  const command = commandFromToolInput(input)
  if (!command) return null
  return SHELL_GUARD_RULES.find((rule) => rule.pattern.test(command)) ?? null
}

// BCX-82. Режим warn дает неделю наблюдений без слома сессий. Переменная
// BEACON_SHELL_GUARD=warn пишет предупреждение и пропускает команду. Любое
// другое значение, включая отсутствие переменной, блокирует команду.
export async function registerShellGuard(ctx) {
  if (typeof ctx?.tool?.hook !== "function") return null
  try {
    return await ctx.tool.hook("execute.before", (event) => {
      const hit = findShellGuardHit(event?.tool, event?.input)
      if (!hit) return
      const message = `BCX-82: ${hit.id} is blocked here. Use ${hit.replacement}.`
      console.warn(message)
      if (process.env.BEACON_SHELL_GUARD === "warn") return
      throw new Error(message)
    })
  } catch {
    // Fail-open: старый хост без tool.hook продолжает работу.
    return null
  }
}

export const beaconProject = Plugin.define({
  id: "beacon-project",
  async setup(ctx) {
    const root = projectRoot(ctx)
    // Старый хост без ctx.location оставляет поведение fail-open: плагин
    // не трогает MCP и не пишет в сессии чужого проекта.
    if (!root) return

    const mcp = await registerBeaconMcp(ctx, root)
    const context = await installProjectContext(ctx, root, locationInfo(ctx))
    const guard = await registerShellGuard(ctx)

    return async () => {
      await guard?.dispose?.()
      await context?.()
      await mcp?.registration?.dispose?.()
    }
  },
})

export default beaconProject
