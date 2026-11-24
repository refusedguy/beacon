// Portable plugin checks: custom guard rules extend built-ins in a non-Go
// project, and a PATH-only beacon binary needs no local binary or main.go.
// Run: node --test .opencode/plugins/beacon-project/portability_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { join } from "node:path"
import { SHELL_GUARD_RULES, buildShellGuardRules, findShellGuardHit } from "./index.js"
import { beaconCommand, beaconServerConfig } from "./mcp/mcp.js"

const ROOT = "/plain-project"

function pathOnlyDeps() {
  const files = { [join("/usr/local/bin", "beacon")]: "binary" }
  return {
    env: { PATH: "/usr/local/bin" },
    existsSync: (path) => Object.hasOwn(files, path),
    statSync: () => { throw new Error("not a local binary") },
    readdirSync: () => { throw new Error("not a Go project") },
    execFileSync: () => { throw new Error("no build script to run") },
  }
}

test("custom shell guard rules extend built-ins in a project without Go", () => {
  const rules = buildShellGuardRules({
    shellGuardRules: [
      { id: "full pytest suite", pattern: /\bpytest\b/, replacement: "pytest for the changed package" },
    ],
  })
  assert.equal(rules.length, SHELL_GUARD_RULES.length + 1)
  assert.ok(SHELL_GUARD_RULES.every((rule) => rules.includes(rule)), "built-ins stay first")
  assert.equal(findShellGuardHit("bash", "pytest -q", rules)?.id, "full pytest suite")
  assert.equal(findShellGuardHit("bash", "go test ./...", rules)?.id, "full go test suite")
  assert.equal(findShellGuardHit("bash", "rg -n PORT src", rules), null)
})

test("a PATH-only beacon resolves with no local binary and no main.go", () => {
  const deps = pathOnlyDeps()
  assert.equal(deps.existsSync(join(ROOT, ".opencode", "bin", "beacon")), false)
  assert.equal(deps.existsSync(join(ROOT, "cmd", "beacon", "main.go")), false)
  assert.deepEqual(beaconCommand(ROOT, deps), [join("/usr/local/bin", "beacon"), "mcp"])
  assert.deepEqual(beaconServerConfig(ROOT, deps).command, [join("/usr/local/bin", "beacon"), "mcp"])
})

test("empty config keeps built-in guard rules untouched", () => {
  assert.equal(buildShellGuardRules({}), SHELL_GUARD_RULES)
  assert.equal(buildShellGuardRules({ shellGuardRules: [] }), SHELL_GUARD_RULES)
})
