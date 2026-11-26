// CI guard: TUI peers are pinned to the versions bundled by the supported
// OpenCode CLI. The plugin declares them as exact peerDependencies and never
// installs a local copy, so the host supplies one renderer instance.
// Run: node --test .opencode/plugins/beacon-project/peers_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const PLUGIN_ROOT = dirname(fileURLToPath(import.meta.url))
const manifest = JSON.parse(readFileSync(join(PLUGIN_ROOT, "supported-cli.json"), "utf8"))
const pkg = JSON.parse(readFileSync(join(PLUGIN_ROOT, "package.json"), "utf8"))

const TUI_PEERS = ["@opentui/core", "@opentui/solid", "solid-js"]
const RUNTIME_DEP_FIELDS = ["dependencies", "optionalDependencies"]

function isExactVersion(value) {
  return typeof value === "string" && /^\d+\.\d+\.\d+$/.test(value)
}

test("supported CLI manifest pins the CLI and each TUI peer to an exact version", () => {
  assert.ok(isExactVersion(manifest.cliVersion), "cliVersion is an exact version, not a range")
  assert.deepEqual(Object.keys(manifest.peers).sort(), [...TUI_PEERS].sort())
  for (const peer of TUI_PEERS) {
    assert.ok(isExactVersion(manifest.peers[peer]), `${peer} is an exact version, not a range`)
  }
})

test("package peerDependencies match the supported CLI manifest exactly", () => {
  const peers = pkg.peerDependencies ?? {}
  for (const peer of TUI_PEERS) {
    assert.equal(
      peers[peer],
      manifest.peers[peer],
      `${peer} matches the supported CLI version; a range or a different version fails`,
    )
  }
})

test("no TUI peer is a runtime dependency", () => {
  for (const field of RUNTIME_DEP_FIELDS) {
    const deps = pkg[field] ?? {}
    for (const peer of TUI_PEERS) {
      assert.equal(deps[peer], undefined, `${peer} must not be in ${field}`)
    }
  }
})
