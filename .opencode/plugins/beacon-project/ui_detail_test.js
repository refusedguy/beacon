// Run: node --test .opencode/plugins/beacon-project/*_test.js
// Detail panel and slot-remount cases split from ui_setup_test.js to keep
// every plugin file under 350 lines.
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { makeRun, solidModule, themeStub } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  PANEL_NAME, beaconStatusDefinition, default: beaconStatus,
} = ui

test("section clicks toggle pooled rows and ctrl+g toggles the panel", async () => {
  const created = []
  const renderers = Object.create(null)
  const initialSections = {
    Identity: false, Timing: false, Attempts: false, Reason: false, Timeline: false, Decisions: false,
  }
  const sectionState = { ...initialSections }
  const writes = []
  let panelName = null
  let layer = null
  const panel = {
    open: (name) => { panelName = name },
    close: () => { panelName = null },
    current: () => (panelName ? { name: panelName } : null),
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({ active: [makeRun()], recent: [] }) }) },
    storage: { store: (name, init) => {
      if (name === "beacon-detail-sections") {
        assert.deepEqual(init.initial, initialSections)
        return [sectionState, async (fn) => { fn(sectionState); writes.push({ ...sectionState }) }]
      }
      return [{ id: "run-1" }, async () => {}]
    } },
    ui: {
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel,
    },
    keymap: { layer: (fn) => { layer = fn(); return () => { layer = null } } },
  }
  const modules = {
    "@opentui/solid": solidModule(created),
    "solid-js": { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))

  const detail = renderers["session.panel"]({ name: PANEL_NAME, close: () => panel.close() })
  assert.equal(detail.children[0].children[1].children[0].props.content, "ctrl+g", "the hint names the real binding")
  assert.equal(JSON.stringify(detail).includes('"esc"'), false, "no esc label remains")
  const rows = detail.children[1].children
  const timing = rows.find((r) => r.children[1]?.props.content === "Timing")
  assert.ok(timing, "the Timing header is rendered")
  assert.equal(timing.children[0].props.content, "▶", "a stored false starts collapsed")
  const visible = () => rows.filter((r) => r.props.visible === true).length
  const before = visible()
  const beforeCreated = created.length

  timing.props.onMouseDown()
  assert.equal(created.length, beforeCreated, "a section click creates no nodes")
  assert.equal(timing.children[0].props.content, "▼", "the marker flips on click")
  assert.ok(visible() > before, "body rows become visible on the pooled nodes")
  assert.equal(writes.length, 1, "the flag is persisted")
  assert.equal(writes[0].Timing, true)

  timing.props.onMouseDown()
  assert.equal(timing.children[0].props.content, "▶")
  assert.equal(visible(), before, "the body is hidden again")
  assert.equal(writes.length, 2)
  assert.equal(writes[1].Timing, false)

  const cmd = layer.commands[0]
  assert.equal(cmd.bind, "ctrl+g")
  assert.equal(panelName, null)
  cmd.run()
  assert.equal(panelName, PANEL_NAME, "ctrl+g opens the panel")
  cmd.run()
  assert.equal(panelName, null, "ctrl+g closes the open panel")
  cmd.run()
  assert.equal(panelName, PANEL_NAME, "ctrl+g reopens it")
  dispose()
})

test("setup without injected deps keeps the TUI imports literal", async () => {
  const src = readFileSync(new URL("./ui.js", import.meta.url), "utf8")
  for (const specifier of ["@opentui/solid", "solid-js", "@opentui/core"]) {
    assert.ok(src.includes(`import("${specifier}")`), `literal import("${specifier}") is present`)
  }
  assert.ok(src.includes('import("@opencode/plugin/tui")'), "the TUI plugin import stays literal")

  // With no injected deps, setup must reach the runtime imports rather than
  // the guard. The TUI peers are absent under node --test, so a module
  // resolution failure is the only acceptable rejection.
  await assert.rejects(beaconStatusDefinition.setup({}), (error) => {
    assert.equal(/unexpected import/.test(error.message), false)
    assert.match(error.message, /Cannot find package|ERR_MODULE_NOT_FOUND|Cannot find module/)
    return true
  })
})

test("ui.js uses theme tokens and native text styling only", () => {
  const src = readFileSync(new URL("./ui.js", import.meta.url), "utf8")
  assert.equal(/#[0-9a-fA-F]{3,8}\b/.test(src), false, "no hardcoded hex colors")
  assert.equal(/ascii_font/.test(src), false)
  assert.equal(/setProp\([^,]+,\s*"(font|size|width|height)"/.test(src), false)
  for (const token of [
    "theme.text.base", "theme.text.muted", "theme.text.action.primary.base",
    "theme.text.feedback", "theme.background.raised.high",
  ]) assert.ok(src.includes(token), `uses ${token}`)
  assert.ok(src.includes('"wrapMode", "none"'))
  assert.ok(src.includes('"selectable", false'))
  assert.ok(src.includes('"truncate", true'))
  assert.ok(src.includes('"minWidth", 0'))
  assert.ok(src.includes('"onMouseDown"'))
  assert.ok(src.includes('"onMouseUp"'))
  assert.ok(src.includes("✕"))
  assert.ok(src.includes('append: "session.panel"'))
})

test("a destroyed sidebar slot is rebuilt without touching the other slots", async () => {
  const created = []
  const renderers = Object.create(null)
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({ active: [makeRun({ ID: "run-1", TaskID: "T-1", title: "First" })], recent: [] }) }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: { slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } }, panel: { open: () => {} } },
    keymap: { layer: () => () => {} },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => ({
      "@opentui/solid": solidModule(created),
      "solid-js": { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } },
      "@opentui/core": { TextAttributes: { BOLD: 1 } },
    })[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))
  const first = renderers["sidebar.content"]()
  const footer = renderers["home.footer.status"]()
  const detail = renderers["session.panel"]({ name: PANEL_NAME })
  for (const node of [first, ...first.children]) node.isDestroyed = true
  const before = created.length
  const second = renderers["sidebar.content"]()
  assert.notEqual(second, first, "remount returns a fresh sidebar node")
  assert.ok(created.length > before, "the destroyed slot allocates a fresh row pool")
  assert.equal(renderers["home.footer.status"](), footer, "the footer node is kept")
  assert.equal(renderers["session.panel"]({ name: PANEL_NAME }), detail, "the detail node is kept")
  assert.ok(second.children.some((c) => c.props.visible === true && String(c.children[1]?.props.content ?? "").includes("T-1")), "the new node shows current runs")
  dispose()
})
