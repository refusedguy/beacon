// Tests for the beacon MCP command resolver.
// Run: node --test .opencode/plugins/beacon-project/mcp_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { delimiter, join } from "node:path"
import {
  beaconCommand,
  beaconServerConfig,
  createMcpRegistrar,
  noopMcpRegistration,
  registeredBeaconConfig,
  registerBeaconMcp,
  BEACON_SERVER,
  BEACON_PROJECT_ROOT_ENV,
  BEACON_PARENT_SESSION_ENV,
  BEACON_BUILD_SCRIPT,
} from "./mcp.js"

const ROOT = "/project"
const BINARY_MTIME = 1000

function localPaths(root = ROOT) {
  const local = join(root, ".opencode", "bin", "beacon")
  return { local }
}

function fakeDeps({ files = {}, env = {}, mtimes = {}, dirs = {}, statErrors = [] } = {}) {
  const stored = { ...files }
  const execCalls = []
  return {
    execCalls,
    env,
    existsSync: (path) => Object.hasOwn(stored, path),
    readFileSync: (path) => {
      if (!Object.hasOwn(stored, path)) throw new Error(`ENOENT ${path}`)
      return stored[path]
    },
    statSync: (path) => {
      if (statErrors.includes(path)) throw new Error(`EACCES ${path}`)
      const known = Object.hasOwn(mtimes, path) || Object.hasOwn(stored, path) || Object.hasOwn(dirs, path)
      if (!known) throw new Error(`ENOENT ${path}`)
      return { mtimeMs: mtimes[path] ?? 0, isDirectory: () => Object.hasOwn(dirs, path) }
    },
    readdirSync: (path) => {
      if (!Object.hasOwn(dirs, path)) throw new Error(`ENOENT ${path}`)
      return dirs[path]
    },
    execFileSync: (cmd, args, opts) => {
      execCalls.push({ cmd, args, opts })
      if (cmd === "bash") return ""
      throw new Error(`unexpected exec: ${cmd}`)
    },
  }
}

// A local binary with no tracked source files is fresh by definition.
function localBinaryDeps(overrides = {}) {
  const { local } = localPaths()
  return fakeDeps({
    files: { [local]: "binary" },
    mtimes: { [local]: BINARY_MTIME },
    ...overrides,
  })
}

// Build a tree with a .go source under cmd/beacon at a controlled mtime.
function sourceTreeDeps(sourceMtime = 500) {
  const { local } = localPaths()
  const cmd = join(ROOT, "cmd")
  const cmdBeacon = join(cmd, "beacon")
  const mainGo = join(cmdBeacon, "main.go")
  const files = { [local]: "binary", [mainGo]: "package main" }
  return {
    mainGo,
    deps: fakeDeps({
      files,
      mtimes: { [local]: BINARY_MTIME, [mainGo]: sourceMtime },
      dirs: { [cmd]: ["beacon"], [cmdBeacon]: ["main.go"] },
    }),
  }
}

function fakeCtx() {
  const reloads = []
  let transforms = 0
  return {
    reloads,
    get transforms() { return transforms },
    mcp: {
      async transform(apply) {
        transforms += 1
        apply({ set() {}, update() {} })
        return { dispose() {} }
      },
      async reload() {
        reloads.push(true)
      },
    },
  }
}

function buildCalls(deps) {
  return deps.execCalls.filter((call) => call.cmd === "bash")
}

test("a fresh binary is used without a rebuild", () => {
  const { local } = localPaths()
  const deps = localBinaryDeps()

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 0)
})

test("one resolution does not spawn bash more than once", () => {
  const { local } = localPaths()
  const { deps } = sourceTreeDeps(500)

  for (let i = 0; i < 5; i += 1) {
    assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  }

  assert.equal(buildCalls(deps).length, 0)
})

test("a newer .go source triggers one rebuild with the build script", () => {
  const { local } = localPaths()
  const { deps } = sourceTreeDeps(BINARY_MTIME + 1)

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])

  const builds = buildCalls(deps)
  assert.equal(builds.length, 1)
  assert.deepEqual(builds[0].args, [join(ROOT, ...BEACON_BUILD_SCRIPT.split("/"))])
  assert.equal(builds[0].opts.cwd, ROOT)
})

