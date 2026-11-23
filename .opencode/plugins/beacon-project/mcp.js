import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { execFileSync as defaultExecFileSync } from "node:child_process"
import { delimiter, join } from "node:path"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"
export const BEACON_BUILD_SCRIPT = "scripts/build-beacon.sh"
export const HEAD_POLL_MS = 3000

const defaultDeps = {
  env: process.env,
  existsSync: fsExistsSync,
  readFileSync: fsReadFileSync,
  execFileSync: defaultExecFileSync,
}

// Shared HEAD baseline for the watcher. A new registration resets it so one
// commit triggers exactly one reload.
let lastHead = null
let headWatcher = null

function pathBinary(name, deps) {
  for (const dir of (deps.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue
    const candidate = join(dir, name)
    if (deps.existsSync(candidate)) return candidate
  }
  return null
}

function currentHead(root, deps) {
  try {
    const output = deps.execFileSync("git", ["rev-parse", "HEAD"], {
      cwd: root,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    })
    return String(output).trim() || null
  } catch {
    return null
  }
}

function runBuild(root, deps) {
  const script = join(root, ...BEACON_BUILD_SCRIPT.split("/"))
  try {
    deps.execFileSync("bash", [script], { cwd: root, stdio: "inherit" })
  } catch (error) {
    console.warn(`beacon MCP: failed to rebuild local binary: ${error.message}`)
  }
}

function rebuildLocalIfStale(root, local, deps) {
  const shaPath = `${local}.build-sha`
  let recorded = null
  if (deps.existsSync(shaPath)) {
    try {
      recorded = String(deps.readFileSync(shaPath, "utf8")).trim()
    } catch {
      recorded = null
    }
  }

  if (recorded !== null) {
    const head = currentHead(root, deps)
    if (head && recorded === head) return
  }

  runBuild(root, deps)
}

export function beaconCommand(root, deps = defaultDeps) {
  const explicit = deps.env.BEACON_BIN
  if (explicit && deps.existsSync(explicit)) return [explicit, "mcp"]

  const local = join(root, ".opencode", "bin", "beacon")
  if (deps.existsSync(local)) {
    rebuildLocalIfStale(root, local, deps)
    return [local, "mcp"]
  }

  const repoBin = join(root, "bin", "beacon")
  if (deps.existsSync(repoBin)) return [repoBin, "mcp"]

  const global = pathBinary("beacon", deps)
  if (global) return [global, "mcp"]

  if (deps.existsSync(join(root, "cmd", "beacon", "main.go"))) {
    return ["go", "run", "./cmd/beacon", "mcp"]
  }
  throw new Error("beacon MCP: no beacon binary and no cmd/beacon/main.go in project root")
}

export function beaconServerConfig(root, deps = defaultDeps) {
  if (typeof root !== "string" || root.trim() === "") {
    throw new Error("beacon MCP: project root is required")
  }
  return Object.freeze({
    type: "local",
    command: beaconCommand(root, deps),
    cwd: root,
    environment: Object.freeze({ [BEACON_PROJECT_ROOT_ENV]: root }),
    disabled: false,
  })
}

function stopHeadWatcher() {
  if (headWatcher !== null) {
    headWatcher.clear(headWatcher.handle)
    headWatcher = null
  }
}

function watchHead(ctx, root, deps) {
  if (deps.env.BEACON_BIN) return
  stopHeadWatcher()
  lastHead = currentHead(root, deps)
  const setTimer = globalThis.setInterval
  const clearTimer = globalThis.clearInterval
  const handle = setTimer(() => {
    const head = currentHead(root, deps)
    if (!head || head === lastHead) return
    lastHead = head
    ;(async () => {
      try {
        await ctx.mcp.reload()
      } catch (error) {
        console.warn(`beacon MCP: reload after HEAD change failed: ${error.message}`)
      }
    })()
  }, HEAD_POLL_MS)
  headWatcher = { handle, clear: clearTimer }
  if (typeof handle?.unref === "function") handle.unref()
}

export async function registerBeaconMcp(ctx, root, deps = defaultDeps) {
  let current = root
  const registration = await ctx.mcp.transform((editor) => {
    editor.set(BEACON_SERVER, beaconServerConfig(current, deps))
    editor.update(BEACON_SERVER, (server) => {
      server.cwd = current
      server.environment = {
        ...(server.environment ?? {}),
        [BEACON_PROJECT_ROOT_ENV]: current,
      }
      server.disabled = false
    })
  })

  watchHead(ctx, root, deps)

  return {
    registration,
    async setRoot(next) {
      if (typeof next !== "string" || next === current) return
      current = next
      await ctx.mcp.reload()
    },
  }
}
