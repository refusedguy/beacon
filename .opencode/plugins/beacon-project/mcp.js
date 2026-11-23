import { existsSync } from "node:fs"
import { delimiter, join } from "node:path"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"

function pathBinary(name) {
  for (const dir of (process.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue
    const candidate = join(dir, name)
    if (existsSync(candidate)) return candidate
  }
  return null
}

export function beaconCommand(root) {
  const explicit = process.env.BEACON_BIN
  if (explicit && existsSync(explicit)) return [explicit, "mcp"]

  const local = join(root, ".opencode", "bin", "beacon")
  if (existsSync(local)) return [local, "mcp"]

  const repoBin = join(root, "bin", "beacon")
  if (existsSync(repoBin)) return [repoBin, "mcp"]

  const global = pathBinary("beacon")
  if (global) return [global, "mcp"]

  if (existsSync(join(root, "cmd", "beacon", "main.go"))) {
    return ["go", "run", "./cmd/beacon", "mcp"]
  }
  throw new Error("beacon MCP: no beacon binary and no cmd/beacon/main.go in project root")
}

export function beaconServerConfig(root) {
  if (typeof root !== "string" || root.trim() === "") {
    throw new Error("beacon MCP: project root is required")
  }
  return Object.freeze({
    type: "local",
    command: beaconCommand(root),
    cwd: root,
    environment: Object.freeze({ [BEACON_PROJECT_ROOT_ENV]: root }),
    disabled: false,
  })
}

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
