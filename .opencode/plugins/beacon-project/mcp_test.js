// Tests for the beacon MCP command resolver.
// Run: node --test .opencode/plugins/beacon-project/mcp_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { delimiter, join } from "node:path"
import {
  beaconCommand,
  registerBeaconMcp,
  BEACON_BUILD_SCRIPT,
  HEAD_POLL_MS,
} from "./mcp.js"

const ROOT = "/project"

function fakeDeps({ files = {}, head = "abc123", env = {} } = {}) {
  const execCalls = []
  const existing = new Set(Object.keys(files))
  const headValue = () => (typeof head === "function" ? head() : head)
  return {
    execCalls,
    env,
    existsSync: (path) => existing.has(path),
    readFileSync: (path) => {
      if (!existing.has(path)) throw new Error(`ENOENT ${path}`)
      return files[path]
    },
    execFileSync: (cmd, args, opts) => {
      execCalls.push({ cmd, args, opts })
      if (cmd === "git" && args[0] === "rev-parse") return `${headValue()}\n`
      if (cmd === "bash") return ""
      throw new Error(`unexpected exec: ${cmd}`)
    },
  }
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

function gitCalls(deps) {
  return deps.execCalls.filter((call) => call.cmd === "git")
}

function localBinaryDeps(overrides = {}) {
  const { local, sha } = localPaths()
  return fakeDeps({
    files: { [local]: "binary", [sha]: "abc123\n" },
    ...overrides,
  })
}

function localPaths(root = ROOT) {
  const local = join(root, ".opencode", "bin", "beacon")
  return { local, sha: `${local}.build-sha` }
}

function buildCalls(deps) {
  return deps.execCalls.filter((call) => call.cmd === "bash")
}

test("local binary with a matching build sha is used without a rebuild", () => {
  const { local, sha } = localPaths()
  const deps = fakeDeps({
    files: { [local]: "binary", [sha]: "abc123\n" },
    head: "abc123",
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])
  assert.equal(buildCalls(deps).length, 0)
})

test("stale build sha triggers one rebuild and the local binary is still returned", () => {
  const { local, sha } = localPaths()
  const deps = fakeDeps({
    files: { [local]: "binary", [sha]: "oldsha\n" },
    head: "newsha",
  })

  assert.deepEqual(beaconCommand(ROOT, deps), [local, "mcp"])

  const builds = buildCalls(deps)
  assert.equal(builds.length, 1)
  assert.deepEqual(builds[0].args, [join(ROOT, ...BEACON_BUILD_SCRIPT.split("/"))])
  assert.equal(builds[0].opts.cwd, ROOT)
})

test("missing build sha triggers one rebuild", () => {
  const { local } = localPaths()
  const deps = fakeDeps({ files: { [local]: "binary" }, head: "abc123" })

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

test("HEAD watcher polls without reloading while HEAD is unchanged", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] })
  const ctx = fakeCtx()
  const deps = localBinaryDeps({ head: () => "abc123" })

  await registerBeaconMcp(ctx, ROOT, deps)

  t.mock.timers.tick(HEAD_POLL_MS * 3)
  assert.ok(gitCalls(deps).length >= 3)
  assert.equal(ctx.reloads.length, 0)
})

test("HEAD watcher reloads once when HEAD advances", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] })
  let head = "abc123"
  const ctx = fakeCtx()
  const deps = localBinaryDeps({ head: () => head })

  await registerBeaconMcp(ctx, ROOT, deps)
  assert.equal(ctx.reloads.length, 0)

  head = "newsha"
  t.mock.timers.tick(HEAD_POLL_MS)
  assert.equal(ctx.reloads.length, 1)

  t.mock.timers.tick(HEAD_POLL_MS * 3)
  assert.equal(ctx.reloads.length, 1)
})

test("HEAD watcher is skipped when BEACON_BIN is set", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] })
  const explicit = "/custom/beacon"
  const ctx = fakeCtx()
  const deps = localBinaryDeps({
    files: {
      [join(ROOT, ".opencode", "bin", "beacon")]: "binary",
      [`${join(ROOT, ".opencode", "bin", "beacon")}.build-sha`]: "abc123\n",
      [explicit]: "binary",
    },
    env: { BEACON_BIN: explicit },
  })

  await registerBeaconMcp(ctx, ROOT, deps)
  t.mock.timers.tick(HEAD_POLL_MS * 5)

  assert.equal(gitCalls(deps).length, 0)
  assert.equal(ctx.reloads.length, 0)
})

test("HEAD watcher does not reload before registration completes", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] })
  let head = "abc123"
  let releaseTransform
  const ctx = fakeCtx()
  ctx.mcp.transform = (apply) => new Promise((resolve) => {
    releaseTransform = () => {
      apply({ set() {}, update() {} })
      resolve({ dispose() {} })
    }
  })
  const deps = localBinaryDeps({ head: () => head })

  const registering = registerBeaconMcp(ctx, ROOT, deps)
  head = "newsha"
  t.mock.timers.tick(HEAD_POLL_MS * 3)
  assert.equal(ctx.reloads.length, 0)

  releaseTransform()
  await registering
  assert.equal(ctx.reloads.length, 0)

  head = "anothersha"
  t.mock.timers.tick(HEAD_POLL_MS)
  assert.equal(ctx.reloads.length, 1)
})

test("reload re-resolves the command and rebuilds a stale binary", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] })
  let head = "abc123"
  const { local, sha } = localPaths()
  const deps = fakeDeps({
    files: { [local]: "binary", [sha]: "abc123\n" },
    head: () => head,
  })
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

  await registerBeaconMcp(ctx, ROOT, deps)
  assert.equal(buildCalls(deps).length, 0)

  head = "newsha"
  t.mock.timers.tick(HEAD_POLL_MS)
  await Promise.resolve()

  assert.equal(ctx.reloads.length, 1)
  assert.equal(buildCalls(deps).length, 1)
})
