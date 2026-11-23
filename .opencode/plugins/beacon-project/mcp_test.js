// Tests for the beacon MCP command resolver.
// Run: node --test .opencode/plugins/beacon-project/mcp_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { delimiter, join } from "node:path"
import {
  beaconCommand,
  beaconServerConfig,
  registeredBeaconConfig,
  registerBeaconMcp,
  BEACON_SERVER,
  BEACON_PROJECT_ROOT_ENV,
  BEACON_BUILD_SCRIPT,
  BUILD_HASH_FLAG,
} from "./mcp.js"

const ROOT = "/project"

function localPaths(root = ROOT) {
  const local = join(root, ".opencode", "bin", "beacon")
  return { local, sha: `${local}.build-sha` }
}

function fakeDeps({ files = {}, hash = "h0", head = "commit0", env = {} } = {}) {
  const { sha } = localPaths()
  const stored = { ...files }
  const execCalls = []
  const hashValue = () => (typeof hash === "function" ? hash() : hash)
  const headValue = () => (typeof head === "function" ? head() : head)
  return {
    execCalls,
    env,
    existsSync: (path) => Object.hasOwn(stored, path),
    readFileSync: (path) => {
      if (!Object.hasOwn(stored, path)) throw new Error(`ENOENT ${path}`)
      return stored[path]
    },
    execFileSync: (cmd, args, opts) => {
      execCalls.push({ cmd, args, opts })
      if (cmd === "git") return `${headValue()}\n`
      if (cmd === "bash") {
        if (args.includes(BUILD_HASH_FLAG)) return `${hashValue()}\n`
        stored[sha] = `${hashValue()}\n`
        return ""
      }
      throw new Error(`unexpected exec: ${cmd}`)
    },
  }
}

function localBinaryDeps(overrides = {}) {
  const { local, sha } = localPaths()
  return fakeDeps({
    files: { [local]: "binary", [sha]: "h0\n" },
    hash: "h0",
    ...overrides,
  })
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
  return deps.execCalls.filter((call) => call.cmd === "bash" && !call.args.includes(BUILD_HASH_FLAG))
}

test("local binary with a matching build hash is used without a rebuild", () => {
  const { local } = localPaths()
  const deps = localBinaryDeps()

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 0)
})

test("stale build hash triggers one rebuild and the local binary is still returned", () => {
  const { local, sha } = localPaths()
  const deps = fakeDeps({ files: { [local]: "binary", [sha]: "old\n" }, hash: "new" })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])

  const builds = buildCalls(deps)
  assert.equal(builds.length, 1)
  assert.deepEqual(builds[0].args, [join(ROOT, ...BEACON_BUILD_SCRIPT.split("/"))])
  assert.equal(builds[0].opts.cwd, ROOT)
})

test("missing build hash triggers one rebuild", () => {
  const { local } = localPaths()
  const deps = fakeDeps({ files: { [local]: "binary" }, hash: "h0" })

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
    files: { [otherLocal]: "binary", [`${otherLocal}.build-sha`]: "h0\n" },
    hash: "h0",
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
  assert.equal(config.disabled, false)
  assert.throws(() => beaconServerConfig("", deps), /project root is required/)
})
