// Header identity and total time for the read-only card panel (T-4b1b14b6).
// Run: node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js --test .opencode/plugins/beacon-project/ui/ui_card_view_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  CARD_PANEL_NAME, beaconStatusDefinition, cardHeader, cardModel,
  createButtonPlate, createCardMount, createDetailMount, createPanelChrome,
  durationText, normalizeRun, normalizeSnapshot, runEventTimes, runTotalTime,
} from "./ui.js"
import { hostModule, solidModule, themeStub } from "./ui_test_helpers.js"

const color = (key) => `theme.${key}`
const cardDeps = (onClose = () => {}) => ({
  color, BOLD: 1, onCopy: () => {}, onClose, onBack: () => {}, onItem: () => {},
  setProp: (node, key, value) => { node.props[key] = value },
})
const CARD = {
  id: "T-0ea563d7", title: "Remove quadratic overlap checks", priority: "92",
  status: "done", kind: "task", depends: "", body: "Goal: bound the checks.\n\nContext: a benchmark.\n",
}
const SPAN = {
  ID: "run-2534", TaskID: "T-0ea563d7", PRNumber: 2534, State: "merged",
  Timeline: [
    { at: "2026-11-26T10:00:00Z", kind: "dispatch" },
    { at: "2026-11-26T10:08:00Z", kind: "reconcile.decision" },
    { at: "2026-11-26T10:16:00Z", kind: "merged" },
  ],
}

function texts(node, out = []) {
  if (typeof node.props?.content === "string" && node.props.content) out.push(node.props.content)
  for (const child of node.children ?? []) texts(child, out)
  return out
}
function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const hit = findText(child, content)
    if (hit) return hit
  }
  return null
}
function headerOf(node) {
  const header = node.children?.[0]
  assert.equal(header.props.flexDirection, "row")
  assert.equal(header.props.justifyContent, "space-between", "the header keeps its two-sided layout")
  return header
}
function parentOf(root, target) {
  for (const child of root.children ?? []) {
    if (child === target) return root
    const found = parentOf(child, target)
    if (found) return found
  }
  return null
}
function findItem(node, predicate) {
  if (node.item && predicate(node.item)) return node
  for (const child of node.children ?? []) {
    const found = findItem(child, predicate)
    if (found) return found
  }
  return null
}

test("duration uses the smallest readable unit, like the rest of the panel", () => {
  assert.equal(durationText(500), "0s")
  assert.equal(durationText(16 * 60 * 1000), "16m")
  assert.equal(durationText(2 * 60 * 60 * 1000), "2h")
  assert.equal(durationText(-1), "")
})

test("total time spans the first and last event stamp, including decisions", () => {
  const run = normalizeRun(SPAN)
  assert.deepEqual(runEventTimes(run), [
    Date.parse("2026-11-26T10:00:00Z"),
    Date.parse("2026-11-26T10:08:00Z"),
    Date.parse("2026-11-26T10:16:00Z"),
  ])
  assert.deepEqual(runTotalTime(run), { text: "16m", label: "total" })
})

test("a run without events shows its updated_at with an explicit label", () => {
  const run = normalizeRun({ TaskID: "T-9", UpdatedAt: "2026-11-26T10:16:00Z" })
  assert.deepEqual(runTotalTime(run), { text: "10:16:00", label: "updated" })
  assert.deepEqual(runTotalTime(normalizeRun({ TaskID: "T-9" })), { text: "", label: "" })
})

test("the header line carries task ID, PR and total time from one run", () => {
  const header = cardHeader(normalizeRun(SPAN))
  assert.equal(header.taskID, "T-0ea563d7")
  assert.equal(header.pr, 2534)
  assert.ok(header.ref.includes("T-0ea563d7"))
  assert.ok(header.ref.includes("#2534"))
  assert.equal(header.duration, "16m")
  assert.deepEqual(cardHeader(null), { ref: "", taskID: "", pr: 0, duration: "", label: "" })
})

test("a 16 minute fixture renders one header line with both refs and a right-aligned duration", () => {
  const created = []
  const mount = createCardMount(solidModule(created), cardDeps())
  mount.apply(cardModel(CARD, [], null, normalizeRun(SPAN)))

  const header = headerOf(mount.node)
  const [title, controls, duration] = header.children
  assert.equal(title.props.flexGrow, 1, "the title pushes the controls and duration right")
  assert.ok(texts(title).some((line) => line.includes("T-0ea563d7") && line.includes("#2534")),
    "the task ID and PR share one header line")
  assert.equal(duration.props.content, "total 16m", "the duration is labelled")
  assert.equal(duration.props.visible, true)
  assert.ok(parentOf(mount.node, duration) === header, "the duration sits in the header row")
  assert.ok(header.children.indexOf(duration) > header.children.indexOf(controls),
    "the duration stays on the right edge past the controls")
  assert.equal(controls.props.flexWrap, "wrap", "the controls keep their narrow-width layout")
  assert.ok(findText(mount.node, "← Back") && findText(mount.node, "ctrl+g") && findText(mount.node, "✕"),
    "the existing header controls still render")
})

