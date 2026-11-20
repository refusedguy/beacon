// T3.177. Регистрация MCP-сервера Beacon через ctx.mcp.transform (ADR 0069).
// Корень проекта приходит из location.js и равен ctx.location.project.canonical.
// В конфиге сервера нет жёстко заданного cwd: каталог процесса и явная
// переменная окружения вычисляются из корня на каждом экземпляре плагина.

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"

export function beaconServerConfig(root) {
  if (typeof root !== "string" || root.trim() === "") {
    throw new Error("beacon MCP: нужен корень проекта")
  }
  return Object.freeze({
    type: "local",
    command: [".opencode/bin/beacon", "mcp"],
    cwd: root,
    environment: Object.freeze({ [BEACON_PROJECT_ROOT_ENV]: root }),
    disabled: false,
  })
}

// Возвращает регистрацию и setRoot. Трансформы не наблюдают внешние входы
// сами: после смены корня вызывается reload, который заново применяет
// регистрацию и выравнивает жизненный цикл сервера.
export async function registerBeaconMcp(ctx, root) {
  let current = root
  const registration = await ctx.mcp.transform((editor) => {
    editor.set(BEACON_SERVER, beaconServerConfig(current))
    editor.update(BEACON_SERVER, (server) => {
      server.cwd = current
      server.environment = {
        ...(server.environment ?? {}),
        [BEACON_PROJECT_ROOT_ENV]: current,
      }
      server.disabled = false
    })
  })

  return {
    registration,
    async setRoot(next) {
      if (typeof next !== "string" || next === current) return
      current = next
      await ctx.mcp.reload()
    },
  }
}
