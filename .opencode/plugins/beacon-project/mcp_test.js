// Tests for the beacon MCP command resolver and build-hash watchdog.
// Run: node --test .opencode/plugins/beacon-project/mcp_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { delimiter, join } from "node:path"
import {
  beaconCommand,
  registerBeaconMcp,
  BEACON_BUILD_SCRIPT,
  BUILD_HASH_FLAG,
  BUILD_HASH_POLL_MS,
  RELOAD_DEBOUNCE_MS,
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
  const editor = { set() {}, update(name, apply) { apply({}) } }
  return {
    reloads,
    mcp: {
      async transform(apply) {
        apply(editor)
        return { dispose() {} }
      },
      async reload() {
        reloads.push(true)
      },
    },
  }
}

function ctxThatReresolves() {
  const ctx = fakeCtx()
  let applyTransform
  ctx.mcp.transform = (apply) => {
    applyTransform = apply
    apply({ set() {}, update() {} })
    return Promise.resolve({ dispose() {} })
  }
  ctx.mcp.reload = async () => {
    ctx.reloads.push(true)
    applyTransform({ set() {}, update() {} })
  }
  return ctx
}

function buildCalls(deps) {
  return deps.execCalls.filter((call) => call.cmd === "bash" && !call.args.includes(BUILD_HASH_FLAG))
}

function hashCalls(deps) {
  return deps.execCalls.filter((call) => call.cmd === "bash" && call.args.includes(BUILD_HASH_FLAG))
}

function gitCalls(deps) {
  return deps.execCalls.filter((call) => call.cmd === "git")
}

function recorded(deps) {
  const { sha } = localPaths()
  return String(deps.readFileSync(sha, "utf8")).trim()
}

function enableTimers(t) {
  t.mock.timers.enable({ apis: ["setInterval", "setTimeout"] })
}

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
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

test("reload debounce is at least ten seconds", () => {
  assert.ok(RELOAD_DEBOUNCE_MS >= 10000)
})

test("a HEAD-only change does not rebuild or reload", async (t) => {
  enableTimers(t)
  let head = "commit0"
  const ctx = ctxThatReresolves()
  const deps = localBinaryDeps({ head: () => head })

  await registerBeaconMcp(ctx, ROOT, deps)
  assert.equal(buildCalls(deps).length, 0)

  head = "commit1"
  t.mock.timers.tick(BUILD_HASH_POLL_MS * 5)
  await flush()

  assert.equal(gitCalls(deps).length, 0)
  assert.ok(hashCalls(deps).length >= 5)
  assert.equal(buildCalls(deps).length, 0)
  assert.equal(ctx.reloads.length, 0)
})

test("a source change rebuilds once and reloads once after the debounce", async (t) => {
  enableTimers(t)
  let hash = "h0"
  const ctx = ctxThatReresolves()
  const deps = localBinaryDeps({ hash: () => hash })

  await registerBeaconMcp(ctx, ROOT, deps)
  assert.equal(buildCalls(deps).length, 0)

  hash = "h1"
  t.mock.timers.tick(BUILD_HASH_POLL_MS)
  assert.equal(ctx.reloads.length, 0, "no reload before the debounce")
  assert.equal(buildCalls(deps).length, 0, "no rebuild before the debounce")

  t.mock.timers.tick(RELOAD_DEBOUNCE_MS - 1)
  assert.equal(ctx.reloads.length, 0, "still inside the debounce window")

  t.mock.timers.tick(1)
  await flush()

  assert.equal(ctx.reloads.length, 1)
  assert.equal(buildCalls(deps).length, 1)
  assert.equal(recorded(deps), "h1")
})

test("rapid successive source changes cause one reload", async (t) => {
  enableTimers(t)
  let hash = "h0"
  const ctx = ctxThatReresolves()
  const deps = localBinaryDeps({ hash: () => hash })

  await registerBeaconMcp(ctx, ROOT, deps)

  hash = "h1"
  t.mock.timers.tick(BUILD_HASH_POLL_MS)
  hash = "h2"
  t.mock.timers.tick(BUILD_HASH_POLL_MS)
  hash = "h3"
  t.mock.timers.tick(BUILD_HASH_POLL_MS)
  t.mock.timers.tick(RELOAD_DEBOUNCE_MS)
  await flush()

  assert.equal(ctx.reloads.length, 1)
  assert.equal(buildCalls(deps).length, 1)
  assert.equal(recorded(deps), "h3")

  t.mock.timers.tick(BUILD_HASH_POLL_MS * 5)
  await flush()
  assert.equal(ctx.reloads.length, 1)
  assert.equal(buildCalls(deps).length, 1)
})

test("watchdog is skipped when BEACON_BIN is set", async (t) => {
  enableTimers(t)
  const explicit = "/custom/beacon"
  const { local, sha } = localPaths()
  const ctx = ctxThatReresolves()
  const deps = fakeDeps({
    files: { [local]: "binary", [sha]: "h0\n", [explicit]: "binary" },
    hash: "h0",
    env: { BEACON_BIN: explicit },
  })

  await registerBeaconMcp(ctx, ROOT, deps)
  t.mock.timers.tick(BUILD_HASH_POLL_MS * 5)
  await flush()

  assert.equal(deps.execCalls.length, 0)
  assert.equal(ctx.reloads.length, 0)
})

test("watchdog does not reload before registration completes", async (t) => {
  enableTimers(t)
  let hash = "h0"
  let releaseTransform
  const ctx = fakeCtx()
  ctx.mcp.transform = (apply) => new Promise((resolve) => {
    releaseTransform = () => {
      apply({ set() {}, update() {} })
      resolve({ dispose() {} })
    }
  })
  const deps = localBinaryDeps({ hash: () => hash })

  const registering = registerBeaconMcp(ctx, ROOT, deps)
  hash = "h1"
  t.mock.timers.tick(BUILD_HASH_POLL_MS * 5)
  assert.equal(ctx.reloads.length, 0)

  releaseTransform()
  await registering
  assert.equal(ctx.reloads.length, 0)

  hash = "h2"
  t.mock.timers.tick(BUILD_HASH_POLL_MS)
  t.mock.timers.tick(RELOAD_DEBOUNCE_MS)
  await flush()
  assert.equal(ctx.reloads.length, 1)
})
