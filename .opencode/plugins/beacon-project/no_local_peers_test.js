// CI guard: the plugin must use the host's TUI peers. A second copy under the
// plugin yields "No renderer found" because the renderer context belongs to a
// different Solid module instance. Run: node --test .opencode/plugins/beacon-project/no_local_peers_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join, relative, sep } from "node:path"
import { fileURLToPath } from "node:url"

const PLUGIN_ROOT = dirname(fileURLToPath(import.meta.url))
const PEERS = ["solid-js", join("@opentui", "solid"), join("@opentui", "core")]

function isPeerPath(path) {
  return PEERS.some((peer) => path === peer || path.endsWith(sep + peer))
}

export function findLocalPeerCopies(nodeModulesDir) {
  const found = []
  if (!existsSync(nodeModulesDir)) return found

  function walk(dir) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      const rel = relative(nodeModulesDir, full)
      if (isPeerPath(rel)) found.push(rel)
      if (entry.isDirectory()) walk(full)
    }
  }

  walk(nodeModulesDir)
  return found
}

test("plugin checkout has no local TUI peer copies", () => {
  assert.deepEqual(findLocalPeerCopies(join(PLUGIN_ROOT, "node_modules")), [])
})

test("walker rejects each TUI peer in a nested node_modules and clears after removal", (t) => {
  const root = mkdtempSync(join(tmpdir(), "beacon-no-peers-"))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const nodeModules = join(root, "node_modules")
  mkdirSync(nodeModules, { recursive: true })

  PEERS.forEach((peer, index) => {
    const wrapper = join(nodeModules, `wrapper-${index}`, "node_modules")
    const target = join(wrapper, peer)
    mkdirSync(target, { recursive: true })
    assert.deepEqual(findLocalPeerCopies(nodeModules), [relative(nodeModules, target)])
    rmSync(join(nodeModules, `wrapper-${index}`), { recursive: true })
  })

  assert.deepEqual(findLocalPeerCopies(nodeModules), [])
})
