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

// CS-01. XS-01 из docs/research/comp-claude-sdk.md. SDK несёт типизированный
// вход PreToolUse/PostToolUse и решение permissionDecision, а fail-closed
// проверку способности ставит до регистрации (_internal/query.py:396-409).
// Здесь тот же контракт поверх хуков OpenCode. guard из BCX-82 остаётся
// отдельным execute.before и не меняет своё поведение.
export const TOOL_LIFECYCLE_EVENTS = Object.freeze({
  pre: "PreToolUse",
  post: "PostToolUse",
})

export const PERMISSION_DECISIONS = Object.freeze(["allow", "deny", "ask", "defer"])
export const ON_FAILURE_MODES = Object.freeze(["continue", "block"])

const REQUIRED_TOOL_EVENTS = Object.freeze([
  TOOL_LIFECYCLE_EVENTS.pre,
  TOOL_LIFECYCLE_EVENTS.post,
])

export function toolLifecycleInput(event, phase) {
  const raw = event ?? {}
  const typed = {
    event: phase,
    tool_name: raw.tool ?? raw.toolName ?? raw.name ?? "",
    tool_input: raw.input ?? raw.toolInput ?? {},
    tool_use_id: raw.toolUseId ?? raw.tool_use_id ?? raw.id ?? "",
    agent_id: raw.agentId ?? raw.agent_id ?? "",
    agent_type: raw.agentType ?? raw.agent_type ?? "",
  }
  if (phase === TOOL_LIFECYCLE_EVENTS.post) {
    typed.tool_response = raw.toolResponse ?? raw.tool_response ?? raw.response ?? null
    typed.duration_ms = Number(raw.durationMs ?? raw.duration_ms ?? 0) || 0
  }
  return typed
}

export function normalizePermissionDecision(decision = {}) {
  const permissionDecision = decision.permissionDecision ?? decision.decision ?? "allow"
  if (!PERMISSION_DECISIONS.includes(permissionDecision)) {
    throw new Error(`CS-01: unknown permissionDecision ${permissionDecision}`)
  }
  const normalized = { permissionDecision }
  if (decision.reason) normalized.reason = decision.reason
  if (Object.hasOwn(decision, "updatedInput")) normalized.updatedInput = decision.updatedInput
  if (decision.additionalContext) normalized.additionalContext = decision.additionalContext
  return normalized
}

export function decidePreToolUse(event) {
  const typed = toolLifecycleInput(event, TOOL_LIFECYCLE_EVENTS.pre)
  const hit = findShellGuardHit(typed.tool_name, typed.tool_input)
  if (!hit) return { permissionDecision: "allow" }
  const message = `CS-01: ${hit.id} is denied here. Use ${hit.replacement}.`
  // Тот же режим наблюдения, что и в BCX-82: warn блокировку не применяет.
  if (process.env.BEACON_SHELL_GUARD === "warn") {
    return { permissionDecision: "allow", additionalContext: message }
  }
  return { permissionDecision: "deny", reason: message, additionalContext: message }
}

export function decidePostToolUse(event) {
  // Ответ инструмента уже получен, решение deny здесь бесполезно. Типизированный
  // вход остаётся доступным разработчику плагина через toolLifecycleInput.
  toolLifecycleInput(event, TOOL_LIFECYCLE_EVENTS.post)
  return { permissionDecision: "allow" }
}

export function toolHookCapability(ctx) {
  const hook = ctx?.tool?.hook
  if (typeof hook !== "function") return { supported: false, events: [] }
  const advertised = ctx.tool.supportedEvents ?? ctx.tool.hookEvents ?? ctx.tool.events
  if (!Array.isArray(advertised)) {
    return { supported: true, events: [...REQUIRED_TOOL_EVENTS] }
  }
  const missing = REQUIRED_TOOL_EVENTS.filter((name) => !advertised.includes(name))
  return { supported: missing.length === 0, events: advertised }
}

export function assertFailClosedCapability(ctx, onFailure) {
  if (onFailure !== "block") return
  const capability = toolHookCapability(ctx)
  if (!capability.supported) {
    throw new Error(
      "CS-01: host cannot enforce onFailure=block for PreToolUse/PostToolUse",
    )
  }
}

export async function registerToolLifecycle(ctx, options = {}) {
  const onFailure = options.onFailure ?? process.env.BEACON_TOOL_HOOK_ON_FAILURE ?? "continue"
  if (!ON_FAILURE_MODES.includes(onFailure)) {
    throw new Error(`CS-01: unknown onFailure mode ${onFailure}`)
  }
  // Проверка способности идёт до регистрации: при block старый хост не
  // превращает guard в наблюдателя, а получает ошибку запуска.
  assertFailClosedCapability(ctx, onFailure)
  const capability = toolHookCapability(ctx)
  if (!capability.supported) return null

  let pre = null
  let post = null
  try {
    pre = await ctx.tool.hook(TOOL_LIFECYCLE_EVENTS.pre, (event) =>
      normalizePermissionDecision(decidePreToolUse(event)),
    )
    post = await ctx.tool.hook(TOOL_LIFECYCLE_EVENTS.post, (event) =>
      normalizePermissionDecision(decidePostToolUse(event)),
    )
  } catch (error) {
    await post?.dispose?.()
    await pre?.dispose?.()
    if (onFailure === "block") throw error
    return null
  }

  return {
    onFailure,
    capability,
    async dispose() {
      await post?.dispose?.()
      await pre?.dispose?.()
    },
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
    const lifecycle = await registerToolLifecycle(ctx)

    return async () => {
      await lifecycle?.dispose?.()
      await guard?.dispose?.()
      await context?.()
      await mcp?.registration?.dispose?.()
    }
  },
})

export default beaconProject
