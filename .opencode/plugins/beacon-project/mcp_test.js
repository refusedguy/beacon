// Tests for the beacon MCP command resolver.
// Run: node --test .opencode/plugins/beacon-project/mcp_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { delimiter, join } from "node:path"
import {
  beaconCommand,
  beaconServerConfig,
  registerBeaconMcp,
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
  let disposed = 0
  const editor = { set() {}, update(name, apply) { apply({}) } }
  return {
    reloads,
    get disposed() { return disposed },
    mcp: {
      async transform(apply) {
        apply(editor)
        return { dispose() { disposed += 1 } }
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

test("registration sets the server once without starting a watcher or reload", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "setTimeout"] })
  const ctx = fakeCtx()
  let hash = "h0"
  const deps = localBinaryDeps({ hash: () => hash })

  const result = await registerBeaconMcp(ctx, ROOT, deps)
  assert.equal(typeof result.registration.dispose, "function")

  hash = "h1"
  t.mock.timers.tick(60_000)
  await Promise.resolve()
  await Promise.resolve()

  assert.equal(ctx.reloads.length, 0)
  result.registration.dispose()
  assert.equal(ctx.disposed, 1)
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
