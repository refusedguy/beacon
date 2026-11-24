// Rendered-shape tests for the Beacon sidebar panel. Pure view tests live in
// ui_view_test.js and setup integration tests in ui_setup_test.js.
// Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  DECISION_LIMIT, PANEL_NAME, beaconStatusDefinition, createRowMount,
  detailLines, normalizeRun, snapshotLineItems,
} from "./ui.js"
import { NOW, makeRun, solidModule, themeStub } from "./ui_test_helpers.js"

function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const hit = findText(child, content)
    if (hit) return hit
  }
  return null
}

test("rendered rows use the native flex primitives with no mid-word wrap", () => {
  const mount = createRowMount(solidModule(), { color: (k) => `theme.${k}`, BOLD: 1 })
  const container = { children: [], tag: "box" }
  mount(container, () => snapshotLineItems({ active: [makeRun()], recent: [] }, null, NOW), 8)
  const row = container.children[2]
  assert.equal(row.props.flexDirection, "row")
  assert.equal(row.props.gap, 1)
  assert.equal(row.props.minWidth, 0)
  const [dot, name, status] = row.children
  assert.equal(dot.props.flexShrink, 0)
  assert.equal(name.props.flexGrow, 1)
  assert.equal(name.props.flexShrink, 1)
  assert.equal(name.props.minWidth, 0)
  assert.equal(name.props.wrapMode, "none")
  assert.equal(name.props.truncate, true)
  assert.equal(status.props.flexShrink, 0)
})

test("compact header carries active, blocked, rebase, fix counts and last update time", () => {
  const snapshot = {
    active: [makeRun({ ID: "run-1", State: "blocked" }), makeRun({ ID: "run-2" })],
    recent: [],
    generatedAt: "2026-11-23T10:09:00Z",
    metrics: { blockedCount: 3, rebaseCount: 2, fixCount: 4 },
  }
  const header = snapshotLineItems(snapshot, null, NOW)[0]
  assert.equal(header.name, "Beacon")
  assert.equal(header.status, "A2 B3 R2 F4 10:09:00")
  const mount = createRowMount(solidModule(), { color: (k) => `theme.${k}`, BOLD: 1 })
  const container = { children: [], tag: "box" }
  mount(container, () => snapshotLineItems(snapshot, null, NOW), 8)
  assert.equal(container.children[0].children[2].props.content, "A2 B3 R2 F4 10:09:00")
})

test("detail groups render as sections with an aligned label and value", () => {
  const mount = createRowMount(solidModule(), { color: (k) => `theme.${k}`, BOLD: 1 })
  const container = { children: [], tag: "box" }
  const rows = detailLines(normalizeRun(makeRun()), {}, NOW)
  mount(container, () => rows, rows.length)
  const sectionRow = container.children[0]
  assert.equal(sectionRow.children[0].props.content, "▼")
  assert.equal(sectionRow.children[1].props.content, "Identity")
  assert.equal(sectionRow.children[1].props.attributes, 1)
  const fieldRow = container.children.find((row) => row.children[1].props.content === "Branch")
  assert.equal(fieldRow.children[2].props.content, "beacon/T-1")
  assert.equal(fieldRow.children[2].props.fg, "theme.base")
})

test("detail panel close control is styled, padded and fires once", async () => {
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
  const title = findText(panel, "Beacon run detail")
  assert.equal(title.props.attributes, 1, "the header title is bold")
  assert.ok(findText(panel, "ctrl+g"), "a ctrl+g hint labels the control")
  const closeButton = findText(panel, "✕")
  assert.ok(closeButton, "the detail panel renders a ✕ text")
  assert.equal(closeButton.props.fg, "error", "the ✕ uses the error feedback color")
  assert.equal(closeButton.props.padding, 1, "padding widens the hit area")
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

test("rendered detail caps decisions at 20 with an earlier marker", () => {
  const decisions = Array.from({ length: 25 }, (_, i) => ({
    at: `2026-11-23T10:${String(i).padStart(2, "0")}:00Z`,
    kind: "reconcile.decision",
    verdict: `verdict-${i}`,
  }))
  const names = detailLines(normalizeRun(makeRun({ Decisions: decisions })), {}, NOW).map((i) => i.name ?? i.text)
  assert.equal(names.filter((n) => n.includes("reconcile.decision")).length, DECISION_LIMIT)
  assert.ok(names.includes("... 5 earlier decisions"))
})
