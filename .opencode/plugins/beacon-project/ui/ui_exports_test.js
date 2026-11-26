// Run: node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js --test .opencode/plugins/beacon-project/ui_exports_test.js
// Guards the ui_model / ui_view / ui_bridge split against broken cross-module imports.
import { readFileSync } from "node:fs"
import { test } from "node:test"
import assert from "node:assert/strict"
import { Plugin } from "@opencode/plugin"

const modulePaths = [
  "./ui_model.js",
  "./ui_view.js",
  "./ui_bridge.js",
  "./ui.js",
  "../index.js",
  "../state/location.js",
  "../mcp/mcp.js",
  "../rules/context.js",
  "../state/options.js",
  "./ui_alerts.js",
]

async function importAll() {
  return Promise.all(modulePaths.map((path) => import(path)))
}

test("every plugin module imports without a missing export", async () => {
  await importAll()
})

test("createRunChangeBridge is exported from ui_bridge.js, not ui_model.js", async () => {
  const [model, , bridge, ui] = await importAll()
  assert.equal(typeof bridge.createRunChangeBridge, "function")
  assert.equal(typeof ui.createRunChangeBridge, "function")
  assert.equal(ui.createRunChangeBridge, bridge.createRunChangeBridge)
  assert.equal(model.createRunChangeBridge, undefined)
})

test("the default plugin export resolves", async () => {
  const [, , , , index] = await importAll()
  assert.ok(index.default)
  assert.equal(typeof index.beaconProject, "object")
})

test("entry modules take Plugin from the host without an identity fallback", () => {
  for (const file of ["../index.js", "./ui.js"]) {
    const src = readFileSync(new URL(file, import.meta.url), "utf8")
    assert.match(src, /import\s*\{\s*Plugin\s*\}\s*from\s*"@opencode\/plugin/)
    assert.equal(src.includes("catch(() => (definition) => definition)"), false, file)
  }
})

test("both default exports are the host define result, not the definition", async () => {
  // The loader maps both host specifiers to this one Plugin double, so swapping
  // define reaches both entry modules. A null marker makes an identity fallback
  // or a || / ?? replacement return the definition and fail these assertions.
  const hostResult = null
  const originalDefine = Plugin.define
  Plugin.define = () => hostResult
  try {
    const fresh = "?host-define-sentinel"
    const [index, ui] = await Promise.all([
      import(`../index.js${fresh}`),
      import(`./ui.js${fresh}`),
    ])
    assert.equal(index.default, hostResult)
    assert.equal(ui.default, hostResult)
  } finally {
    Plugin.define = originalDefine
  }
})
