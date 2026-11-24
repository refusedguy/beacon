// Regression tests for the five live-TUI defects in T-20261124-63bc9761.
// Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  ACTION_NAMES, DETAIL_OPEN, PANEL_NAME, SIDEBAR_OPEN, beaconStatusDefinition,
  createDetailMount, createLineMount, detailModel, normalizeRun, stateBarItem,
} from "./ui.js"
import { NOW, makeRun, solidModule, themeStub } from "./ui_test_helpers.js"

const color = (key) => `theme.${key}`
const deps = {
  color, BOLD: 1, setProp: (node, key, value) => { node.props[key] = value },
  onAction: () => {}, onToggle: () => {}, onClose: () => {},
}
function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const hit = findText(child, content)
    if (hit) return hit
  }
  return null
}
function findItem(node, predicate) {
  if (node.item && predicate(node.item)) return node
  for (const child of node.children ?? []) {
    const hit = findItem(child, predicate)
    if (hit) return hit
  }
  return null
}
function modules(created) {
  return {
    "@opentui/solid": solidModule(created),
    "solid-js": { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
}

test("1. merged hero title stays visible, bold and wrapped without truncation", () => {
  const created = []
  const mount = createDetailMount(solidModule(created), deps)
  const title = "Hotfix: align live Beacon detail and sidebar with the approved mockup, wrapped across several lines"
  const run = normalizeRun(makeRun({ ID: "r-merged", TaskID: "T-9", State: "merged", title }))
  mount.apply(detailModel(run, {}, NOW), run)
  const node = findText(mount.node, title)
  assert.ok(node, "the full title is rendered, not cut to a fixed width")
  assert.equal(node.props.content.length, title.length, "no VALUE_WIDTH truncation")
  assert.equal(node.props.wrapMode, "word", "the title wraps")
  assert.equal(node.props.truncate, false, "the title is never truncated")
  assert.equal(node.props.flexGrow, undefined, "the title does not grow in the column card")
  assert.equal(node.props.attributes, 1, "the title is bold")
})

test("2. Copy ID is visible from mount and follows state for the other actions", () => {
  const mount = createDetailMount(solidModule([]), deps)
  assert.equal(mount.buttons.copy.props.visible, true, "Copy ID does not start hidden")
  const applyFor = (state, overrides = {}) => {
    const run = normalizeRun(makeRun({ ID: "r-state", TaskID: "T-9", State: state, ...overrides }))
    mount.apply(detailModel(run, {}, NOW), run)
    return Object.fromEntries(ACTION_NAMES.map((name) => [name, mount.buttons[name].props.visible]))
  }
  assert.deepEqual(applyFor("merged"), { retry: false, approve: false, cancel: false, pr: false, help: false, copy: true })
  assert.deepEqual(applyFor("blocked"), { retry: true, approve: false, cancel: false, pr: false, help: false, copy: true })
  assert.deepEqual(applyFor("waiting_ci"), { retry: false, approve: true, cancel: true, pr: false, help: true, copy: true })
  assert.deepEqual(applyFor("running"), { retry: false, approve: true, cancel: true, pr: false, help: true, copy: true })
})

test("3. stale storage cannot reopen the Technical section", async () => {
  const created = []
  const renderers = Object.create(null)
  const values = {
    "beacon-selected-run": { id: "r-stale" },
    "beacon-sidebar-sections": { ...SIDEBAR_OPEN },
    "beacon-detail-sections": { ...DETAIL_OPEN, technical: true },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({
      active: [makeRun({ ID: "r-stale", TaskID: "T-9", State: "running" })], recent: [],
    }) }) },
    storage: { store: (name, init) => {
      values[name] = { ...init.initial, ...(values[name] ?? {}) }
      return [values[name], async () => {}]
    } },
    ui: {
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel: { open: () => {}, close: () => {} },
    },
    keymap: { layer: () => () => {} },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules(created)[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))
  const detail = renderers["session.panel"]({ name: PANEL_NAME })
  const markerOf = (key) => findItem(detail, (item) => item.sectionKey === key)?.children[0].props.content
  assert.equal(markerOf("technical"), "▶", "stored technical:true is discarded on load")
  assert.equal(markerOf("why"), "▼")
  assert.equal(markerOf("timeline"), "▼")
  assert.equal(markerOf("checks"), "▼")
  dispose()
})

test("4. compact header and spaced live markers", () => {
  const mount = createDetailMount(solidModule([]), deps)
  assert.ok(findText(mount.node, "Beacon"), "the header is the compact word")
  assert.equal(findText(mount.node, "Beacon · Run & Control"), null, "the long title is gone")
  assert.ok(findText(mount.node, "ctrl+g"), "the toggle hint stays")
  assert.ok(findText(mount.node, "✕"), "the close hint stays")

  const line = createLineMount(solidModule([]), { color, maxLines: 1 })
  line.apply([{ marker: "✔", markerSpace: true, name: "Hotfix", status: "MERGED", color: "success" }])
  assert.equal(findText({ children: line.node }, "✔ ")?.props.content, "✔ ", "the marker keeps a trailing space")
})

test("5. summary with no active runs never reports needs work", () => {
  const item = stateBarItem({
    active: [],
    recent: [makeRun({ ID: "r-done", TaskID: "T-9", State: "merged" })],
    metrics: { rebaseCount: 1, fixCount: 2, mergeCount: 37 },
  })
  assert.match(item.summary, /^0 active/, "no active runs is reported explicitly")
  assert.equal(item.summary.includes("needs work"), false, "cumulative metrics do not create a badge")
  assert.match(item.summary, /1 done/, "done comes from the deduplicated runs")
  assert.match(item.legend, /M37/, "the legend still uses the real mergeCount")
})
