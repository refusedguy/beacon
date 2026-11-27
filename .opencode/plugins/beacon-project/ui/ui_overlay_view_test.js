// Fixed-snapshot coverage for the pure performance overlay view model.
// Run: node --test .opencode/plugins/beacon-project/ui/ui_overlay_view_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { normalizeSnapshot } from "./ui_model.js"
import { OVERLAY_PANEL_NAME, OVERLAY_TITLE, overlayCommand, overlayModel } from "./ui_overlay_view.js"

const NOW = Date.parse("2026-11-27T12:00:00Z")
const LONG_ID = "T-1234567890abcdef1234567890abcdef"
const LONG_TITLE = "A long performance card title that the sidebar clips at twenty-nine characters"
const SNAPSHOT = normalizeSnapshot({
  active: [
    { ID: "run-11", TaskID: "T-de0058a2", State: "running", PRNumber: 2741, UpdatedAt: "2026-11-27T11:58:00Z" },
    { ID: "run-12", TaskID: "T-unrelated", State: "running", PRNumber: 100, UpdatedAt: "2026-11-27T11:58:00Z" },
  ],
  recent: [
    { ID: "run-13", TaskID: "T-5d11af6c", State: "merged", PRNumber: 2743, UpdatedAt: "2026-11-27T10:00:00Z" },
  ],
  queue: [{ ID: "T-b8fc3701", Title: "Reconcile tick reads and fanout cut", Status: "ready" }],
  generated_at: "2026-11-27T11:59:00Z",
})
const CARDS = [
  { id: "T-de0058a2", title: "JS sidebar read and grouping allocation cut", perf: true },
  { id: "T-5d11af6c", title: "Planner allocation cut after dispatch merge", perf: true },
  { id: "T-b8fc3701", title: "Reconcile tick reads and fanout cut", status: "ready", perf: true },
  { id: LONG_ID, title: LONG_TITLE, perf: true },
  { id: "T-not-perf", title: "Ordinary card", perf: false },
]

test("overlay model joins injected perf cards to runs from the normalized snapshot", () => {
  const model = overlayModel(SNAPSHOT, NOW, CARDS)
  assert.equal(model.title, OVERLAY_TITLE)
  assert.equal(model.summary, "4 perf cards · 1 active · updated 1m ago")
  assert.deepEqual(model.rows, [
    { id: "T-de0058a2", title: "JS sidebar read and grouping allocation cut", state: "running", pr: "#2741", runID: "run-11" },
    { id: "T-5d11af6c", title: "Planner allocation cut after dispatch merge", state: "merged", pr: "#2743", runID: "run-13" },
    { id: "T-b8fc3701", title: "Reconcile tick reads and fanout cut", state: "ready", pr: "-", runID: "" },
    { id: LONG_ID, title: LONG_TITLE, state: "unknown", pr: "-", runID: "" },
  ])
})

test("full ids and titles are carried without sidebar clipping", () => {
  const { rows } = overlayModel(SNAPSHOT, NOW, CARDS)
  assert.equal(rows[3].id, LONG_ID)
  assert.ok(rows[3].id.length > 29)
  assert.equal(rows[3].title, LONG_TITLE)
  assert.ok(rows[3].title.length > 29)
})

test("perf cards carried by the snapshot take priority and are filtered by perf mark", () => {
  const snap = normalizeSnapshot({
    active: [{ ID: "run-21", TaskID: "T-a", State: "blocked", PRNumber: 77 }],
  })
  snap.perfCards = [
    { id: "T-a", title: "[perf] Allocation cut", perf: true },
    { id: "T-b", title: "Ordinary card", perf: false },
  ]
  const model = overlayModel(snap, NOW, CARDS)
  assert.deepEqual(model.rows, [
    { id: "T-a", title: "[perf] Allocation cut", state: "blocked", pr: "#77", runID: "run-21" },
  ])
  assert.equal(model.summary, "1 perf card · 1 active")
})

test("an empty snapshot has no rows and a zero summary", () => {
  const model = overlayModel(normalizeSnapshot({}), NOW, [])
  assert.deepEqual(model.rows, [])
  assert.equal(model.summary, "0 perf cards · 0 active")
})

test("the view model allocates no TUI nodes", () => {
  const src = readFileSync(new URL("./ui_overlay_view.js", import.meta.url), "utf8")
  assert.equal(src.includes("ui_nodes.js"), false)
  for (const name of ["createElement", "createBox", "createText", "mountLines"]) {
    assert.equal(src.includes(name), false, name)
  }
  for (const row of overlayModel(SNAPSHOT, NOW, CARDS).rows) {
    assert.equal(Object.hasOwn(row, "props"), false)
    assert.equal(Object.hasOwn(row, "children"), false)
    assert.equal(Object.hasOwn(row, "tag"), false)
  }
})

test("overlay command opens the overlay panel", () => {
  const opened = []
  const command = overlayCommand((name) => opened.push(name))
  assert.equal(command.id, "beacon.perf.overlay")
  assert.equal(command.title, "Beacon performance overlay")
  assert.equal(command.group, "Beacon")
  assert.equal(command.palette, true)
  assert.deepEqual(command.slash, { name: "beacon-overlay" })
  assert.equal(command.bind, "ctrl+o")
  command.run()
  assert.deepEqual(opened, [OVERLAY_PANEL_NAME])
  assert.equal(OVERLAY_PANEL_NAME, "beacon.overlay")
})
