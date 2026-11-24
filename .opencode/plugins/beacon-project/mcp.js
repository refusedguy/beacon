import { existsSync as fsExistsSync, statSync as fsStatSync, readdirSync as fsReaddirSync } from "node:fs"
import { execFileSync as defaultExecFileSync } from "node:child_process"
import { delimiter, join } from "node:path"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"
export const BEACON_BUILD_SCRIPT = "scripts/build-beacon.sh"
export const BUILD_HASH_FLAG = "--hash"

const SOURCE_DIRS = ["cmd", "internal", "pkg"]
const MODULE_FILES = ["go.mod", "go.sum", "go.work"]

const defaultDeps = {
  env: process.env,
  existsSync: fsExistsSync,
  statSync: fsStatSync,
  readdirSync: fsReaddirSync,
  execFileSync: defaultExecFileSync,
}

// Partial dependency objects are useful at call sites that only override one
// probe. Merge them with the real defaults before any command resolution.
function normalizeDeps(deps) {
  return { ...defaultDeps, ...(deps && typeof deps === "object" ? deps : {}) }
}

// One registration per canonical project root. The operator attaches the MCP
// server manually. Registration here only records the config for that attach.
// It never starts, reloads or restarts a daemon.
const registrations = new Map()

function pathBinary(name, deps) {
  for (const dir of (deps.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue
    const candidate = join(dir, name)
    if (deps.existsSync(candidate)) return candidate
  }
  return null
}

// Yield every build input tracked by scripts/build-beacon.sh. Directories are
// walked with readdirSync and statSync so the common path never spawns bash.
function* sourcePaths(root, deps) {
  for (const name of MODULE_FILES) {
    const path = join(root, name)
    if (deps.existsSync(path)) yield path
  }
  const stack = SOURCE_DIRS.map((dir) => join(root, dir))
  while (stack.length) {
    const dir = stack.pop()
    let entries
    try {
      entries = deps.readdirSync(dir)
    } catch {
      continue
    }
    for (const name of entries) {
      const path = join(dir, name)
      let stat
      try {
        stat = deps.statSync(path)
      } catch {
        continue
      }
      if (stat.isDirectory()) stack.push(path)
      else if (name.endsWith(".go")) yield path
    }
  }
}

// A binary is fresh when every build input is older than the binary itself.
// Any stat failure means we could not prove freshness, so we rebuild.
function sourceNewerThanBinary(root, deps, binaryMtime) {
  for (const source of sourcePaths(root, deps)) {
    try {
      if (deps.statSync(source).mtimeMs > binaryMtime) return true
    } catch {
      return true
    }
  }
  return false
}

function runBuild(root, deps) {
  const script = join(root, ...BEACON_BUILD_SCRIPT.split("/"))
  try {
    deps.execFileSync("bash", [script], { cwd: root, stdio: "inherit" })
  } catch (error) {
    console.warn(`beacon MCP: failed to rebuild local binary: ${error.message}`)
  }
}

// One rebuild attempt per root and binary mtime. A successful build changes
// the binary mtime, so a later resolution checks the new build. Until then,
// repeated resolutions reuse the decision instead of racing with the script.
const rebuilds = new WeakMap()

function rebuildLocalIfStale(root, local, deps, cacheDeps) {
  let mtime
  try { mtime = deps.statSync(local).mtimeMs } catch { mtime = null }
  let checked = rebuilds.get(cacheDeps)
  if (!checked) { checked = new Set(); rebuilds.set(cacheDeps, checked) }
  const key = `${root}\u0000${mtime}`
  if (checked.has(key)) return
  checked.add(key)
  if (mtime === null || sourceNewerThanBinary(root, deps, mtime)) runBuild(root, deps)
}

export function beaconCommand(root, deps = defaultDeps) {
  const cacheDeps = deps && typeof deps === "object" ? deps : defaultDeps
  deps = normalizeDeps(deps)
  const explicit = deps.env.BEACON_BIN
  if (explicit && deps.existsSync(explicit)) return [explicit, "mcp"]

  const local = join(root, ".opencode", "bin", "beacon")
  if (deps.existsSync(local)) {
    rebuildLocalIfStale(root, local, deps, cacheDeps)
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
  deps = normalizeDeps(deps)
  const resolveCommand = typeof deps.beaconCommand === "function" ? deps.beaconCommand : beaconCommand
  return Object.freeze({
    type: "local",
    command: resolveCommand(root, deps),
    cwd: root,
    environment: Object.freeze({ [BEACON_PROJECT_ROOT_ENV]: root }),
    disabled: false,
  })
}

export function registeredBeaconConfig(root) {
  return registrations.get(root)?.config ?? null
}

// A worker session inherits the pooled daemon from its parent. This stands in
// for a real registration so the plugin's cleanup path stays uniform without
// resolving a command or touching the registration cache.
export function noopMcpRegistration() {
  let disposed = false
  return Object.freeze({
    server: BEACON_SERVER,
    config: null,
    get disposed() { return disposed },
    async dispose() { disposed = true },
  })
}

// ctx keeps the plugin setup signature. The function does not call ctx.mcp.
export async function registerBeaconMcp(ctx, root, deps = defaultDeps) {
  if (typeof root !== "string" || root.trim() === "") {
    throw new Error("beacon MCP: project root is required")
  }
  deps = normalizeDeps(deps)
  const existing = registrations.get(root)
  if (existing) return existing

  const config = beaconServerConfig(root, deps)
  const registration = Object.freeze({
    server: BEACON_SERVER,
    config,
    async dispose() {
      if (registrations.get(root) === registration) registrations.delete(root)
    },
  })
  registrations.set(root, registration)
  return registration
}
