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
} from "./mcp.js"

const ROOT = "/project"
const BINARY_MTIME = 1000

function localPaths(root = ROOT) {
  const local = join(root, ".opencode", "bin", "beacon")
  return { local }
}

function fakeDeps({ files = {}, env = {}, mtimes = {}, dirs = {} } = {}) {
  const stored = { ...files }
  const execCalls = []
  const statCalls = []
  return {
    execCalls,
    statCalls,
    env,
    existsSync: (path) => Object.hasOwn(stored, path),
    readFileSync: (path) => {
      if (!Object.hasOwn(stored, path)) throw new Error(`ENOENT ${path}`)
      return stored[path]
    },
    statSync: (path) => {
      statCalls.push(path)
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

// Count filesystem probes so a test can prove the resolver ran once.
function probeCountingDeps(deps) {
  const probes = []
  return {
    ...deps,
    probes,
    existsSync: (path) => {
      probes.push(path)
      return deps.existsSync(path)
    },
  }
}

test("a local binary resolves without probing sources or spawning a build", () => {
  const { local } = localPaths()
  const deps = localBinaryDeps()

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(deps.execCalls.length, 0)
  assert.equal(deps.statCalls.length, 0)
})

test("a newer .go source never delays a repeated dispatch call", () => {
  const { local } = localPaths()
  const { deps } = sourceTreeDeps(BINARY_MTIME + 1)

  for (let i = 0; i < 5; i += 1) {
    assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  }

  assert.equal(deps.execCalls.length, 0)
  assert.equal(deps.statCalls.length, 0)
})

test("a newer source under pkg or go.mod never triggers a build", () => {
  const { local } = localPaths()
  const source = join(ROOT, "pkg", "core", "run.go")
  const goMod = join(ROOT, "go.mod")
  const deps = fakeDeps({
    files: { [local]: "binary", [source]: "package core", [goMod]: "module example" },
    mtimes: { [local]: BINARY_MTIME, [source]: BINARY_MTIME + 1, [goMod]: BINARY_MTIME + 1 },
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(deps.execCalls.length, 0)
  assert.equal(deps.statCalls.length, 0)
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

test("500 config requests over a 2000-file source tree never walk or build", () => {
  const { local } = localPaths()
  const cmd = join(ROOT, "cmd")
  const files = { [local]: "binary" }
  const mtimes = { [local]: BINARY_MTIME }
  const names = []
  for (let i = 0; i < 2000; i += 1) {
    const name = `source${i}.go`
    files[join(cmd, name)] = "package main"
    mtimes[join(cmd, name)] = BINARY_MTIME + 1
    names.push(name)
  }
  const deps = fakeDeps({ files, mtimes, dirs: { [cmd]: names } })

  for (let i = 0; i < 500; i += 1) {
    assert.deepEqual(beaconServerConfig(ROOT, deps).command, [local, "mcp"])
  }

  assert.equal(deps.execCalls.length, 0)
  assert.equal(deps.statCalls.length, 0)
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

test("two registrars resolve the local binary without a build", () => {
  const { local } = localPaths()
  const { deps } = sourceTreeDeps(BINARY_MTIME + 1)
  const registrarA = createMcpRegistrar()
  const registrarB = createMcpRegistrar()

  assert.deepEqual(registrarA.beaconCommand(ROOT, deps), [local, "mcp"])
  assert.deepEqual(registrarB.beaconCommand(ROOT, deps), [local, "mcp"])

  assert.equal(deps.execCalls.length, 0)
  assert.equal(deps.statCalls.length, 0)
})

test("ten sequential CLI calls perform one path resolution", () => {
  const { local } = localPaths()
  const deps = probeCountingDeps(localBinaryDeps())

  for (let i = 0; i < 10; i += 1) {
    assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  }

  assert.deepEqual(deps.probes, [local])
})

test("sequential server config requests reuse one path resolution", () => {
  const { local } = localPaths()
  const deps = probeCountingDeps(localBinaryDeps())

  for (let i = 0; i < 10; i += 1) {
    assert.deepEqual(beaconServerConfig(ROOT, deps).command, [local, "mcp"])
  }

  assert.deepEqual(deps.probes, [local])
})

test("a changed BEACON_BIN invalidates the memo", () => {
  const { local } = localPaths()
  const explicit = "/custom/beacon"
  const deps = fakeDeps({
    files: { [local]: "binary", [explicit]: "binary" },
    env: {},
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])

  deps.env.BEACON_BIN = explicit

  assert.deepEqual(beaconCommand(ROOT, deps), [explicit, "mcp"])
})

test("a new local binary invalidates the memo", () => {
  const { local } = localPaths()
  const mainGo = join(ROOT, "cmd", "beacon", "main.go")
  const before = fakeDeps({ files: { [mainGo]: "package main" } })
  const after = fakeDeps({ files: { [local]: "binary", [mainGo]: "package main" } })

  assert.deepEqual(beaconCommand(ROOT, before), ["go", "run", "./cmd/beacon", "mcp"])
  assert.deepEqual(beaconCommand(ROOT, after), [local, "mcp"])
})
