// Rendered-shape tests for the Beacon panels.
// Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  ACTION_NAMES, DECISION_LIMIT, PANEL_NAME, beaconStatusDefinition,
  createDetailMount, createRowMount, detailLines, normalizeRun, snapshotLineItems,
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
function signals() {
  return { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } }
}

test("rendered rows use native flex primitives with no mid-word wrap", () => {
  const mount = createRowMount(solidModule(), { color: (k) => `theme.${k}`, BOLD: 1 })
  const container = { children: [], tag: "box" }
  mount(container, () => snapshotLineItems({ active: [makeRun()], recent: [] }, null, NOW), 8)
  const row = container.children.find((box) => box.item?.runID === "run-1")
  assert.equal(row.props.flexDirection, "row")
  assert.equal(row.props.gap, 1)
  assert.equal(row.props.minWidth, 0)
  const [marker, name, status] = row.children
  assert.equal(marker.props.flexShrink, 0)
  assert.equal(marker.props.wrapMode, "none")
  assert.equal(marker.props.truncate, true)
  assert.equal(name.props.flexGrow, 1)
  assert.equal(name.props.flexShrink, 1)
  assert.equal(name.props.minWidth, 0)
  assert.equal(name.props.wrapMode, "none")
  assert.equal(name.props.truncate, true)
  assert.equal(status.props.flexShrink, 0)
})

test("compact header carries the human summary and R C B M counters", () => {
  const snapshot = {
    active: [makeRun({ ID: "run-1", State: "blocked" }), makeRun({ ID: "run-2" })],
    recent: [],
    generatedAt: "2026-11-23T10:09:00Z",
    metrics: { blockedCount: 3, rebaseCount: 2, fixCount: 4 },
  }
  const items = snapshotLineItems(snapshot, null, NOW)
  assert.equal(items[0].name, "Beacon")
  assert.equal(items[0].status, "1 running · 1 blocked")
  assert.equal(items[1].name, "R1 C0 B1 M0")
  const mount = createRowMount(solidModule(), { color: (k) => `theme.${k}`, BOLD: 1 })
  const container = { children: [], tag: "box" }
  mount(container, () => items, 8)
  assert.equal(container.children[0].children[2].props.content, "1 running · 1 blocked")
})

test("detail hero wraps the full title and reasons, and exposes fixed action buttons", () => {
  const created = []
  const color = (k) => `theme.${k}`
  const mount = createDetailMount(solidModule(created), {
    color, BOLD: 1, setProp: (node, key, value) => { node.props[key] = value },
    onAction: () => {}, onToggle: () => {}, onClose: () => {},
  })
  const model = {
    badge: "▲ BLK · 2s", badgeColor: "error", pr: "No PR yet", branch: "beacon/T-1",
    title: "Working collapsible sections and honest close hint in detail panel".repeat(2),
    task: "T-1", phase: "Run run-1 · Coding 0s · CI 0s · Blocked 2s",
    actions: ["retry", "approve", "cancel", "pr", "help", "copy"],
    rows: [
      { marker: "▼", name: "Why / Blocker", status: "", color: "action", bold: true, sectionKey: "why" },
      { marker: "↳", name: "workspace: conflict: refusing to remove dirty worktree at /tmp/wt", status: "", color: "error" },
    ],
  }
  const run = normalizeRun(makeRun({ State: "blocked" }))
  mount.apply(model, run)
  assert.ok(findText(mount.node, "Beacon"), "the panel has the compact title")
  const title = findText(mount.node, model.title)
  assert.ok(title, "hero shows the full title")
  assert.equal(title.props.wrapMode, "word")
  assert.equal(title.props.truncate, false)
  assert.equal(title.props.flexGrow, undefined, "the hero title does not grow in the column card")
  assert.equal(title.props.attributes, 1, "the hero title is bold")
  const branch = findText(mount.node, model.branch)
  assert.equal(branch?.props.wrapMode, "word", "branch lives in its own wrapping key-value row")
  const reason = findText(mount.node, model.rows[1].name)
  assert.equal(reason.props.wrapMode, "word", "the reason wraps")
  assert.equal(reason.props.truncate, false)
  assert.ok(mount.node.children.some((c) => c.tag === "scrollbox" || c.children?.some((x) => x.tag === "scrollbox")), "sections live in a scrollbox")
  for (const name of ACTION_NAMES) {
    assert.equal(typeof mount.buttons[name].props.onMouseUp, "function", `${name} is clickable`)
    assert.equal(mount.buttons[name].props.visible, true)
  }
  assert.equal(mount.buttons.cancel.props.fg, "theme.error", "cancel uses the error tone")
  const before = created.length
  mount.apply({ ...model, actions: ["copy"], rows: [model.rows[0]] }, run)
  assert.equal(created.length, before, "collapsing creates no nodes")
  assert.equal(mount.buttons.retry.props.visible, false, "buttons follow the action list")
})

test("first click opens, clicking the open row closes, and ctrl+g toggles", async () => {
  const renderers = Object.create(null)
  let openCount = 0
  let closeCount = 0
  const modules = {
    "@opentui/solid": solidModule(),
    "solid-js": signals(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const panel = {
    open: () => { openCount++ }, close: () => { closeCount++ },
    current: () => (openCount > closeCount ? { name: PANEL_NAME } : null),
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({ active: [makeRun()], recent: [] }) }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel,
    },
    keymap: { layer: (fn) => ({ commands: fn().commands }) },
  }
  let layer
  ctx.keymap.layer = (fn) => { layer = fn(); return () => { layer = null } }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))
  const sidebar = renderers["sidebar.content"]()
  const row = sidebar.children.find((child) => child.item?.runID === "run-1")
  assert.ok(row, "a run row exposes a click handler")

  const detail = renderers["session.panel"]({ name: PANEL_NAME, close: () => { closeCount++ } })
  const closeButton = findText(detail, "✕")
  assert.ok(closeButton, "the panel renders a red ✕")
  assert.equal(closeButton.props.fg, "error")
  assert.equal(typeof closeButton.props.onMouseUp, "function")
  assert.equal(JSON.stringify(detail).includes('"esc"'), false, "no esc hint remains")
  assert.equal(JSON.stringify(detail).includes("fullscreen"), false, "no fullscreen button remains")

  row.props.onMouseDown()
  assert.equal(openCount, 1, "the first click opens detail")
  closeButton.props.onMouseUp()
  assert.equal(closeCount, 1, "the ✕ closes detail")
  row.props.onMouseDown()
  assert.equal(openCount, 2, "the next click opens again, no swallowed gesture")
  row.props.onMouseDown()
  assert.equal(closeCount, 2, "clicking the selected open row closes detail")

  const cmd = layer.commands[0]
  assert.equal(cmd.bind, "ctrl+g")
  cmd.run()
  assert.equal(openCount, 3, "ctrl+g reopens")
  cmd.run()
  assert.equal(closeCount, 3, "ctrl+g closes the open panel")
  dispose()
})

test("rendered detail caps decisions at 20 with an earlier marker", () => {
  const decisions = Array.from({ length: 25 }, (_, i) => ({
    at: `2026-11-23T10:${String(i).padStart(2, "0")}:00Z`,
    kind: "reconcile.decision", verdict: `verdict-${i}`,
  }))
  const rows = detailLines(normalizeRun(makeRun({ Decisions: decisions })), {}, NOW, { technical: true })
  const names = rows.map((i) => i.name ?? i.text)
  assert.equal(names.filter((n) => n.includes("reconcile.decision")).length, DECISION_LIMIT)
  assert.ok(names.includes("... 5 earlier decisions"))
})
