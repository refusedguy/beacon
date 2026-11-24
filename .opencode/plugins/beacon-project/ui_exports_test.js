// Run: node --test .opencode/plugins/beacon-project/ui_exports_test.js
// Guards the ui_model / ui_view / ui_bridge split against broken cross-module imports.
import { test } from "node:test"
import assert from "node:assert/strict"

const modulePaths = [
  "./ui_model.js",
  "./ui_view.js",
  "./ui_bridge.js",
  "./ui.js",
  "./index.js",
  "./location.js",
  "./mcp.js",
  "./context.js",
  "./options.js",
]

async function importAll() {
  return Promise.all(modulePaths.map((path) => import(path)))
}

test("every plugin module imports without a missing export", async () => {
  const loaded = await importAll()
  loaded.forEach((mod, index) => {
    assert.ok(mod, `${modulePaths[index]} failed to load`)
  })
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
