// T3.176. Точка входа локального плагина Beacon (ADR 0069, решение 1).
// Плагин лежит в .opencode/plugins и загружается автоматически, без записи
// в глобальный opencode.json. Серверная часть объявляется через Plugin.define
// и экспортируется по умолчанию.

import { Plugin } from "@opencode/plugin"
import { locationInfo, projectRoot } from "./location.js"
import { registerBeaconMcp } from "./mcp.js"
import { installProjectContext } from "./context.js"

export const beaconProject = Plugin.define({
  id: "beacon-project",
  async setup(ctx) {
    const root = projectRoot(ctx)
    // Старый хост без ctx.location оставляет поведение fail-open: плагин
    // не трогает MCP и не пишет в сессии чужого проекта.
    if (!root) return

    const mcp = await registerBeaconMcp(ctx, root)
    const context = await installProjectContext(ctx, root, locationInfo(ctx))

    return async () => {
      await context?.()
      await mcp?.registration?.dispose?.()
    }
  },
})

export default beaconProject
