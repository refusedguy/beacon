// Behavior tests for the false RED banner, inspectable Queue cards, stale
// card filtering and the three card clipboard buttons.
// Run: node --test .opencode/plugins/beacon-project/ui_card_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  CARD_PANEL_NAME, PANEL_NAME, baseIncidentItem, beaconStatusDefinition,
  createStatusSource, fixPrompt, readCard, sidebarModel,
} from "./ui.js"
import { NOW, makeRun, solidModule, themeStub } from "./ui_test_helpers.js"

const CARD = `---
id: T-42
title: A very long human readable card title
status: available
priority: 7
scope:
  - pkg/core/run.go
  - pkg/core/run_test.go
depends:
  - T-40
---
Goal: a short goal.

Context: a fact.

Steps: do the work.
`

const cardDeps = {
  existsSync: (path) => path.endsWith("/docs/tasks/T-42.md"),
  readFileSync: () => CARD,
}

function findItem(node, predicate) {
  if (node.item && predicate(node.item)) return node
  for (const child of node.children ?? []) {
    const found = findItem(child, predicate)
    if (found) return found
  }
  return null
}

function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const found = findText(child, content)
    if (found) return found
  }
  return null
}

function texts(node, out = []) {
  if (typeof node.props?.content === "string" && node.props.content) out.push(node.props.content)
  for (const child of node.children ?? []) texts(child, out)
  return out
}

async function setup({ queue = [], extra = {}, files = cardDeps, copied = [], copyResult = true, onLayer } = {}) {
  const created = []
  const renderers = Object.create(null)
  const toasts = []
  const opened = []
  const closed = []
  const rpc = {
    snapshot: async () => ({ active: [], recent: [], queue, ...extra }),
    control: async () => ({ ok: true }),
    dispatch: async () => ({ started: 1 }),
    events: { on: () => () => {} },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => rpc },
    storage: {
      store: (name, init) => [
        name === "beacon-sidebar-sections" ? { ...init.initial, queue: true } : { ...init.initial },
        async () => {},
      ],
    },
    renderer: { copyToClipboardOSC52: (text) => { copied.push(text); return copyResult } },
    ui: {
      toast: { show: (item) => toasts.push(item) },
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel: { open: (name) => opened.push(name), close: () => closed.push(true) },
    },
    keymap: { layer: (fn) => { const value = fn(); onLayer?.(value); return () => {} } },
  }
  const modules = {
    "@opentui/solid": solidModule(created),
    "solid-js": { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    root: "/p",
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
    cardDeps: files,
  })
  await new Promise((resolve) => setImmediate(resolve))
  return { renderers, toasts, opened, closed, created, copied, dispose }
}

test("a stable zero-value base incident raises no banner", () => {
  const zero = { id: "", base_branch: "", failed_checks: [], fixer_run_id: "", attempts: 0 }
  assert.equal(baseIncidentItem(zero), null)
  const view = sidebarModel({ active: [], recent: [], base_incident: zero }, null, NOW)
  assert.equal(view.listItems.some((item) => item.banner), false)
})

test("a real incident is still a clickable RED banner", async () => {
  const h = await setup({
    extra: {
      active: [makeRun({ ID: "fixer-1", State: "running" })],
      base_incident: { id: "inc-1", base_branch: "main", failed_checks: ["ci"], fixer_run_id: "fixer-1" },
    },
  })
  const sidebar = h.renderers["sidebar.content"]()
  const banner = findItem(sidebar, (item) => item.banner)
  assert.ok(banner, "the incident banner is rendered")
  assert.equal(banner.item.runID, "fixer-1")
  banner.props.onMouseDown()
  assert.deepEqual(h.opened, [PANEL_NAME])
  const detail = h.renderers["session.panel"]({ name: PANEL_NAME })
  assert.ok(texts(detail).includes("T-1"), "the fixer run is selected")
  h.dispose()
})