test("a newer source under pkg triggers a rebuild", () => {
  const { local } = localPaths()
  const pkg = join(ROOT, "pkg")
  const core = join(pkg, "core")
  const source = join(core, "run.go")
  const deps = fakeDeps({
    files: { [local]: "binary", [source]: "package core" },
    mtimes: { [local]: BINARY_MTIME, [source]: BINARY_MTIME + 1 },
    dirs: { [pkg]: ["core"], [core]: ["run.go"] },
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 1)
})

test("a newer module file triggers a rebuild", () => {
  const { local } = localPaths()
  const goMod = join(ROOT, "go.mod")
  const deps = fakeDeps({
    files: { [local]: "binary", [goMod]: "module example" },
    mtimes: { [local]: BINARY_MTIME, [goMod]: BINARY_MTIME + 1 },
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 1)
})

test("sources older than the binary do not trigger a rebuild", () => {
  const { local } = localPaths()
  const deps = sourceTreeDeps(500).deps

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 0)
})

test("an unreadable binary is treated as stale", () => {
  const { local } = localPaths()
  const deps = fakeDeps({ files: { [local]: "binary" }, statErrors: [local] })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 1)
})

test("BEACON_BIN wins over the local binary and never rebuilds", () => {
  const explicit = "/custom/beacon"
  const { local } = localPaths()
  const deps = fakeDeps({
    files: { [local]: "binary", [explicit]: "binary" },
    env: { BEACON_BIN: explicit },
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [explicit, "mcp"])
  assert.equal(deps.execCalls.length, 0)
})

test("a repo binary is used without rebuilding when no local binary exists", () => {
  const repoBin = join(ROOT, "bin", "beacon")
  const deps = fakeDeps({ files: { [repoBin]: "binary" } })

  assert.deepEqual(beaconCommand(ROOT, deps), [repoBin, "mcp"])
  assert.equal(deps.execCalls.length, 0)
})

test("PATH is searched before the go run fallback", () => {
  const dir = "/tools"
  const deps = fakeDeps({
    files: { [join(dir, "beacon")]: "binary" },
    env: { PATH: dir },
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [join(dir, "beacon"), "mcp"])
  assert.equal(deps.execCalls.length, 0)
})

test("go run fallback is used when no binary exists", () => {
  const mainGo = join(ROOT, "cmd", "beacon", "main.go")
  const deps = fakeDeps({ files: { [mainGo]: "package main" } })

  assert.deepEqual(beaconCommand(ROOT, deps), ["go", "run", "./cmd/beacon", "mcp"])
  assert.equal(deps.execCalls.length, 0)
})

test("an empty PATH segment is skipped", () => {
  const dir = "/tools"
  const deps = fakeDeps({
    files: { [join(dir, "beacon")]: "binary" },
    env: { PATH: `${delimiter}${dir}` },
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [join(dir, "beacon"), "mcp"])
})

test("repeated registration records one server config per project root", async (t) => {
  const ctx = fakeCtx()
  const deps = localBinaryDeps()
  const first = await registerBeaconMcp(ctx, ROOT, deps)
  t.after(() => first.dispose())

  const second = await registerBeaconMcp(ctx, ROOT, deps)

  assert.equal(first, second)
  assert.equal(first.server, BEACON_SERVER)
  assert.deepEqual(first.config, beaconServerConfig(ROOT, localBinaryDeps()))
  assert.equal(registeredBeaconConfig(ROOT), first.config)
})

test("registrations are keyed by project root", async (t) => {
  const ctx = fakeCtx()
  const first = await registerBeaconMcp(ctx, ROOT, localBinaryDeps())

  const other = "/other"
  const otherLocal = join(other, ".opencode", "bin", "beacon")
  const otherDeps = fakeDeps({
    files: { [otherLocal]: "binary" },
    mtimes: { [otherLocal]: BINARY_MTIME },
  })
  const second = await registerBeaconMcp(ctx, other, otherDeps)
  t.after(async () => {
    await first.dispose()
    await second.dispose()
  })

  assert.notEqual(first, second)
  assert.equal(registeredBeaconConfig(ROOT), first.config)
  assert.equal(registeredBeaconConfig(other), second.config)
})

test("four location resolutions spawn no build subprocess", async (t) => {
  const registrations = []
  const allDeps = []
  for (let i = 0; i < 4; i += 1) {
    const root = `/project-${i}`
    const local = join(root, ".opencode", "bin", "beacon")
    const deps = fakeDeps({ files: { [local]: "binary" }, mtimes: { [local]: BINARY_MTIME } })
    allDeps.push(deps)
    registrations.push(await registerBeaconMcp(fakeCtx(), root, deps))
  }
  t.after(async () => {
    for (const registration of registrations) await registration.dispose()
  })

  for (const deps of allDeps) {
    assert.equal(deps.execCalls.length, 0)
  }
})

test("registration schedules no timer, reload or daemon command", async (t) => {
  const setTimer = t.mock.method(globalThis, "setTimeout", () => ({ unref() {} }))
  const setInterval = t.mock.method(globalThis, "setInterval", () => ({ unref() {} }))
  const ctx = fakeCtx()
  const deps = localBinaryDeps()

  const registration = await registerBeaconMcp(ctx, ROOT, deps)
  t.after(() => registration.dispose())

  assert.equal(setTimer.mock.callCount(), 0)
  assert.equal(setInterval.mock.callCount(), 0)
  assert.equal(ctx.transforms, 0)
  assert.deepEqual(ctx.reloads, [])
  assert.deepEqual(buildCalls(deps), [])
  assert.equal(deps.execCalls.some((call) => call.args.includes("mcp")), false)
})

test("server config carries the project root and enables the server", () => {
  const { local } = localPaths()
  const deps = localBinaryDeps()
  const config = beaconServerConfig(ROOT, deps)

  assert.equal(config.type, "local")
  assert.deepEqual(config.command, [local, "mcp"])
  assert.equal(config.cwd, ROOT)
  assert.equal(config.environment[BEACON_PROJECT_ROOT_ENV], ROOT)
  assert.equal(BEACON_PARENT_SESSION_ENV in config.environment, false)
  assert.equal(config.disabled, false)
  assert.throws(() => beaconServerConfig("", deps), /project root is required/)
})

test("server config carries the operator session for the daemon fallback", () => {
  const config = beaconServerConfig(ROOT, localBinaryDeps(), "ses_operator")

  assert.equal(config.environment[BEACON_PROJECT_ROOT_ENV], ROOT)
  assert.equal(config.environment[BEACON_PARENT_SESSION_ENV], "ses_operator")
})

test("registration reads the session id from the plugin context", async (t) => {
  const ctx = { ...fakeCtx(), sessionID: "ses_operator" }
  const registration = await registerBeaconMcp(ctx, ROOT, localBinaryDeps())
  t.after(() => registration.dispose())

  assert.equal(registration.config.environment[BEACON_PARENT_SESSION_ENV], "ses_operator")
})

test("500 config requests over a 2000-file source tree keep using the local binary", () => {
  const { local } = localPaths()
  const cmd = join(ROOT, "cmd")
  const files = { [local]: "binary" }
  const mtimes = { [local]: BINARY_MTIME }
  const names = []
  for (let i = 0; i < 2000; i += 1) {
    const name = `source${i}.go`
    files[join(cmd, name)] = "package main"
    mtimes[join(cmd, name)] = BINARY_MTIME - 1
    names.push(name)
  }
  const deps = fakeDeps({ files, mtimes, dirs: { [cmd]: names } })

  for (let i = 0; i < 500; i += 1) {
    assert.deepEqual(beaconServerConfig(ROOT, deps).command, [local, "mcp"])
  }

  assert.equal(deps.execCalls.length, 0)
})

test("a deps override for beaconCommand is used instead of probing the filesystem", () => {
  const calls = []
  const override = (root) => { calls.push(root); return ["/bin/beacon", "mcp"] }
  const config = beaconServerConfig(ROOT, { beaconCommand: override })

  assert.deepEqual(calls, [ROOT])
  assert.deepEqual(config.command, ["/bin/beacon", "mcp"])
})

test("a no-op registration disposes once and registers no config", async () => {
  const registration = noopMcpRegistration()

  assert.equal(registration.server, BEACON_SERVER)
  assert.equal(registration.config, null)
  assert.equal(registration.disposed, false)
  await registration.dispose()
  await registration.dispose()
  assert.equal(registration.disposed, true)
  assert.equal(registeredBeaconConfig(ROOT), null)
})

test("two registrars keep registration state separate", async (t) => {
  const registrarA = createMcpRegistrar({ beaconCommand: () => ["/bin/a", "mcp"] })
  const registrarB = createMcpRegistrar({ beaconCommand: () => ["/bin/b", "mcp"] })
  const a = await registrarA.registerBeaconMcp(fakeCtx(), ROOT)
  const b = await registrarB.registerBeaconMcp(fakeCtx(), ROOT)
  t.after(async () => {
    await a.dispose()
    await b.dispose()
  })

  assert.deepEqual(a.config.command, ["/bin/a", "mcp"])
  assert.deepEqual(b.config.command, ["/bin/b", "mcp"])
  assert.equal(registrarA.registeredBeaconConfig(ROOT), a.config)
  assert.equal(registrarB.registeredBeaconConfig(ROOT), b.config)

  await a.dispose()
  assert.equal(registrarA.registeredBeaconConfig(ROOT), null)
  assert.equal(registrarB.registeredBeaconConfig(ROOT), b.config)
})

test("two registrars do not share rebuild decisions", () => {
  const { deps } = sourceTreeDeps(BINARY_MTIME + 1)
  const registrarA = createMcpRegistrar()
  const registrarB = createMcpRegistrar()

  registrarA.beaconCommand(ROOT, deps)
  registrarA.beaconCommand(ROOT, deps)
  registrarB.beaconCommand(ROOT, deps)
  registrarB.beaconCommand(ROOT, deps)

  assert.equal(buildCalls(deps).length, 2)
})
