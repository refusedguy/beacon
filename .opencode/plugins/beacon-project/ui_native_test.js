// Native-quality UI contract tests from T-20261124-5bff7685.
// Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import {
  ACTION_NAMES, DETAIL_OPEN, SIDEBAR_OPEN, SIDEBAR_TITLE_WIDTH, STATE_BAR_WIDTH,
  beaconStatusDefinition, cardModel, createCardMount, createDetailMount,
  createSidebarMount, detailModel, normalizeRun, sidebarModel, sidebarPresentation,
  stateBarItem, timelineText,
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
function signals() {
  return { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } }
}

test("state bar fills 30 cells with thin glyphs and a compact R C B M legend", () => {
  assert.equal(STATE_BAR_WIDTH, 30)
  const item = stateBarItem({ active: [], recent: [], metrics: { merge_count: 51 } })
  assert.equal(item.bar.length, 30)
  assert.equal(item.segments.at(-1).text, "─".repeat(30))
  assert.equal(item.legend, "R0 C0 B0 M51")
})

test("Beacon header sits at column 0 and every Recent row is unbolded", () => {
  const sidebar = createSidebarMount(solidModule([]), {
    color, BOLD: 1, onItem: () => {}, hoverBg: "theme.hover",
  })
  sidebar.apply(sidebarModel({
    active: [makeRun({ ID: "a", State: "running", title: "Active run" })],
    recent: [makeRun({ ID: "r", TaskID: "T-DONE", State: "merged", title: "Done run" })],
  }, null, NOW))
  const header = sidebar.node.children[0]
  assert.equal(header.props.flexDirection, "row")
  assert.equal(header.props.justifyContent, "space-between")
  assert.equal(header.props.paddingLeft, undefined, "no left offset")
  assert.equal(header.props.marginLeft, undefined, "no left margin")
  assert.equal(sidebar.headerName.props.content, "Beacon")
  assert.equal(sidebar.headerSummary.props.fg, "theme.muted")
  const recentRows = sidebar.node.children.filter((box) => box.item?.recent === true)
  assert.ok(recentRows.length >= 2, "both recent lines are present")
  for (const row of recentRows) {
    assert.equal(row.children[1].props.attributes, 0, "recent title has no bold attribute")
    assert.equal(row.children[1].props.fg, "theme.muted", "recent copy stays calm")
  }
})

test("hero card, Why/Blocker card and each button box carry padding and background", () => {
  const mount = createDetailMount(solidModule([]), detailDeps())
  const run = normalizeRun(makeRun({ State: "blocked", Reason: "dirty worktree" }))
  mount.apply(detailModel(run, {}, NOW), run)

  const hero = mount.heroCard
  assert.equal(hero.props.border, true)
  assert.equal(hero.props.borderStyle, "rounded")
  assert.equal(hero.props.marginTop, 1)
  assert.equal(hero.props.backgroundColor, "theme.panel")
  for (const key of ["paddingLeft", "paddingRight", "paddingTop", "paddingBottom"]) {
    assert.equal(hero.props[key], 1, `hero has ${key}`)
  }

  const reasonRows = mount.body.node.filter((box) => box.item?.kind === "reason")
  assert.equal(reasonRows.length, 1, "the open blocker is its own card")
  const reason = reasonRows[0]
  assert.equal(reason.props.border, true)
  assert.equal(reason.props.borderStyle, "rounded")
  assert.equal(reason.props.borderColor, "theme.error", "a blocked reason uses the error border")
  assert.equal(reason.props.backgroundColor, "theme.raised")
  assert.equal(reason.props.paddingLeft, 1)
  assert.equal(reason.props.paddingRight, 1)
  const reasonText = reason.children[1]
  assert.equal(reasonText.props.content, "dirty worktree")
  assert.equal(reasonText.props.wrapMode, "word")
  assert.equal(reasonText.props.truncate, false)
  assert.equal(reason.children[0].props.content, "", "no lone arrow marker on the reason")

  for (const name of ACTION_NAMES) {
    const box = mount.buttonBoxes[name]
    assert.equal(box.props.paddingLeft, 1, `${name} has left padding`)
    assert.equal(box.props.paddingRight, 1, `${name} has right padding`)
    assert.ok(box.props.backgroundColor, `${name} has a themed background`)
    assert.equal(typeof box.props.onMouseOver, "function", `${name} has hover in`)
    assert.equal(typeof box.props.onMouseOut, "function", `${name} has hover out`)
    assert.equal(typeof box.props.onMouseUp, "function", `${name} is clickable`)
  }
})