test("clicking a Queue row opens the full read-only card", async () => {
  const h = await setup({ queue: [{ id: "T-42", title: "Queued task", status: "ready", priority: 7 }] })
  const sidebar = h.renderers["sidebar.content"]()
  const row = findItem(sidebar, (item) => item.action === "card:T-42")
  assert.ok(row, "queue rows carry a card action")
  row.props.onMouseDown()
  assert.deepEqual(h.opened, [CARD_PANEL_NAME])
  const card = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  const content = texts(card)
  for (const expected of [
    "A very long human readable card title", "T-42", "7", "available",
    "T-40", "docs/tasks/T-42.md", "Goal: a short goal.", "Steps: do the work.",
  ]) {
    assert.ok(content.includes(expected), `card view missing ${expected}`)
  }
  assert.equal(content.some((line) => line.startsWith("/p/")), false, "no absolute path in the card")
  h.dispose()

  const other = await setup({ queue: [{ id: "T-42", status: "ready" }] })
  assert.equal(other.renderers["session.panel"]({ name: "some.other.panel" }), null)
  other.dispose()
})

test("card body lines are word-wrapped, unbounded and capped at 80", async () => {
  const body = Array.from({ length: 90 }, (_, i) => `body line ${i}`).join("\n")
  const many = { ...cardDeps, readFileSync: () => CARD.replace("Goal: a short goal.", body) }
  const card = readCard("/p", "T-42", many)
  const modelRows = card.body.split(/\r?\n/).filter((line) => line.startsWith("body line"))
  assert.equal(modelRows.length, 80, "readCard keeps only the first 80 body lines")

  const h = await setup({ queue: [{ id: "T-42", status: "ready" }] })
  const sidebar = h.renderers["sidebar.content"]()
  findItem(sidebar, (item) => item.action === "card:T-42").props.onMouseDown()
  const panel = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  const bodyRow = findItem(panel, (item) => item.kind === "card-body")
  assert.ok(bodyRow, "the body is rendered as pooled rows")
  const nameCell = bodyRow.children[1]
  assert.equal(nameCell.props.wrapMode, "word")
  assert.equal(nameCell.props.truncate, false)
  h.dispose()
})

test("stale, done, dropped, missing and unreadable queue cards are filtered", async () => {
  const files = {
    "/p/docs/tasks/T-1.md": "---\nid: T-1\nstatus: available\n---\n",
    "/p/docs/tasks/T-2.md": "---\nid: T-2\nstatus: done\n---\n",
    "/p/docs/tasks/T-3.md": "---\nid: T-3\nstatus: dropped\n---\n",
    "/p/docs/tasks/T-4.md": "---\nid: T-4\nstatus: ready\n---\n",
  }
  const deps = {
    existsSync: (path) => Object.hasOwn(files, path),
    readFileSync: (path) => {
      if (path.endsWith("T-4.md")) throw new Error("EACCES")
      return files[path]
    },
  }
  const ids = ["T-1", "T-2", "T-3", "T-4", "T-9"]
  const rpc = { snapshot: async () => ({ active: [], recent: [], queue: ids.map((id) => ({ id, title: id })) }) }
  const source = createStatusSource({ client: { rpc: () => rpc } }, { root: "/p", deps })
  const snap = await source.snapshot()
  assert.deepEqual(snap.queue.map((item) => item.id), ["T-1"])
})

