// Run: node --test .opencode/plugins/beacon-project/ui_setup_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { NOW, makeRun, solidModule, themeStub } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const { EMPTY_SNAPSHOT, beaconStatusDefinition, createRowMount, snapshotLineItems, stateBarItem } = ui

function barNode(container) {
  return container.children.find((child) =>
    child.children?.some((cell) => /[━─]/.test(String(cell.props?.content ?? ""))))
}
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
function ctxFor(rpc, store) {
  return {
    theme: themeStub(),
    client: { rpc: () => rpc },
    storage: store ?? { store: () => [{ id: "" }, async () => {}] },
    ui: { slot: () => () => {}, panel: { open: () => {} } },
    keymap: { layer: () => () => {} },
  }
}

test("state bar builds a proportional one-row visualization", () => {
  const item = stateBarItem({
    active: [
      makeRun({ ID: "a", State: "running" }), makeRun({ ID: "b", State: "running" }),
      makeRun({ ID: "c", State: "waiting_ci" }), makeRun({ ID: "d", State: "blocked" }),
    ],
    recent: [makeRun({ ID: "e", TaskID: "T-E", State: "merged" })],
  })
  assert.deepEqual(item.counts, { running: 2, waiting_ci: 1, blocked: 1, merged: 1 })
  assert.equal(item.filled, 5)
  assert.equal((item.bar.match(/━/g) ?? []).length, 5)
  assert.equal((item.bar.match(/─/g) ?? []).length, ui.STATE_BAR_WIDTH - 5)
  assert.equal(item.fg, "error")
})

test("sidebar bar updates on refresh without allocating nodes", async () => {
  let current = { active: [makeRun({ ID: "r-1", State: "running" })], recent: [] }
  const created = []
  const renderers = Object.create(null)
  let tick = null
  const ctx = ctxFor({ snapshot: async () => current })
  ctx.ui.slot = ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 1000, watcherEnabled: false },
    import: (specifier) => modules(created)[specifier],
    setInterval: (fn) => { tick = fn; return 1 }, clearInterval: () => {},
    setTimeout: () => 1, clearTimeout: () => {},
  })
  await new Promise((resolve) => setImmediate(resolve))
  const sidebar = renderers["sidebar.content"]()
  const bar = barNode(sidebar)
  assert.ok(bar, "the sidebar renders a segmented state bar")
  assert.ok(bar.children.some((cell) => cell.props.fg === "success"), "running uses the success tone")
  const before = created.length
  current = { active: [makeRun({ ID: "r-1", State: "blocked" })], recent: [] }
  tick()
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(created.length, before, "refresh must not create nodes")
  assert.equal(barNode(sidebar), bar, "the bar node is reused")
  assert.ok(bar.children.some((cell) => cell.props.fg === "error"), "refresh updates the bar color")
  assert.ok(sidebar.children.some((child) => child.item?.status === "1 blocked"), "the header summary updates")
  dispose()
})

test("row mount reuses nodes across refreshes and keeps the click handler", () => {
  const created = []
  const uiApi = {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push(node)
      return node
    },
    insert: (parent, child) => parent.children.push(child),
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const picked = []
  const mount = createRowMount(uiApi, {
    color: (key) => `theme.${key}`, BOLD: 1, onRow: (item) => picked.push(item), hoverBg: "theme.hover",
    maxRows: 8,
  })
  const container = { children: [], tag: "box" }
  let items = snapshotLineItems({ active: [makeRun({ title: "First" })], recent: [] }, null, NOW)
  const apply = mount(container, () => items, 12)
  const afterFirst = created.length
  const row = container.children.find((box) => box.item?.runID === "run-1")
  const handler = row.props.onMouseDown

  items = snapshotLineItems({ active: [makeRun({ State: "blocked", title: "Second" })], recent: [] }, null, NOW)
  apply()
  assert.equal(created.length, afterFirst, "refresh must not create nodes")
  assert.equal(container.children.find((box) => box.item?.runID === "run-1"), row, "row identity is stable")
  assert.equal(row.props.onMouseDown, handler, "click handler survives")
  assert.equal(row.children[1].props.content, "Second")
  assert.equal(row.children[1].props.fg, "theme.error", "the name follows the state color")
  assert.equal(row.children[0].props.content, "▲ ", "a run marker keeps a trailing space in the live cell")
  assert.equal(row.children[0].props.fg, "theme.error")
  handler()
  assert.equal(picked.length, 1)
  assert.equal(picked[0].runID, "run-1")

  const beforeHide = container.children.filter((box) => box.props.visible === true).length
  items = snapshotLineItems(EMPTY_SNAPSHOT, null, NOW)
  apply()
  assert.equal(created.length, afterFirst, "hiding rows must not create nodes")
  assert.ok(container.children.filter((box) => box.props.visible === true).length < beforeHide)
})

