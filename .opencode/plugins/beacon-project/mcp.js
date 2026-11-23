import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { execFileSync as defaultExecFileSync } from "node:child_process"
import { delimiter, join } from "node:path"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"
export const BEACON_BUILD_SCRIPT = "scripts/build-beacon.sh"
export const BUILD_HASH_FLAG = "--hash"
export const BUILD_HASH_POLL_MS = 3000
export const RELOAD_DEBOUNCE_MS = 10000

const defaultDeps = {
  env: process.env,
  existsSync: fsExistsSync,
  readFileSync: fsReadFileSync,
  execFileSync: defaultExecFileSync,
}

// Shared watchdog state. A new registration resets it so one source change
// causes exactly one rebuild and one reload.
let lastBuildHash = null
let pollWatcher = null
let reloadTimer = null

function pathBinary(name, deps) {
  for (const dir of (deps.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue
    const candidate = join(dir, name)
    if (deps.existsSync(candidate)) return candidate
  }
  return null
}

function currentBuildHash(root, deps) {
  const script = join(root, ...BEACON_BUILD_SCRIPT.split("/"))
  try {
    const output = deps.execFileSync("bash", [script, BUILD_HASH_FLAG], {
      cwd: root,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    })
    return String(output).trim() || null
  } catch {
    return null
  }
}

function recordedBuildHash(local, deps) {
  const shaPath = `${local}.build-sha`
  if (!deps.existsSync(shaPath)) return null
  try {
    return String(deps.readFileSync(shaPath, "utf8")).trim() || null
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
  const recorded = recordedBuildHash(local, deps)
  if (recorded === null) {
    runBuild(root, deps)
    return
  }
  const current = currentBuildHash(root, deps)
  if (current && recorded === current) return
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

function stopWatchdog() {
  if (pollWatcher !== null) {
    pollWatcher.clear(pollWatcher.handle)
    pollWatcher = null
  }
  if (reloadTimer !== null) {
    reloadTimer.clear(reloadTimer.handle)
    reloadTimer = null
  }
  lastBuildHash = null
}

function scheduleReload(ctx) {
  if (reloadTimer !== null) reloadTimer.clear(reloadTimer.handle)
  const setTimer = globalThis.setTimeout
  const clearTimer = globalThis.clearTimeout
  const handle = setTimer(() => {
    reloadTimer = null
    void (async () => {
      try {
        await ctx.mcp.reload()
      } catch (error) {
        console.warn(`beacon MCP: reload after source change failed: ${error.message}`)
      }
    })()
  }, RELOAD_DEBOUNCE_MS)
  reloadTimer = { handle, clear: clearTimer }
  if (typeof handle?.unref === "function") handle.unref()
}

function watchBuildHash(ctx, root, deps) {
  if (deps.env.BEACON_BIN) return
  stopWatchdog()
  lastBuildHash = currentBuildHash(root, deps)
  const setTimer = globalThis.setInterval
  const clearTimer = globalThis.clearInterval
  const handle = setTimer(() => {
    const hash = currentBuildHash(root, deps)
    if (!hash || hash === lastBuildHash) return
    lastBuildHash = hash
    scheduleReload(ctx)
  }, BUILD_HASH_POLL_MS)
  pollWatcher = { handle, clear: clearTimer }
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

  watchBuildHash(ctx, root, deps)

  return {
    registration,
    async setRoot(next) {
      if (typeof next !== "string" || next === current) return
      current = next
      await ctx.mcp.reload()
    },
  }
}