test("hero buttons keep an explicit gap and Control hint is spaced", () => {
  const mount = createDetailMount(solidModule([]), detailDeps())
  assert.equal(mount.buttonBoxes.retry.props.marginRight, 1)
  assert.equal(mount.buttonBoxes.approve.props.marginRight, 1)
  assert.equal(mount.buttonBoxes.cancel.props.marginRight, 1)
  assert.equal(mount.buttonBoxes.copy.props.marginRight, 0)
  const controls = findText(mount.node, "ctrl+g")
  assert.equal(controls.props.paddingLeft, 1)
  assert.equal(controls.props.paddingRight, 1)
  assert.ok(findText(mount.node, " "), "an explicit spacer separates the hint from ✕")
  assert.equal(findText(mount.node, "esc"), null)
})

test("sidebar titles clip from the end and detail has no fixed value width", () => {
  const long = "Make worktree registration refresh reliable at every exit"
  const [item] = sidebarPresentation([{
    marker: "●", name: long, status: "RUN", color: "success", bold: true, runID: "run-1",
  }])
  assert.equal(item.name.length, SIDEBAR_TITLE_WIDTH)
  assert.ok(item.name.endsWith("…"))
  assert.ok(long.startsWith(item.name.slice(0, -1)), "the clipping keeps the beginning")
  const src = ["ui_view.js", "ui_detail_view.js", "ui_sidebar.js"]
    .map((file) => readFileSync(new URL(`./${file}`, import.meta.url), "utf8")).join("\n")
  assert.equal(/VALUE_WIDTH/.test(src), false, "no VALUE_WIDTH=22 in detail")
  const title = "A long detail title that wraps ".repeat(4).trim()
  assert.equal(detailModel(normalizeRun(makeRun({ title })), {}, NOW).title, title)
})

test("Timeline uses human copy and Technical keys are aligned", () => {
  assert.equal(timelineText({ kind: "created" }), "Run created")
  assert.equal(timelineText({ kind: "spawn" }), "Worker started")
  assert.equal(timelineText({ kind: "watch_pr", pr: 2362 }), "PR #2362 opened")
  assert.equal(timelineText({ kind: "merge" }), "Squash merged to main")
  const run = normalizeRun(makeRun({
    State: "running",
    Timeline: [{ at: "2026-11-23T10:01:00Z", kind: "created", state: "running" }],
    Decisions: [{ at: "2026-11-23T10:06:00Z", kind: "reconcile.decision", verdict: "merge" }],
  }))
  const rows = detailModel(run, {}, NOW, { ...DETAIL_OPEN, technical: true }).rows
  assert.ok(rows.some((r) => r.name === "Run created"))
  for (const label of ["Run ID", "Head SHA", "Attempts"]) {
    assert.ok(rows.some((r) => r.marker === label.padEnd(10, " ")), `${label} is padEnd(10)`)
  }
})