test("setup defers every createElement to a slot render callback", async () => {
  const created = []
  const renderers = Object.create(null)
  const ctx = ctxFor({ snapshot: async () => ({ active: [], recent: [] }) })
  ctx.ui.slot = ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } }
  const dispose = await beaconStatusDefinition.setup(ctx, { import: (specifier) => modules(created)[specifier] })
  assert.equal(created.length, 0, "setup must not call createElement before any render callback")
  for (const name of ["sidebar.content", "home.footer.status", "session.panel", "app"]) {
    assert.equal(typeof renderers[name], "function", `registers ${name}`)
  }
  const sidebar = renderers["sidebar.content"]()
  assert.ok(sidebar, "sidebar render returns a node")
  assert.ok(created.length > 0, "first render callback creates the nodes")
  const before = created.length
  assert.equal(renderers["sidebar.content"](), sidebar, "sidebar node is reused")
  assert.equal(created.length, before, "repeat renders create no new nodes")
  const footer = renderers["home.footer.status"]()
  assert.notEqual(footer, sidebar, "footer has its own node")
  const detail = renderers["session.panel"]({ name: ui.PANEL_NAME })
  assert.ok(detail, "panel render returns the detail node")
  assert.equal(renderers["session.panel"]({ name: "other" }), null, "other panels render nothing")
  assert.equal(renderers["session.panel"]({ name: ui.PANEL_NAME }), detail, "detail node is reused")
  dispose()
  assert.equal(Object.keys(renderers).length, 0, "dispose removes every slot")
})

test("a row count change during refresh creates no nodes outside a render callback", async () => {
  let current = { active: [], recent: [] }
  const created = []
  let renderActive = false
  const uiApi = {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push({ node, renderActive })
      return node
    },
    insert: (parent, child) => parent.children.push(child),
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const renderers = Object.create(null)
  let tick = null
  const ctx = ctxFor({ snapshot: async () => current, events: { on: () => () => {} } })
  ctx.ui.slot = ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    import: (specifier) => ({ ...modules(), "@opentui/solid": uiApi })[specifier],
    setInterval: (fn) => { tick = fn; return 1 }, clearInterval: () => {},
    setTimeout: () => 1, clearTimeout: () => {},
  })
  assert.equal(created.length, 0, "setup creates nothing before a render callback")
  assert.equal(typeof tick, "function", "the refresh timer is installed")

  renderActive = true
  const sidebar = renderers["sidebar.content"]()
  renderActive = false
  const afterEnsure = created.length
  assert.ok(afterEnsure > 0, "the first render callback allocates the row pool")
  assert.ok(created.every((call) => call.renderActive), "every node was created inside a render callback")

  const beforeVisible = sidebar.children.filter((child) => child.item?.runID).length
  current = {
    active: [
      makeRun({ ID: "run-a", TaskID: "T-A", title: "First" }),
      makeRun({ ID: "run-b", TaskID: "T-B", title: "Second" }),
      makeRun({ ID: "run-c", TaskID: "T-C", title: "Third" }),
    ],
    recent: [],
  }
  tick()
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(created.length, afterEnsure, "refresh with more rows must not create nodes")
  assert.ok(created.every((call) => call.renderActive), "no node was created outside a render callback")
  const afterVisible = sidebar.children.filter((child) => child.item?.runID).length
  assert.ok(afterVisible > beforeVisible, "the panel shows the new rows")
  dispose()
})

