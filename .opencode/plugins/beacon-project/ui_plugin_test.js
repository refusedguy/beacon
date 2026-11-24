// Run: node --test .opencode/plugins/beacon-project/ui_plugin_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { solidModule, themeStub, signalStub } from "./ui_test_helpers.js"
import { PANEL_NAME, beaconStatusDefinition, createCliStatusSource, createRowMount } from "./ui.js"

test("CLI source forwards snapshot limits as flags", async () => {
  const calls = []
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async (program, args) => { calls.push([program, ...args]); return { stdout: '{"active":[],"recent":[]}' } },
  }
  const source = createCliStatusSource({
    root: "/p",
    deps,
    limits: { recentLimit: 3, decisionsLimit: 9 },
  })
  await source.snapshot()
  assert.deepEqual(calls, [["/bin/beacon", "status", "--snapshot", "--recent-limit", "3", "--decisions-limit", "9"]])
})

test("statusPanelEnabled false skips all TUI setup", async () => {
  const slots = []
  const ctx = {
    ui: { slot: (spec) => { slots.push(spec); return () => {} } },
    keymap: { layer: () => { throw new Error("keymap must not run") } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPanelEnabled: false },
    import: () => { throw new Error("TUI peers must not load") },
  })
  assert.deepEqual(slots, [])
  assert.equal(typeof dispose, "function")
  dispose()
})

test("keymap layer is installed once and survives repeated app slot renders", async () => {
  let layers = 0
  let appRender = null
  const modules = {
    "@opentui/solid": solidModule(),
    "solid-js": signalStub(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({ active: [], recent: [] }) }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => {
        if (append === "app") appRender = render
        return () => {}
      },
      panel: { open: () => {} },
    },
    keymap: { layer: () => { layers++; return () => {} } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
  })
  assert.equal(layers, 1)
  appRender()
  appRender()
  assert.equal(layers, 1)
  dispose()
})

test("row mount accepts a per-slot row budget", () => {
  const color = () => "base"
  const makeMount = (limit) => {
    const created = []
    const mount = createRowMount(solidModule(created), { color })
    mount({ children: [] }, () => [], limit)
    return created.length
  }
  const footer = makeMount(1)
  const detail = makeMount(120)
  assert.ok(footer > 0 && footer < 20, `footer allocated ${footer} nodes`)
  assert.ok(detail > footer, `detail allocated ${detail} nodes`)
  const src = readFileSync(new URL("./ui.js", import.meta.url), "utf8")
  assert.ok(src.includes("maxLines: FOOTER_ROWS"), "the footer has a one-line budget")
  assert.ok(src.includes("createDetailMount(ui,"), "the detail slot owns its own node budget")
  assert.ok(readFileSync(new URL("./ui_detail_view.js", import.meta.url), "utf8").includes("DETAIL_LINES"))
})
