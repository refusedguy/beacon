// Behavior tests for daemon alerts, the queue, nudge affordances and transition
// toasts. Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  PANEL_NAME, beaconStatusDefinition, collectTransitionToasts,
  createDetailMount, createSidebarMount, detailModel, normalizeRun,
  shortTaskID, sidebarModel,
} from "./ui.js"
import { NOW, hostModule, makeRun, solidModule, themeStub } from "./ui_test_helpers.js"

const color = (key) => `theme.${key}`
const detailDeps = (onAction = () => {}) => ({
  color, BOLD: 1, setProp: (node, key, value) => { node.props[key] = value },
  onAction, onToggle: () => {}, onClose: () => {},
})
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

async function makeSetup(current, opts = {}) {
  const created = []
  const renderers = Object.create(null)
  const toasts = []
  const controls = []
  const opened = []
  let tick = null
  const rpc = {
    snapshot: async () => (typeof current === "function" ? current() : current),
    control: async (input) => { controls.push(input); return { ok: true } },
    dispatch: async () => ({ started: 1 }),
    events: { on: () => () => {} },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => rpc },
    storage: { store: (name, init) => [{ ...init.initial, ...(opts.storage?.[name] ?? {}) }, async () => {}] },
    renderer: { copyToClipboardOSC52: () => true },
    ui: {
      toast: { show: (item) => toasts.push(item) },
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel: { open: (name) => opened.push(name), close: () => {}, current: () => null },
      ...(opts.ui ?? {}),
    },
    keymap: { layer: () => () => {} },
  }
  const modules = {
    "@opentui/solid": solidModule(created),
    "solid-js": { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 1000, watcherEnabled: false, ...(opts.options ?? {}) },
    import: (specifier) => modules[specifier],
    setInterval: (fn) => { tick = fn; return 1 }, clearInterval: () => {},
    setTimeout: () => 1, clearTimeout: () => {},
  })
  await new Promise((resolve) => setImmediate(resolve))
  return {
    renderers, toasts, controls, opened, created,
    findText: (node, content) => findText(node, content),
    findItem: (node, predicate) => findItem(node, predicate),
    retick: () => { tick?.() },
    dispose,
  }
}

test("daemon dead and stale raise a banner; alive or absent raise none", () => {
  const dead = sidebarModel({ active: [], recent: [], daemon: { state: "dead", error: "heartbeat lost" } }, null, NOW)
  const banner = dead.listItems.find((i) => i.banner)
  assert.equal(banner.name, "⚠ Daemon dead: heartbeat lost")
  assert.equal(banner.color, "error")
  const stale = sidebarModel({ active: [], recent: [], daemon: { state: "stale" } }, null, NOW).listItems.find((i) => i.banner)
  assert.equal(stale.name, "⚠ Daemon stale: ")
  assert.equal(stale.color, "warning")
  assert.equal(sidebarModel({ active: [], recent: [], daemon: { state: "alive" } }, null, NOW).listItems.some((i) => i.banner), false)
  assert.equal(sidebarModel({ active: [], recent: [] }, null, NOW).listItems.some((i) => i.banner), false)
  const errorOnly = sidebarModel({ active: [], recent: [], daemon: { state: "alive", error: "rpc failed" } }, null, NOW)
  assert.ok(errorOnly.listItems.some((i) => i.banner), "an error on an alive daemon is still surfaced")
})

test("a base incident banner is clickable and opens its fixer run", async () => {
  const h = await makeSetup({
    active: [makeRun({ ID: "fixer-1", State: "running" })], recent: [],
    base_incident: { id: "inc-1", base_branch: "main", failed_checks: ["ci"], fixer_run_id: "fixer-1" },
  })
  const sidebar = h.renderers["sidebar.content"]()
  const banner = h.findItem(sidebar, (item) => item.banner)
  assert.ok(banner, "the incident banner is in the sidebar list")
  assert.equal(banner.children[1].props.content, "🔥 main RED (1) · Fixer")
  assert.equal(banner.item.runID, "fixer-1")
  banner.props.onMouseDown()
  assert.deepEqual(h.opened, [PANEL_NAME], "click asks for the detail panel")
  const detail = h.renderers["session.panel"]({ name: PANEL_NAME })
  assert.ok(h.findText(detail, "T-1"), "the fixer run is selected in detail")
  h.dispose()
})