test("stale waits for two failures, clears on success and skips stale toasts", async () => {
  const good = (state) => ({ stdout: JSON.stringify({ active: [makeRun({ ID: "r-1", State: state })], recent: [] }) })
  const outcomes = [
    () => good("running"),
    () => { throw new Error("Error: bad flag\nUsage:\n  beacon --help") },
    () => { throw new Error("Error: bad flag\nUsage:\n  beacon --help") },
    () => good("blocked"),
    () => good("merged"),
  ]
  let attempt = 0
  const cliDeps = {
    command: () => ["/bin/beacon"],
    existsSync: () => false,
    execFile: async () => outcomes[Math.min(attempt++, outcomes.length - 1)](),
  }
  const toasts = []
  const renderers = Object.create(null)
  let tick = null
  const ctx = ctxFor(null)
  ctx.ui.toast = { show: (toast) => toasts.push(toast) }
  ctx.ui.slot = ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 1000, watcherEnabled: false },
    import: (specifier) => modules([])[specifier],
    cardDeps: cliDeps,
    setInterval: (fn) => { tick = fn; return 1 }, clearInterval: () => {},
    setTimeout: () => 1, clearTimeout: () => {},
  })
  const flush = () => new Promise((resolve) => setImmediate(resolve))
  await flush()
  const sidebar = renderers["sidebar.content"]()
  const staleLine = () => sidebar.children.find((child) =>
    child.item?.name?.startsWith("[stale]") && child.props.visible)?.children[1].props.content
  const runState = () => sidebar.children.find((child) => child.item?.runID === "r-1")?.item.status

  assert.equal(attempt, 1)
  assert.equal(runState(), "RUN")
  assert.equal(staleLine(), undefined, "one failure keeps the good snapshot")
  tick()
  await flush()
  assert.equal(runState(), "RUN", "the previous snapshot stays visible")
  assert.equal(staleLine(), undefined, "one failure shows no stale line")
  tick()
  await flush()
  assert.equal(staleLine(), "[stale] beacon status temporarily unavailable")
  assert.equal(runState(), "RUN")
  tick()
  await flush()
  assert.equal(runState(), "BLK")
  assert.equal(staleLine(), undefined, "the first success clears stale")
  assert.equal(toasts.length, 0, "a snapshot taken while stale fires no toast")
  tick()
  await flush()
  assert.equal(runState(), "MERGED")
  assert.equal(toasts.length, 1, "toasts resume after recovery")
  assert.match(toasts[0].message, /Merged #42/)
  dispose()
})

test("a late ctx.location fills the queue without a manual reload", async () => {
  const root = "/work/project"
  const files = {
    [`${root}/docs/tasks/T-A.md`]: "---\ntitle: Alpha\nstatus: todo\n---\n",
    [`${root}/docs/tasks/T-B.md`]: "---\ntitle: Beta\nstatus: todo\n---\n",
  }
  const calls = []
  const execFile = async (program, args, opts) => {
    calls.push(opts)
    const queue = opts.cwd === root ? [{ id: "T-A" }, { id: "T-B" }] : []
    return { stdout: JSON.stringify({ active: [], recent: [], queue }) }
  }
  const ctx = ctxFor(null)
  ctx.client = {}
  const renderers = Object.create(null)
  let tick = null
  ctx.ui.slot = ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 1000, watcherEnabled: false },
    import: (specifier) => modules([])[specifier],
    cardDeps: {
      command: () => ["/bin/beacon"], execFile,
      existsSync: (p) => Object.hasOwn(files, p), readFileSync: (p) => files[p],
    },
    setInterval: (fn) => { tick = fn; return 1 }, clearInterval: () => {},
    setTimeout: () => 1, clearTimeout: () => {},
  })
  const flush = () => new Promise((resolve) => setImmediate(resolve))
  await flush()
  const queueCount = () => renderers["sidebar.content"]().children.find((c) => c.item?.sectionKey === "queue")?.item.status
  assert.equal(queueCount(), "0", "an empty snapshot from a cwd guess is not cached")
  assert.equal(calls.length, 1, "the CLI still runs with the transient cwd root")

  ctx.location = { project: { canonical: root } }
  tick()
  await flush()
  assert.equal(queueCount(), "2", "the queue appears once the canonical root is known")
  assert.equal(calls[1].cwd, root)
  assert.equal(calls[1].env.BEACON_PROJECT_ROOT, root)
  dispose()
})
