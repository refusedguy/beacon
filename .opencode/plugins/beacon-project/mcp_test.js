// Tests for the beacon MCP command resolver.
// Run: node --test .opencode/plugins/beacon-project/mcp_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { delimiter, join } from "node:path"
import { beaconCommand, BEACON_BUILD_SCRIPT } from "./mcp.js"

const ROOT = "/project"

function fakeDeps({ files = {}, head = "abc123", env = {} } = {}) {
  const execCalls = []
  const existing = new Set(Object.keys(files))
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
      if (cmd === "git" && args[0] === "rev-parse") return `${head}\n`
      if (cmd === "bash") return ""
      throw new Error(`unexpected exec: ${cmd}`)
    },
  }
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
