import { existsSync as fsExistsSync, statSync as fsStatSync, readdirSync as fsReaddirSync } from "node:fs"
import { execFileSync as defaultExecFileSync } from "node:child_process"
import { delimiter, join } from "node:path"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"
export const BEACON_BUILD_SCRIPT = "scripts/build-beacon.sh"
export const BUILD_HASH_FLAG = "--hash"

const SOURCE_DIRS = ["cmd", "internal", "pkg", "harnesses", "ui/tui"]
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

// A worker session inherits the pooled daemon from its parent. This stands in
// for a real registration so the plugin's cleanup path stays uniform without
// resolving a command or touching a registrar's cache.
export function noopMcpRegistration() {
  let disposed = false
  return Object.freeze({
    server: BEACON_SERVER,
    config: null,
    get disposed() { return disposed },
    async dispose() { disposed = true },
  })
}

// Every returned registrar owns its registration and rebuild maps. A caller
// that passes different deps to two registrars gets two independent caches.
export function createMcpRegistrar(deps = defaultDeps) {
  const baseDeps = normalizeDeps(deps)
  const registrations = new Map()
  const rebuilds = new WeakMap()

  // Call-level deps override the deps this registrar was created with.
  function resolveDeps(callDeps) {
    return { ...baseDeps, ...(callDeps && typeof callDeps === "object" ? callDeps : {}) }
  }

  // One rebuild attempt per root and binary mtime. A successful build changes
  // the binary mtime, so a later resolution checks the new build. Until then,
  // repeated resolutions reuse the decision instead of racing with the script.
  function rebuildLocalIfStale(root, local, effective, cacheDeps) {
    let mtime
    try { mtime = effective.statSync(local).mtimeMs } catch { mtime = null }
    let checked = rebuilds.get(cacheDeps)
    if (!checked) { checked = new Set(); rebuilds.set(cacheDeps, checked) }
    const key = `${root}\u0000${mtime}`
    if (checked.has(key)) return
    checked.add(key)
    if (mtime === null || sourceNewerThanBinary(root, effective, mtime)) runBuild(root, effective)
  }

  function beaconCommand(root, callDeps = baseDeps) {
    const cacheDeps = callDeps && typeof callDeps === "object" ? callDeps : baseDeps
    const effective = resolveDeps(callDeps)
    const explicit = effective.env.BEACON_BIN
    if (explicit && effective.existsSync(explicit)) return [explicit, "mcp"]

    const local = join(root, ".opencode", "bin", "beacon")
    if (effective.existsSync(local)) {
      rebuildLocalIfStale(root, local, effective, cacheDeps)
      return [local, "mcp"]
    }

    const repoBin = join(root, "bin", "beacon")
    if (effective.existsSync(repoBin)) return [repoBin, "mcp"]

    const global = pathBinary("beacon", effective)
    if (global) return [global, "mcp"]

    if (effective.existsSync(join(root, "cmd", "beacon", "main.go"))) {
      return ["go", "run", "./cmd/beacon", "mcp"]
    }
    throw new Error("beacon MCP: no beacon binary and no cmd/beacon/main.go in project root")
  }

  function beaconServerConfig(root, callDeps = baseDeps) {
    if (typeof root !== "string" || root.trim() === "") {
      throw new Error("beacon MCP: project root is required")
    }
    const effective = resolveDeps(callDeps)
    const resolveCommand = typeof effective.beaconCommand === "function" ? effective.beaconCommand : beaconCommand
    return Object.freeze({
      type: "local",
      command: resolveCommand(root, effective),
      cwd: root,
      environment: Object.freeze({ [BEACON_PROJECT_ROOT_ENV]: root }),
      disabled: false,
    })
  }

  function registeredBeaconConfig(root) {
    return registrations.get(root)?.config ?? null
  }

  // One registration per canonical project root. The operator attaches the MCP
  // server manually. Registration here only records the config for that attach.
  // It never starts, reloads or restarts a daemon.
  async function registerBeaconMcp(ctx, root, callDeps = baseDeps) {
    if (typeof root !== "string" || root.trim() === "") {
      throw new Error("beacon MCP: project root is required")
    }
    const effective = resolveDeps(callDeps)
    const existing = registrations.get(root)
    if (existing) return existing

    const config = beaconServerConfig(root, effective)
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

  return Object.freeze({
    beaconCommand,
    beaconServerConfig,
    registeredBeaconConfig,
    registerBeaconMcp,
    noopMcpRegistration,
  })
}

// Thin wrappers keep the module-level API for callers that do not create a
// registrar. They all delegate to one shared default registrar.
const defaultRegistrar = createMcpRegistrar(defaultDeps)

export const beaconCommand = (...args) => defaultRegistrar.beaconCommand(...args)
export const beaconServerConfig = (...args) => defaultRegistrar.beaconServerConfig(...args)
export const registeredBeaconConfig = (...args) => defaultRegistrar.registeredBeaconConfig(...args)
export const registerBeaconMcp = (...args) => defaultRegistrar.registerBeaconMcp(...args)
