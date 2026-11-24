// Run: node --test .opencode/plugins/beacon-project/ui_setup_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { NOW, lines, makeRun, solidModule, themeStub, signalStub } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  BEACON_STATUS_RPC_ID,
  BeaconStatusRpc,
  CliStatusSource,
  DECISION_LIMIT,
  EMPTY_SNAPSHOT,
  PANEL_NAME,
  RECENT_LIMIT,
  REASON_WIDTH,
  TITLE_WIDTH,
  beaconStatusDefinition,
  createCliStatusSource,
  createRowMount,
  createStatusSource,
  defineBeaconStatusPlugin,
  detailCommand,
  detailLines,
  elapsed,
  enrichRuns,
  errorMessage,
  normalizeRun,
  normalizeSnapshot,
  parseSnapshot,
  readTitle,
  renderSnapshotLines,
  runLineItems,
  snapshotLineItems,
  truncate,
  default: beaconStatus,
} = ui

test("row mount reuses nodes across refreshes and keeps the click handler", () => {
  let nextNode = 0
  const created = []
  const ui = {
    createElement: (tag) => {
      const node = { id: ++nextNode, tag, props: {}, children: [] }
      created.push(node)
      return node
    },
    insert: (parent, child) => parent.children.push(child),
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const picked = []
  const color = (key) => `theme.${key}`
  const mount = createRowMount(ui, { color, BOLD: 1, onRow: (item) => picked.push(item), hoverBg: "theme.hover" })
  const container = ui.createElement("box")
  let items = snapshotLineItems({ active: [makeRun({ title: "First" })], recent: [] }, null, NOW)
  const apply = mount(container, () => items)
  const afterFirst = created.length
  const boxes = container.children
  const handler = boxes[2].props.onMouseDown

  items = snapshotLineItems({ active: [makeRun({ State: "blocked", title: "Second" })], recent: [] }, null, NOW)
  apply()

  assert.equal(created.length, afterFirst, "refresh must not create nodes")
  assert.equal(container.children[2], boxes[2], "row node identity is stable")
  assert.equal(boxes[2].props.onMouseDown, handler, "click handler survives")
  assert.equal(boxes[2].children[1].props.content, "T-1 Second")
  assert.equal(boxes[2].children[1].props.fg, "theme.base")
  assert.equal(boxes[2].children[0].props.content, "▲")
  assert.equal(boxes[2].children[0].props.fg, "theme.error")
  handler()
  assert.equal(picked.length, 1)
  assert.equal(picked[0].runID, "run-1")

  const beforeHide = container.children.length
  items = snapshotLineItems(EMPTY_SNAPSHOT, null, NOW)
  apply()
  assert.equal(created.length, afterFirst, "hiding rows must not create nodes")
  assert.equal(container.children.length, beforeHide, "rows are hidden, not removed")
  assert.equal(container.children[beforeHide - 1].props.visible, false)
})

test("setup defers every createElement to a slot render callback", async () => {
  const created = []
  const ui = {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push(node)
      return node
    },
    insert: (parent, child) => { parent.children.push(child) },
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const createSignal = (initial) => {
    let value = initial
    return [() => value, (next) => { value = typeof next === "function" ? next(value) : next }]
  }
  const modules = {
    "@opentui/solid": ui,
    "solid-js": { createSignal },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const renderers = Object.create(null)
  const ctx = {
    theme: {
      text: {
        base: "base", muted: "muted", action: { primary: { base: "action" } },
        feedback: {
          error: { base: "error" }, warning: { base: "warning" },
          success: { base: "success" }, info: { base: "info" },
        },
      },
      background: { raised: { high: "hover" } },
    },
    client: { rpc: () => ({ snapshot: async () => ({ active: [], recent: [] }) }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => {
        renderers[append] = render
        return () => { delete renderers[append] }
      },
      panel: { open: () => {} },
    },
    keymap: { layer: () => {} },
  }

  const dispose = await beaconStatusDefinition.setup(ctx, { import: (specifier) => modules[specifier] })
  assert.equal(created.length, 0, "setup must not call createElement before any render callback")
  for (const name of ["sidebar.content", "home.footer.status", "session.panel", "app"]) {
    assert.equal(typeof renderers[name], "function", `registers ${name}`)
  }

  const sidebar = renderers["sidebar.content"]()
  assert.ok(sidebar, "sidebar render returns a node")
  assert.ok(created.length > 0, "first render callback creates the nodes")

  const before = created.length
  assert.equal(renderers["sidebar.content"](), sidebar, "sidebar node is reused")
  assert.equal(renderers["home.footer.status"]() !== sidebar, true, "footer has its own node")
  assert.equal(created.length, before, "repeat renders create no new nodes")

  const detail = renderers["session.panel"]({ name: PANEL_NAME })
  assert.ok(detail, "panel render returns the detail node")
  assert.equal(renderers["session.panel"]({ name: "other" }), null, "other panels render nothing")
  assert.equal(created.length, before, "opening the detail panel creates no new nodes")

  dispose()
  assert.equal(Object.keys(renderers).length, 0, "dispose removes every slot")
})

test("a row count change during refresh creates no nodes outside a render callback", async () => {
  let current = { active: [], recent: [] }
  const created = []
  let renderActive = false
  const ui = {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push({ node, renderActive })
      return node
    },
    insert: (parent, child) => parent.children.push(child),
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const createSignal = (initial) => {
    let value = initial
    return [() => value, (next) => { value = typeof next === "function" ? next(value) : next }]
  }
  const modules = {
    "@opentui/solid": ui,
    "solid-js": { createSignal },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const renderers = Object.create(null)
  let tick = null
  const ctx = {
    theme: {
      text: {
        base: "base", muted: "muted", action: { primary: { base: "action" } },
        feedback: {
          error: { base: "error" }, warning: { base: "warning" },
          success: { base: "success" }, info: { base: "info" },
        },
      },
      background: { raised: { high: "hover" } },
    },
    client: { rpc: () => ({
      snapshot: async () => current,
      events: { on: () => () => {} },
    }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => {
        renderers[append] = render
        return () => { delete renderers[append] }
      },
      panel: { open: () => {} },
    },
    keymap: { layer: () => {} },
  }
  const deps = {
    import: (specifier) => modules[specifier],
    setInterval: (fn) => { tick = fn; return 1 },
    clearInterval: () => {},
    setTimeout: () => 1,
    clearTimeout: () => {},
  }

  const dispose = await beaconStatusDefinition.setup(ctx, deps)
  assert.equal(created.length, 0, "setup creates nothing before a render callback")
  assert.equal(typeof tick, "function", "the refresh timer is installed")

  renderActive = true
  const sidebar = renderers["sidebar.content"]()
  renderActive = false
  assert.ok(sidebar, "the first render callback returns the sidebar node")
  const afterEnsure = created.length
  assert.ok(afterEnsure > 0, "the first render callback allocates the row pool")
  assert.ok(created.every((call) => call.renderActive), "every node was created inside a render callback")

  const beforeVisible = sidebar.children.filter((child) => child.props.visible === true).length
  current = {
    active: [
      makeRun({ ID: "run-a", TaskID: "T-A", title: "First" }),
      makeRun({ ID: "run-b", TaskID: "T-B", title: "Second" }),
      makeRun({ ID: "run-c", TaskID: "T-C", title: "Third" }),
    ],
    recent: [],
  }
  renderActive = false
  tick()
  await new Promise((resolve) => setImmediate(resolve))

  assert.equal(created.length, afterEnsure, "refresh with more rows must not create nodes")
  assert.ok(created.every((call) => call.renderActive), "no node was created outside a render callback")
  const afterVisible = sidebar.children.filter((child) => child.props.visible === true).length
  assert.ok(afterVisible > beforeVisible, "the panel shows the new rows")

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

test("detail panel ✕ closes once and swallows the next row click", async () => {
  const renderers = Object.create(null)
  let openCount = 0
  let closeCount = 0
  const createSignal = (initial) => {
    let value = initial
    return [() => value, (next) => { value = typeof next === "function" ? next(value) : next }]
  }
  const modules = {
    "@opentui/solid": solidModule(),
    "solid-js": { createSignal },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({
      active: [makeRun({ ID: "run-1", TaskID: "T-1", title: "First" })], recent: [],
    }) }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => {
        renderers[append] = render
        return () => { delete renderers[append] }
      },
      panel: { open: () => { openCount++ }, close: () => { closeCount++ } },
    },
    keymap: { layer: () => () => {} },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))

  const sidebar = renderers["sidebar.content"]()
  const row = sidebar.children.find((child) =>
    child.props.onMouseDown && String(child.children[1]?.props.content ?? "").includes("T-1"))
  assert.ok(row, "a run row exposes onMouseDown")

  const panel = renderers["session.panel"]({ name: PANEL_NAME, close: () => { closeCount++ } })
  const findText = (node, content) => {
    if (node.props?.content === content) return node
    for (const child of node.children ?? []) {
      const hit = findText(child, content)
      if (hit) return hit
    }
    return null
  }
  const closeButton = findText(panel, "✕")
  assert.ok(closeButton, "the detail panel renders a ✕ text")
  assert.equal(closeButton.props.fg, "muted")
  assert.equal(closeButton.props.selectable, false)
  assert.equal(closeButton.props.wrapMode, "none")
  assert.equal(typeof closeButton.props.onMouseUp, "function")

  closeButton.props.onMouseUp()
  assert.equal(closeCount, 1, "the slot close callback runs once")
  assert.equal(openCount, 0, "closing does not itself open the panel")

  row.props.onMouseDown()
  assert.equal(openCount, 0, "the click right after closing is swallowed")
  row.props.onMouseDown()
  assert.equal(openCount, 1, "the next click reopens the panel")

  dispose()
})
