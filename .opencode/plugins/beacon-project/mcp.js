import { existsSync as fsExistsSync, readFileSync as fsReadFileSync } from "node:fs"
import { execFileSync as defaultExecFileSync } from "node:child_process"
import { delimiter, join } from "node:path"

export const BEACON_SERVER = "beacon"
export const BEACON_PROJECT_ROOT_ENV = "BEACON_PROJECT_ROOT"
export const BEACON_BUILD_SCRIPT = "scripts/build-beacon.sh"
export const BUILD_HASH_FLAG = "--hash"

const defaultDeps = {
  env: process.env,
  existsSync: fsExistsSync,
  readFileSync: fsReadFileSync,
  execFileSync: defaultExecFileSync,
}

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

export async function registerBeaconMcp(ctx, root, deps = defaultDeps) {
  const registration = await ctx.mcp.transform((editor) => {
    editor.set(BEACON_SERVER, beaconServerConfig(root, deps))
    editor.update(BEACON_SERVER, (server) => {
      server.cwd = root
      server.environment = {
        ...(server.environment ?? {}),
        [BEACON_PROJECT_ROOT_ENV]: root,
      }
      server.disabled = false
    })
  })
  return { registration }
}