test("the card detail rows stay byte-identical when the header gains a run", () => {
  const plain = cardModel(CARD, [], null)
  const withRun = cardModel(CARD, [], null, normalizeRun(SPAN))
  assert.equal(JSON.stringify(withRun.rows), JSON.stringify(plain.rows))
  const mount = createCardMount(solidModule([]), cardDeps())
  mount.apply(plain)
  const before = mount.body.node.map((node) => JSON.stringify(node.item ?? node.props))
  mount.apply(withRun)
  const after = mount.body.node.map((node) => JSON.stringify(node.item ?? node.props))
  assert.deepEqual(after, before, "only the header changes")
  assert.equal(findText(mount.node, "Card T-0ea563d7") ?? null, null, "the ref replaces the old title")
})

test("a queue row finds its run from the snapshot without a second lookup", () => {
  const snapshot = normalizeSnapshot({
    active: [SPAN], recent: [], queue: [{ id: "T-0ea563d7", title: "Queued", status: "in_progress" }],
  })
  assert.equal(snapshot.queue[0].run.taskID, "T-0ea563d7")
  const model = cardModel(CARD, snapshot.queue, null)
  assert.equal(model.header.ref, "T-0ea563d7 · #2534")
  assert.equal(model.header.duration, "16m")
  const mount = createCardMount(solidModule([]), cardDeps())
  mount.apply(model)
  assert.ok(texts(mount.node).includes("total 16m"))

  const idle = cardModel(CARD, [{ id: "T-0ea563d7", status: "ready" }], null)
  assert.equal(idle.header.ref, "")
  mount.apply(idle)
  assert.ok(findText(mount.node, "Card T-0ea563d7"), "a card with no run keeps the plain title")
  assert.equal(findText(mount.node, "total 16m"), null, "stale duration is cleared")
})

test("the live panel renders the header from a snapshot run", async () => {
  const renderers = Object.create(null)
  const rpc = {
    snapshot: async () => ({
      active: [SPAN], recent: [],
      queue: [{ id: "T-0ea563d7", title: "Queued", status: "in_progress" }],
    }),
    control: async () => ({}), dispatch: async () => ({}),
    events: { on: () => () => {} },
  }
  const files = {
    existsSync: () => true,
    readFileSync: () => "---\nid: T-0ea563d7\ntitle: Task\nstatus: in_progress\n---\nGoal: work.\n",
  }
  const ctx = {
    theme: themeStub(), client: { rpc: () => rpc },
    storage: {
      store: (name, init) => [
        name === "beacon-sidebar-sections" ? { ...init.initial, queue: true } : { ...init.initial },
        async () => {},
      ],
    },
    ui: {
      toast: { show: () => {} },
      slot: ({ append, render }) => { renderers[append] = render; return () => {} },
      panel: { open: () => {}, close: () => {} },
    },
    keymap: { layer: () => () => {} },
  }
  const modules = {
    "@opentui/solid": solidModule([]),
    "solid-js": { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    root: "/p", options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier], cardDeps: files,
  })
  await new Promise((resolve) => setImmediate(resolve))
  const sidebar = renderers["sidebar.content"]()
  const row = findItem(sidebar, (item) => item.action === "card:T-0ea563d7")
  assert.ok(row, "the queue still opens the card")
  row.props.onMouseDown()
  const panel = renderers["session.panel"]({ name: CARD_PANEL_NAME })
  assert.ok(texts(panel).some((line) => line.includes("T-0ea563d7") && line.includes("#2534")))
  assert.ok(texts(panel).includes("total 16m"))
  dispose()
})

test("panel chrome builder keeps header props and the 10 versus 8 field padding", () => {
  const card = createPanelChrome(solidModule([]), cardDeps(), {
    title: "Task card", titleColor: "action", padding: 10, wrapControls: true,
  })
  const detail = createPanelChrome(solidModule([]), cardDeps(), {
    title: "Beacon", padding: 8,
  })
  assert.equal(card.fieldKey("ID"), "ID        ", "card keys stay padEnd(10)")
  assert.equal(detail.fieldKey("Task"), "Task    ", "detail keys stay padEnd(8)")
  for (const chrome of [card, detail]) {
    assert.equal(chrome.container.props.flexDirection, "column")
    assert.equal(chrome.container.props.minWidth, 0)
    assert.equal(chrome.container.props.flexGrow, 1)
    assert.equal(chrome.header.props.flexDirection, "row")
    assert.equal(chrome.header.props.justifyContent, "space-between")
    assert.equal(chrome.header.props.flexShrink, 0)
    assert.equal(chrome.title.props.attributes, 1, "the title is bold")
    assert.equal(chrome.hint.props.content, "ctrl+g")
    assert.equal(chrome.hint.props.fg, "theme.action")
    assert.equal(chrome.hint.props.paddingLeft, 1)
    assert.equal(chrome.hint.props.paddingRight, 1)
    assert.equal(chrome.gap.props.content, " ")
    assert.equal(chrome.gap.props.fg, "theme.muted")
    assert.equal(chrome.close.props.content, "✕")
    assert.equal(chrome.close.props.fg, "theme.error")
    assert.equal(chrome.close.props.paddingLeft, 1)
    assert.equal(chrome.close.props.paddingRight, 1)
  }
  assert.equal(card.title.props.content, "Task card")
  assert.equal(card.title.props.fg, "theme.action")
  assert.equal(card.controls.props.flexWrap, "wrap")
  assert.equal(card.controls.props.rowGap, 1)
  assert.equal(detail.title.props.content, "Beacon")
  assert.equal(detail.title.props.fg, "theme.base")
  assert.equal(detail.controls.props.flexWrap, undefined)
})