test("queue shows a full title, a compact state line and short clickable dependencies", () => {
  const snap = {
    active: [], recent: [],
    queue: [
      { id: "T-2", title: "Ready task", priority: 9, status: "ready" },
      { id: "T-3", title: "Blocked task", priority: 2, status: "waiting_deps", depends_on: ["T-2"] },
    ],
  }
  const open = sidebarModel(snap, null, NOW, { active: true, queue: true, recent: true })
  const lines = open.listItems.map((i) => [i.marker, i.name, i.status].filter(Boolean).join(" "))
  assert.ok(lines.includes("▼ Queue 2"))
  assert.ok(lines.includes("○ Ready task"), "line one keeps the title")
  assert.ok(lines.includes("ready · P9 · 0 deps"), "line two carries state, priority and count")
  assert.ok(lines.includes("◌ Blocked task"), "a blocked card still shows its title")
  assert.ok(lines.includes("blocked · P2 · 1 dep"))
  assert.ok(lines.includes("↳ T-2"), "the dependency is short and on its own clickable row")
  assert.equal(lines.some((line) => line.includes("blocked by")), false)
  const dep = open.listItems.find((i) => i.kind === "queue-dep")
  assert.equal(dep.action, "card:T-2")
  assert.ok(lines.includes("▶ Dispatch (1 ready)"))
  assert.equal(open.ready, 1)

  const collapsed = sidebarModel(snap, null, NOW, { active: true, queue: false, recent: true })
  assert.equal(collapsed.listItems.some((i) => i.name === "Ready task"), false, "the queue starts collapsed")
  assert.equal(collapsed.listItems.some((i) => i.action === "dispatch"), true, "Dispatch stays available")
})

test("epic and child queue rows carry distinct markers and keep their link", () => {
  const snap = {
    active: [], recent: [],
    queue: [
      { id: "T-EPIC", title: "Harness epic", priority: 9, status: "ready", kind: "epic", depends_on: ["T-EPIC-01"] },
      { id: "T-EPIC-01", title: "Child card", priority: 8, status: "waiting_deps", depends_on: ["T-BLOCK"] },
    ],
  }
  const open = sidebarModel(snap, null, NOW, { active: true, queue: true, recent: true })
  const epic = open.listItems.find((i) => i.kind === "queue-title" && i.cardID === "T-EPIC")
  const child = open.listItems.find((i) => i.kind === "queue-title" && i.cardID === "T-EPIC-01")
  assert.equal(epic.marker, "◆")
  assert.equal(epic.name, "Harness epic")
  assert.equal(epic.status, "EPIC")
  assert.equal(child.marker, "└")
  assert.equal(child.name, "Child card")
  assert.ok(open.listItems.some((i) => i.name === "child · blocked · P8 · 1 dep"))
  assert.equal(open.listItems.find((i) => i.kind === "queue-dep" && i.name === "T-BLOCK").depID, "T-BLOCK")
})

test("child dependencies are not repeated and long IDs keep the readable suffix", () => {
  const snap = {
    active: [], recent: [],
    queue: [
      {
        id: "T-20261124-51f36", title: "Harness epic", status: "ready", priority: 9, kind: "epic",
        depends_on: ["T-20261124-51f36-01", "T-20261124-51f36-02", "T-BLOCK"],
      },
      { id: "T-20261124-51f36-01", title: "Child one", status: "available", priority: 8 },
      { id: "T-20261124-51f36-02", title: "Child two", status: "available", priority: 8 },
    ],
  }
  const open = sidebarModel(snap, null, NOW, { active: true, queue: true, recent: true })
  const deps = open.listItems.filter((i) => i.kind === "queue-dep")
  assert.equal(deps.length, 1, "a child that is also a dependency is drawn once")
  assert.equal(deps[0].depID, "T-BLOCK")
  assert.equal(deps[0].name, "T-BLOCK")
  assert.equal(shortTaskID("T-20261124-51f36-01"), "51f36-01", "the date prefix is dropped, not the ID middle cut")
  assert.equal(shortTaskID("T-2"), "T-2")
  assert.equal(shortTaskID(""), "-")
})