test("stale storage keeps Technical collapsed and Copy ID shows on a merged run", async () => {
  const renderers = Object.create(null)
  const values = {
    "beacon-selected-run": { id: "merged-1" },
    "beacon-sidebar-sections": { ...SIDEBAR_OPEN },
    "beacon-detail-sections": { ...DETAIL_OPEN, technical: true },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({
      active: [], recent: [makeRun({ ID: "merged-1", State: "merged", title: "Merged run" })],
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
  const modules = {
    "@opentui/solid": solidModule(), "solid-js": signals(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))
  const detail = renderers["session.panel"]({ name: "beacon.detail" })
  const tech = findItem(detail, (item) => item.sectionKey === "technical")
  assert.equal(tech.children[0].props.content, "▶", "stored technical:true is discarded")
  const copy = findText(detail, "⧉ Copy ID")
  assert.ok(copy, "the merged run still shows Copy ID")
  assert.equal(copy.props.visible, true)
  dispose()
})

test("Dispatch and Control still reach the RPC through the real setup", async () => {
  const renderers = Object.create(null)
  const controls = []
  const dispatches = []
  const toasts = []
  const rpc = {
    snapshot: async () => ({
      active: [makeRun({ State: "blocked" })], recent: [],
      queue: [{ id: "T-2", title: "Queued task", status: "ready", priority: 1 }],
    }),
    control: async (input) => { controls.push(input); return { ok: true } },
    dispatch: async (input) => { dispatches.push(input); return { started: 1 } },
    events: { on: () => () => {} },
  }
  const ctx = {
    theme: themeStub(), client: { rpc: () => rpc },
    storage: { store: () => [{ id: "run-1" }, async () => {}] },
    ui: {
      toast: { show: (options) => toasts.push(options) },
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel: { open: () => {}, close: () => {} },
    },
    keymap: { layer: () => () => {} },
  }
  const modules = {
    "@opentui/solid": solidModule(), "solid-js": signals(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
    cardDeps: {
      existsSync: (p) => p.endsWith("/docs/tasks/T-2.md"),
      readFileSync: () => "---\nid: T-2\ntitle: Queued task\nstatus: available\n---\n",
    },
  })
  await new Promise((resolve) => setImmediate(resolve))
  const detail = renderers["session.panel"]({ name: "beacon.detail" })
  const sidebar = renderers["sidebar.content"]()
  findText(detail, "↻ Retry").props.onMouseUp()
  findItem(sidebar, (item) => item.action === "dispatch").props.onMouseDown()
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(controls, [{ action: "retry", runID: "run-1", sha: "abcdef1234567890" }])
  assert.deepEqual(dispatches, [{ n: 1 }])
  assert.ok(toasts.length >= 2, "actions report through a toast")
  dispose()
})

test("card controls wrap at narrow widths while status cells yield to titles", () => {
  const mount = createCardMount(solidModule([]), {
    color, BOLD: 1, onCopy: () => {}, onClose: () => {}, onBack: () => {}, onItem: () => {},
  })
  mount.apply(cardModel({
    id: "T-42", title: "A very long card title", status: "ready", priority: 7,
    depends: "T-40", body: "Goal: work.",
  }, [{ id: "T-40", title: "Dependency", status: "available" }], {}))
  const controls = mount.node.children[0].children[1]
  assert.equal(controls.props.flexWrap, "wrap")
  assert.equal(controls.props.rowGap, 1)
  assert.equal(controls.children.length, 5, "Back, ctrl+g, read-only, gap and ✕ stay mounted")
  assert.ok(controls.children.every((node) => node.props.flexShrink === 0))
  const meta = mount.body.node.find((box) => box.item?.kind === "card-meta" && box.item.marker.trim() === "ID")
  const [marker, name, status] = meta.children
  assert.equal(marker.props.content, "ID        ", "the padded label column stays aligned")
  assert.equal(name.props.wrapMode, "word", "long values wrap")
  assert.equal(name.props.truncate, false)
  assert.equal(status.props.flexShrink, 1, "the status cell yields width")
  assert.equal(status.props.minWidth, 0)
  assert.equal(status.props.truncate, true)
})

test("an epic chevron toggles the queue and persists through the real setup", async () => {
  const renderers = Object.create(null)
  const saved = []
  const epicValue = {}
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => ({ snapshot: async () => ({
      active: [], recent: [],
      queue: [
        { id: "T-EPIC", title: "Harness epic", status: "ready", priority: 9 },
        { id: "T-EPIC-01", title: "Child card", status: "available", priority: 8 },
      ],
    }) }) },
    storage: { store: (name, init) => {
      const value = name === "beacon-sidebar-sections" ? { ...init.initial, queue: true } : { ...init.initial }
      if (name === "beacon-epic-sections") {
        return [epicValue, async (fn) => { fn(epicValue); saved.push({ ...epicValue }) }]
      }
      return [value, async () => {}]
    } },
    ui: {
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel: { open: () => {}, close: () => {} },
    },
    keymap: { layer: () => () => {} },
  }
  const modules = {
    "@opentui/solid": solidModule(), "solid-js": signals(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false }, import: (specifier) => modules[specifier],
    cardDeps: { existsSync: () => true, readFileSync: () => "---\nid: T-EPIC\nstatus: ready\n---\n" },
  })
  await new Promise((resolve) => setImmediate(resolve))
  const sidebar = renderers["sidebar.content"]()
  const chevron = findItem(sidebar, (item) => item.epicKey === "T-EPIC" && item.kind === "queue-title")
  assert.ok(chevron, "the epic title carries the toggle")
  assert.equal(chevron.children[0].props.content.trim(), "▶", "epics start collapsed")
  chevron.props.onMouseDown()
  assert.equal(chevron.children[0].props.content.trim(), "▼", "the chevron flips on click")
  assert.equal(saved.at(-1)["T-EPIC"], true, "the state reaches storage")
  dispose()
})

test("both mounts are safe on a host without box.children and never create nodes on refresh", () => {
  const created = []
  const ui = hostModule(created)
  const sidebar = createSidebarMount(ui, { color, BOLD: 1, onItem: () => {}, hoverBg: "theme.hover" })
  const detail = createDetailMount(ui, detailDeps())
  const snapshot = { active: [makeRun({ State: "running" })], recent: [] }
  assert.doesNotThrow(() => sidebar.apply(sidebarModel(snapshot, null, NOW)))
  const run = normalizeRun(makeRun({ State: "running" }))
  assert.doesNotThrow(() => detail.apply(detailModel(run, {}, NOW), run))
  const before = created.length
  assert.doesNotThrow(() => {
    sidebar.apply(sidebarModel({ active: [makeRun({ State: "blocked" })], recent: [] }, null, NOW))
    const merged = normalizeRun(makeRun({ State: "merged" }))
    detail.apply(detailModel(merged, {}, NOW), merged)
  })
  assert.equal(created.length, before, "refresh only changes props")
  const spaced = created.filter((node) => node.props?.content === "● " || node.props?.content === "▲ ")
  assert.ok(spaced.length > 0, "markers carry a trailing space because box gap is unreliable")
})