test("button plate builder keeps raised padding, gap and hover colours", () => {
  const ui = solidModule([])
  const card = createButtonPlate(ui, cardDeps(), {
    content: "⧉ Copy ID", fg: "base", name: "beacon-card-copy-id", marginRight: 1,
    boxClick: true, onMouseUp: () => {},
  })
  const detail = createButtonPlate(ui, cardDeps(), {
    content: "✖ Cancel", fg: "error", name: "beacon-action-cancel", marginRight: 1,
    boxClick: true, visible: false, onMouseUp: () => {},
  })
  const confirm = createButtonPlate(ui, cardDeps(), {
    content: "[ Keep ]", fg: "muted", hover: false, onMouseUp: () => {},
  })
  for (const plate of [card, detail, confirm]) {
    assert.equal(plate.box.props.flexDirection, "row")
    assert.equal(plate.box.props.flexShrink, 0)
    assert.equal(plate.box.props.paddingLeft, 1)
    assert.equal(plate.box.props.paddingRight, 1)
    assert.equal(plate.box.props.backgroundColor, "theme.raised")
    assert.notEqual(plate.text.props.fg, plate.box.props.backgroundColor)
  }
  assert.equal(card.text.props.name, "beacon-card-copy-id")
  assert.equal(card.text.props.fg, "theme.base")
  assert.equal(card.box.props.marginRight, 1)
  assert.equal(detail.text.props.name, "beacon-action-cancel")
  assert.equal(detail.text.props.fg, "theme.error")
  assert.equal(detail.box.props.visible, false)
  assert.equal(detail.text.props.visible, false)
  assert.equal(confirm.text.props.name, undefined, "confirm plate adds no name")
  assert.equal(confirm.box.props.marginRight, undefined, "keep plate has no margin prop")
  assert.equal(confirm.box.props.onMouseOver, undefined, "confirm plate has no hover")
  for (const plate of [card, detail]) {
    assert.equal(typeof plate.box.props.onMouseOver, "function")
    assert.equal(typeof plate.box.props.onMouseOut, "function")
    assert.equal(typeof plate.box.props.onMouseUp, "function")
    plate.box.props.onMouseOver()
    assert.equal(plate.box.props.backgroundColor, "theme.panel")
    plate.box.props.onMouseOut()
    assert.equal(plate.box.props.backgroundColor, "theme.raised")
  }
})

test("both mounts keep chrome and plate props on a host without box.children", () => {
  const created = []
  const ui = hostModule(created)
  createCardMount(ui, cardDeps())
  createDetailMount(ui, {
    color, BOLD: 1, onAction: () => {}, onToggle: () => {}, onClose: () => {},
    setProp: (node, key, value) => { node.props[key] = value },
  })
  const hints = created.filter((node) => node.props.content === "ctrl+g")
  assert.equal(hints.length, 2, "card and detail each keep one hint")
  for (const hint of hints) {
    assert.equal(hint.props.fg, "theme.action")
    assert.equal(hint.props.paddingLeft, 1)
    assert.equal(hint.props.paddingRight, 1)
  }
  const closes = created.filter((node) => node.props.content === "✕")
  assert.equal(closes.length, 2, "card and detail each keep one close control")
  for (const close of closes) {
    assert.equal(close.props.fg, "theme.error")
    assert.equal(close.props.paddingLeft, 1)
    assert.equal(close.props.paddingRight, 1)
  }
  const plates = created.filter((node) => node.props.backgroundColor === "theme.raised"
    && node.props.flexDirection === "row")
  assert.equal(plates.length, 11, "three card, six action and two confirm plates")
  for (const plate of plates) {
    assert.equal(plate.props.paddingLeft, 1)
    assert.equal(plate.props.paddingRight, 1)
    assert.equal(plate.props.flexShrink, 0)
  }
  assert.equal(created.filter((node) => node.props.content === " ").length >= 2, true)
})
