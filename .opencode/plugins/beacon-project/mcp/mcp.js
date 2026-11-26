import { existsSync as fsExistsSync } from "node:fs"
import { delimiter, join } from "node:path"
import { sessionIdentity } from "../state/session_identity.js"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"
export const BEACON_PARENT_SESSION_ENV = "BEACON_PARENT_SESSION"
// Kept as the single path used by the operator reminder in rules/context.js.
// Builds happen at development time, never during MCP command resolution.
export const BEACON_BUILD_SCRIPT = "scripts/build-beacon.sh"

const defaultDeps = {
  env: process.env,
  existsSync: fsExistsSync,
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

// Every returned registrar owns its registration map. A caller that passes
// different deps to two registrars gets two independent caches.
export function createMcpRegistrar(deps = defaultDeps) {
  const baseDeps = normalizeDeps(deps)
  const registrations = new Map()

  // Call-level deps override the deps this registrar was created with.
  function resolveDeps(callDeps) {
    return callDeps && typeof callDeps === "object" ? { ...baseDeps, ...callDeps } : baseDeps
  }

  // Resolve the binary location only. A stale local binary is reported by the
  // operator reminder and rebuilt with the explicit script at development time.
  // Nothing here reads source files or starts a subprocess, so a dispatch call
  // never waits on a build.
  function beaconCommand(root, callDeps = baseDeps) {
    const effective = resolveDeps(callDeps)
    const explicit = effective.env.BEACON_BIN
    if (explicit && effective.existsSync(explicit)) return [explicit, "mcp"]

    const local = join(root, ".opencode", "bin", "beacon")
    if (effective.existsSync(local)) return [local, "mcp"]

    const repoBin = join(root, "bin", "beacon")
    if (effective.existsSync(repoBin)) return [repoBin, "mcp"]

    const global = pathBinary("beacon", effective)
    if (global) return [global, "mcp"]

    if (effective.existsSync(join(root, "cmd", "beacon", "main.go"))) {
      return ["go", "run", "./cmd/beacon", "mcp"]
    }
    throw new Error("beacon MCP: no beacon binary and no cmd/beacon/main.go in project root")
  }

  function beaconServerConfig(root, callDeps = baseDeps, sessionID = "") {
    if (typeof root !== "string" || root.trim() === "") {
      throw new Error("beacon MCP: project root is required")
    }
    const effective = resolveDeps(callDeps)
    const resolveCommand = typeof effective.beaconCommand === "function" ? effective.beaconCommand : beaconCommand
    const environment = { [BEACON_PROJECT_ROOT_ENV]: root }
    // The daemon reads this once at startup. A worker that inherits the pooled
    // connection sends no MCP meta, so the operator session must be in the
    // server environment to give that dispatch a parent binding.
    if (typeof sessionID === "string" && sessionID) {
      environment[BEACON_PARENT_SESSION_ENV] = sessionID
    }
    return Object.freeze({
      type: "local",
      command: resolveCommand(root, effective),
      cwd: root,
      environment: Object.freeze(environment),
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

    // Plugin setup does not always receive a session ID directly. The shared
    // resolver falls back to the newest session, which is the operator session
    // that owns the pooled MCP daemon.
    const identity = await sessionIdentity(ctx)
    const config = beaconServerConfig(root, effective, identity.sessionID)
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