test("card copy buttons write the exact text and report honest results", async () => {
  const copied = []
  const h = await setup({ queue: [{ id: "T-42", status: "ready" }], copied })
  const sidebar = h.renderers["sidebar.content"]()
  findItem(sidebar, (item) => item.action === "card:T-42").props.onMouseDown()
  const panel = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  const card = readCard("/p", "T-42", cardDeps)

  findText(panel, "⧉ Copy ID").props.onMouseUp()
  assert.equal(copied[0], "T-42")
  assert.equal(h.toasts.at(-1).variant, "success")

  findText(panel, "⧉ Copy path").props.onMouseUp()
  assert.equal(copied[1], "docs/tasks/T-42.md")
  assert.equal(copied[1], card.relativePath)
  assert.match(h.toasts.at(-1).message, /^Copied path /)

  findText(panel, "⧉ Copy fix prompt").props.onMouseUp()
  assert.equal(copied[2], fixPrompt(card))
  assert.match(copied[2], /^Fix T-42: A very long human readable card title in .*\. Scope: .*\. Depends: T-40\.$/)
  assert.equal(h.toasts.at(-1).variant, "success")

  const refused = await setup({ queue: [{ id: "T-42", status: "ready" }], copyResult: false })
  const bar = refused.renderers["sidebar.content"]()
  findItem(bar, (item) => item.action === "card:T-42").props.onMouseDown()
  const node = refused.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  findText(node, "⧉ Copy ID").props.onMouseUp()
  assert.equal(refused.toasts.at(-1).message, "Clipboard copy was refused")
  assert.equal(refused.toasts.at(-1).variant, "warning")
  h.dispose()
  refused.dispose()
})

test("the card header offers the detail panel controls and both close the panel", async () => {
  const h = await setup({ queue: [{ id: "T-42", status: "ready" }] })
  const sidebar = h.renderers["sidebar.content"]()
  findItem(sidebar, (item) => item.action === "card:T-42").props.onMouseDown()
  const panel = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  assert.ok(findText(panel, "ctrl+g"), "the ctrl+g hint is visible on the card")
  assert.ok(findText(panel, "read-only"), "read-only stays visible")
  findText(panel, "ctrl+g").props.onMouseUp()
  findText(panel, "✕").props.onMouseUp()
  assert.equal(h.closed.length, 2, "both header controls close the panel")
  h.dispose()
})

const DEP_CARD = "---\nid: T-40\ntitle: Dependency card\nstatus: available\npriority: 5\n---\nGoal: dependency.\n"
const twoFiles = {
  existsSync: (path) => path.endsWith("/docs/tasks/T-42.md") || path.endsWith("/docs/tasks/T-40.md"),
  readFileSync: (path) => path.endsWith("T-40.md") ? DEP_CARD : CARD,
}

test("clicking a card dependency opens it and back restores the previous card", async () => {
  let layer
  const h = await setup({
    queue: [{ id: "T-42", title: "Queued task", status: "ready" }],
    files: twoFiles, onLayer: (value) => { layer = value },
  })
  const sidebar = h.renderers["sidebar.content"]()
  findItem(sidebar, (item) => item.action === "card:T-42" && item.kind === "queue-title").props.onMouseDown()
  const panel = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  assert.ok(texts(panel).includes("A very long human readable card title"))
  const dep = findItem(panel, (item) => item.kind === "card-dep" && item.action === "card:T-40")
  assert.ok(dep, "the dependency row is a navigation link")
  dep.props.onMouseDown()
  assert.deepEqual(h.opened, [CARD_PANEL_NAME, CARD_PANEL_NAME])
  assert.ok(texts(panel).includes("Dependency card"), "the target card opens in the same panel")
  findText(panel, "← Back").props.onMouseUp()
  assert.ok(texts(panel).includes("A very long human readable card title"), "back restores the earlier card")

  const backCommand = layer.commands.find((command) => command.id === "beacon.card.back")
  assert.equal(backCommand.bind, "ctrl+b")
  findItem(panel, (item) => item.kind === "card-dep" && item.action === "card:T-40").props.onMouseDown()
  backCommand.run()
  assert.ok(texts(panel).includes("A very long human readable card title"), "the shortcut goes back too")
  assert.equal(layer.commands.find((command) => command.id === "beacon.run.detail").bind, "ctrl+g")
  h.dispose()
})