test("detail shows the fixer badge, next step, Session and the new buttons", async () => {
  const h = await makeSetup({
    active: [makeRun({
      ID: "fixer-1", State: "running", IsFixer: true,
      SessionID: "ses-9", PRURL: "https://github.com/o/r/pull/42",
    })], recent: [],
  }, { storage: { "beacon-selected-run": { id: "fixer-1" } } })
  const detail = h.renderers["session.panel"]({ name: PANEL_NAME })
  assert.ok(h.findText(detail, "FIXER"), "an incident fixer carries a badge")
  assert.ok(h.findText(detail, "Running: nudge the worker with guidance"), "the hero gives a next step")
  assert.equal(h.findText(detail, "⧉ PR URL").props.visible, true)
  assert.equal(h.findText(detail, "💬 Help").props.visible, true)
  const technical = h.findItem(detail, (item) => item.sectionKey === "technical")
  technical.props.onMouseDown()
  assert.ok(h.findText(detail, "ses-9"), "Session appears under Technical")
  h.dispose()

  const blocked = await makeSetup(
    { active: [makeRun({ ID: "b-1", State: "closed" })], recent: [] },
    { storage: { "beacon-selected-run": { id: "b-1" } } },
  )
  const blockedDetail = blocked.renderers["session.panel"]({ name: PANEL_NAME })
  assert.ok(blocked.findText(blockedDetail, "Closed: retry to resume"))
  assert.equal(blocked.findText(blockedDetail, "↻ Retry").props.visible, true, "a closed run can retry")
  assert.equal(blocked.findText(blockedDetail, "💬 Help").props.visible, false, "no nudge on a terminal run")
  blocked.dispose()
})

test("transition toasts fire once for merged and blocked and never on the first render", () => {
  const previous = new Map()
  const run = (overrides) => normalizeRun(makeRun(overrides))
  assert.deepEqual(collectTransitionToasts(previous, { active: [run({ ID: "r1", State: "running" })], recent: [] }), [])

  const merged = collectTransitionToasts(previous, {
    active: [run({ ID: "r1", State: "merged", title: "Fix T-1" })], recent: [],
  })
  assert.deepEqual(merged, [{ message: "✔ Merged #42: Fix T-1", variant: "success" }])
  assert.deepEqual(collectTransitionToasts(previous, {
    active: [run({ ID: "r1", State: "merged" })], recent: [],
  }), [], "an unchanged state is silent")

  const blocked = collectTransitionToasts(previous, {
    active: [run({
      ID: "r1", State: "blocked",
      Reason: "workspace: conflict: refusing to remove dirty worktree at /tmp/wt",
    })], recent: [],
  })
  assert.deepEqual(blocked, [{ message: "▲ T-1: dirty worktree", variant: "error" }])

  const closed = collectTransitionToasts(previous, {
    active: [], recent: [run({ ID: "r1", State: "closed", Reason: "pull request closed" })],
  })
  assert.deepEqual(closed, [{ message: "▲ T-1: PR closed", variant: "warning" }])
})

test("a live transition toasts once and an unchanged snapshot is silent", async () => {
  let snapshot = { active: [makeRun({ ID: "r1", State: "running" })], recent: [] }
  const h = await makeSetup(() => snapshot)
  h.renderers["sidebar.content"]()
  assert.equal(h.toasts.length, 0, "the first render is silent")

  snapshot = { active: [makeRun({ ID: "r1", State: "merged", title: "Done" })], recent: [] }
  h.retick()
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(h.toasts.length, 1)
  assert.equal(h.toasts[0].message, "✔ Merged #42: Done")

  h.retick()
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(h.toasts.length, 1, "a repeated snapshot does not repeat the toast")
  h.dispose()
})

test("alert, queue and detail rows work on a host without box children", () => {
  const created = []
  const ui = hostModule(created)
  const sidebar = createSidebarMount(ui, { color, BOLD: 1, onItem: () => {}, hoverBg: "theme.hover" })
  const detail = createDetailMount(ui, detailDeps())
  const open = { active: true, queue: true, recent: true }
  const snapshot = {
    active: [makeRun({ ID: "a", State: "running" })], recent: [],
    queue: [{ id: "T-2", title: "Queued", status: "ready", priority: 1 }],
    daemon: { state: "dead", error: "heartbeat lost" },
    base_incident: { id: "inc-1", base_branch: "main", failed_checks: 1, fixer_run_id: "run-1" },
  }
  assert.doesNotThrow(() => sidebar.apply(sidebarModel(snapshot, null, NOW, open)))
  const contents = created.map((node) => node.props?.content)
  assert.ok(contents.includes("⚠ Daemon dead: heartbeat lost"), "the daemon alert is one text cell")
  assert.ok(contents.includes("🔥 main RED (1) · Fixer"), "the incident banner is one text cell, no gap needed")
  const run = normalizeRun(makeRun({ State: "blocked", IsFixer: true, PRURL: "https://x/pull/42" }))
  assert.doesNotThrow(() => detail.apply(detailModel(run, {}, NOW), run))
  const before = created.length
  assert.doesNotThrow(() => {
    sidebar.apply(sidebarModel({ ...snapshot, daemon: { state: "alive" } }, null, NOW, open))
    const merged = normalizeRun(makeRun({ State: "merged" }))
    detail.apply(detailModel(merged, {}, NOW), merged)
  })
  assert.equal(created.length, before, "refresh changes props only")
})
