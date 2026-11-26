// Run: node --test .opencode/plugins/beacon-project/*_test.js
// Detail panel and slot-remount cases split from ui_setup_test.js to keep
// every plugin file under 350 lines.
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { makeRun, solidModule, themeStub } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const { DETAIL_OPEN, PANEL_NAME, SIDEBAR_OPEN, beaconStatusDefinition } = ui

function signals() {
  return { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } }
}
function modules(created) {
  return {
    "@opentui/solid": solidModule(created),
    "solid-js": signals(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
}
function storageStub() {
  const values = {
    "beacon-selected-run": { id: "run-1" },
    "beacon-sidebar-sections": { ...SIDEBAR_OPEN },
    "beacon-detail-sections": { ...DETAIL_OPEN },
  }
  const writes = Object.create(null)
  return {
    writes,
    api: { store: (name, init) => {
      values[name] = { ...init.initial, ...(values[name] ?? {}) }
      writes[name] = []
      return [values[name], async (fn) => { fn(values[name]); writes[name].push({ ...values[name] }) }]
    } },
  }
}
function findItem(container, predicate) {
  return container.children.find((child) => child.item && predicate(child.item))
}

test("sidebar and detail sections collapse with zero new createElement and persist", async () => {
  const created = []
  const renderers = Object.create(null)
  const storage = storageStub()
  let panelName = null
  let layer = null
  const panel = {
    open: (name) => { panelName = name },
    close: () => { panelName = null },
    current: () => (panelName ? { name: panelName } : null),
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({ active: [makeRun()], recent: [makeRun({ ID: "r2", State: "merged" })] }) }) },
    storage: storage.api,
    ui: {
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel,
    },
    keymap: { layer: (fn) => { layer = fn(); return () => { layer = null } } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules(created)[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))
  const sidebar = renderers["sidebar.content"]()
  const detail = renderers["session.panel"]({ name: PANEL_NAME, close: () => panel.close() })

  const activeHeader = findItem(sidebar, (item) => item.sectionKey === "active")
  assert.ok(activeHeader, "the Active header is clickable")
  assert.equal(activeHeader.children[0].props.content, "▼")
  const visibleRows = () => sidebar.children.filter((child) => child.props.visible === true).length
  const beforeSidebar = visibleRows()
  const beforeCreated = created.length
  activeHeader.props.onMouseDown()
  assert.equal(created.length, beforeCreated, "a sidebar section click creates no nodes")
  assert.equal(activeHeader.children[0].props.content, "▶")
  assert.ok(visibleRows() < beforeSidebar, "the body rows are hidden")
  assert.equal(storage.writes["beacon-sidebar-sections"].length, 1)
  assert.equal(storage.writes["beacon-sidebar-sections"][0].active, false)

  const scroll = detail.children.find((child) => child.children?.some((row) => row.item?.sectionKey))
  const whyHeader = findItem(scroll, (item) => item.sectionKey === "why")
  assert.ok(whyHeader, "the Why / Blocker header is clickable")
  assert.equal(whyHeader.children[0].props.content, "▼")
  const beforeDetail = scroll.children.filter((child) => child.props.visible === true).length
  whyHeader.props.onMouseDown()
  assert.equal(created.length, beforeCreated, "a detail section click creates no nodes")
  assert.equal(whyHeader.children[0].props.content, "▶")
  assert.ok(scroll.children.filter((child) => child.props.visible === true).length < beforeDetail)
  assert.equal(storage.writes["beacon-detail-sections"].length, 1)
  assert.equal(storage.writes["beacon-detail-sections"][0].why, false)

  const cmd = layer.commands[0]
  assert.equal(cmd.bind, "ctrl+g")
  assert.equal(panelName, null, "rendering the panel does not emit an open call")
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
  assert.match(src, /import\s*\{\s*Plugin\s*\}\s*from\s*"@opencode\/plugin\/tui"/, "the host Plugin import is static")
  await assert.rejects(beaconStatusDefinition.setup({}), (error) => {
    assert.equal(/unexpected import/.test(error.message), false)
    assert.match(error.message, /Cannot find package|ERR_MODULE_NOT_FOUND|Cannot find module/)
    return true
  })
})

test("plugin rendering uses theme tokens and native text styling only", async () => {
  const files = ["ui.js", "ui_nodes.js", "ui_sidebar.js", "ui_detail_view.js", "ui_actions.js"]
  const src = files.map((file) => readFileSync(new URL(`./${file}`, import.meta.url), "utf8")).join("\n")
  assert.equal(/#[0-9a-fA-F]{3,8}\b/.test(src), false, "no hardcoded hex colors")
  assert.equal(/ascii_font/.test(src), false)
  assert.equal(/setProp\([^,]+,\s*"(font|size|width|height)"/.test(src), false)
  // The view, model and bridge modules must load without the TUI peer. The
  // ui.js import at the top succeeds while @opentui/core is unavailable, so
  // these exported functions prove none of the three modules imports it.
  await assert.rejects(import("@opentui/core"), /ERR_MODULE_NOT_FOUND|Cannot find package|Cannot find module/)
  for (const name of ["detailModel", "normalizeRun", "createRunChangeBridge"]) {
    assert.equal(typeof ui[name], "function", `${name} loads without @opentui/core`)
  }
  for (const token of [
    "theme.text.base", "theme.text.muted", "theme.text.action.primary.base",
    "theme.text.feedback", "theme.background.raised.high",
  ]) assert.ok(src.includes(token), `uses ${token}`)
  const nodes = readFileSync(new URL("./ui_nodes.js", import.meta.url), "utf8")
  for (const token of ["wrapMode", '"word"', '"none"', '"selectable", false', '"truncate"', '"minWidth", 0', '"onMouseDown"', '"onMouseUp"']) {
    assert.ok(nodes.includes(token) || src.includes(token), `uses ${token}`)
  }
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
    import: (specifier) => modules(created)[specifier],
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
  assert.ok(second.children.some((child) => child.item?.runID === "run-1"), "the new node shows current runs")
  dispose()
})