const EPIC_CARD = "---\nid: T-EPIC\ntitle: Harness epic\nstatus: available\npriority: 9\nkind: epic\ndepends:\n  - T-EPIC-01\n---\nGoal: epic.\n"
const CHILD_CARD = "---\nid: T-EPIC-01\ntitle: Child card\nstatus: available\npriority: 8\nkind: task\nbase_branch: beacon/T-EPIC\n---\nGoal: child.\n"
const familyFiles = {
  existsSync: (path) => /docs\/tasks\/T-EPIC(-01)?\.md$/.test(path),
  readFileSync: (path) => path.endsWith("T-EPIC-01.md") ? CHILD_CARD : EPIC_CARD,
}

test("an epic collapses its children, expands on click and links back from the child", async () => {
  const h = await setup({
    queue: [
      { id: "T-EPIC", title: "Harness epic", status: "ready", priority: 9 },
      { id: "T-EPIC-01", title: "Child card", status: "available", priority: 8 },
    ],
    files: familyFiles,
  })
  const sidebar = h.renderers["sidebar.content"]()
  findItem(sidebar, (item) => item.action === "card:T-EPIC" && item.kind === "queue-status").props.onMouseDown()
  const panel = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  assert.ok(texts(panel).includes("Children (1)"), "the child count stays visible")
  assert.equal(findItem(panel, (item) => item.kind === "card-child"), null, "children start collapsed")
  assert.equal(findItem(panel, (item) => item.kind === "card-dep" && item.name === "T-EPIC-01"), null,
    "the dependency ID is not drawn twice")
  const toggle = findItem(panel, (item) => item.kind === "card-section" && item.epicKey === "T-EPIC")
  assert.equal(toggle.children[0].props.content, "▶", "the children row carries a chevron")
  toggle.props.onMouseDown()
  for (const expected of ["Children (1)", "T-EPIC-01 · Child card", "available"]) {
    assert.ok(texts(panel).includes(expected), `epic card missing ${expected}`)
  }
  assert.equal(toggle.children[0].props.content, "▼", "clicking expands the children")
  const kindRow = findItem(panel, (item) => item.kind === "card-meta" && item.marker.trim() === "Kind")
  assert.equal(kindRow?.children[1].props.content, "epic")
  const child = findItem(panel, (item) => item.kind === "card-child")
  assert.equal(child.item.action, "card:T-EPIC-01")
  child.props.onMouseDown()
  const childTexts = texts(panel)
  assert.ok(childTexts.includes("Child card"), "the child opens in the same panel")
  const parent = findItem(panel, (item) => item.kind === "card-parent")
  assert.equal(parent.item.action, "card:T-EPIC")
  assert.equal(parent.item.status, "Harness epic", "the parent epic carries its queue title")
  parent.props.onMouseDown()
  assert.ok(texts(panel).includes("Harness epic"), "the parent epic opens from the child")
  h.dispose()
})

test("an empty depends list adds no link and back with no history closes the panel", async () => {
  const EMPTY_DEPS = "---\nid: T-EMPTY\ntitle: No deps\nstatus: available\ndepends: []\n---\nGoal: none.\n"
  const files = {
    existsSync: (path) => path.endsWith("/docs/tasks/T-EMPTY.md"),
    readFileSync: () => EMPTY_DEPS,
  }
  let layer
  const h = await setup({
    queue: [{ id: "T-EMPTY", title: "No deps", status: "ready" }],
    files, onLayer: (value) => { layer = value },
  })
  const sidebar = h.renderers["sidebar.content"]()
  findItem(sidebar, (item) => item.action === "card:T-EMPTY" && item.kind === "queue-title").props.onMouseDown()
  const panel = h.renderers["session.panel"]({ name: CARD_PANEL_NAME })
  assert.equal(findItem(panel, (item) => item.kind === "card-dep"), null, "no fake link from an empty list")
  layer.commands.find((command) => command.id === "beacon.card.back").run()
  assert.equal(h.closed.length, 1, "back with no history closes the panel")
  h.dispose()
})
