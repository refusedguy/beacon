// Run: node --test .opencode/plugins/beacon-project/mcp_cache_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { join } from "node:path"
import { beaconCommand } from "./mcp.js"

const ROOT = "/project"
const BINARY_MTIME = 1000

function depsFor(sourceMtime, binaryMtime) {
  const local = join(ROOT, ".opencode", "bin", "beacon")
  const cmd = join(ROOT, "cmd")
  const cmdBeacon = join(cmd, "beacon")
  const mainGo = join(cmdBeacon, "main.go")
  const times = { [local]: binaryMtime, [mainGo]: sourceMtime }
  const builds = []
  return {
    builds,
    binaryMtime: (value) => { times[local] = value },
    env: {},
    existsSync: (path) => path === local || path === mainGo,
    statSync: (path) => {
      const dir = path === cmd || path === cmdBeacon
      if (!dir && !Object.hasOwn(times, path)) throw new Error(`ENOENT ${path}`)
      return { mtimeMs: dir ? 0 : times[path], isDirectory: () => dir }
    },
    readdirSync: (path) => path === cmd ? ["beacon"] : path === cmdBeacon ? ["main.go"] : [],
    execFileSync: (command, args, opts) => { builds.push({ command, args, opts }) },
  }
}

test("repeated resolutions rebuild once and recheck after the binary mtime changes", () => {
  const deps = depsFor(BINARY_MTIME + 5, BINARY_MTIME)
  const command = [join(ROOT, ".opencode", "bin", "beacon"), "mcp"]
  for (let i = 0; i < 4; i += 1) assert.deepEqual(beaconCommand(ROOT, deps), command)
  assert.equal(deps.builds.length, 1, "concurrent polls do not race the build script")
  deps.binaryMtime(BINARY_MTIME + 10)
  assert.deepEqual(beaconCommand(ROOT, deps), command)
  assert.equal(deps.builds.length, 1, "a fresh binary needs no rebuild")
})
