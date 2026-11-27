// Run: node --import ./.opencode/plugins/beacon-project/test_support/register_plugin_doubles.js --test .opencode/plugins/beacon-project/ui/ui_overlay_mount_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { OVERLAY_LINES, cardLine, createOverlayMount } from "./ui_overlay_mount.js"
import { solidModule, themeStub } from "./ui_test_helpers.js"

const theme = themeStub()
const color = (key) => key === "base" ? theme.text.base
  : key === "muted" ? theme.text.muted
    : key === "action" ? theme.text.action.primary.base
      : key === "raised" ? theme.background.raised.high
        : key === "panel" ? theme.background.raised.base
          : theme.text.feedback[key]?.base ?? theme.text.muted
const deps = (onClose = () => {}) => ({ color, BOLD: 1, onClose })
const cells = (row) => row.children.slice(0, 3).map((cell) => cell.props.content)

const ROWS = [
  {
    id: "T-de0058a2", title: "JS sidebar read and grouping allocation cut",
    state: "running", pr: 2741, runID: "run-abc",
  },
  {
    id: "T-b8fc3701", title: "Reconcile tick reads and fanout cut",
    state: "failure", pr: 2742, runID: "run-def",
  },
]

test("apply fills a fixed table from the model and keeps full ids", () => {
  const created = []
  const mount = createOverlayMount(solidModule(created), deps())
  const before = created.length

  mount.apply({ title: "Performance", summary: "2 perf cards · 1 running", rows: ROWS })

  assert.equal(created.length, before, "apply must not allocate nodes")
  assert.equal(mount.rows.node.length, OVERLAY_LINES, "the line pool is fixed at construction")
  assert.equal(mount.title.props.content, "Performance")
  assert.equal(mount.summary.props.content, "2 perf cards · 1 running")
  assert.deepEqual(cells(mount.rows.node[0]), [
    "T-de0058a2",
    "JS sidebar read and grouping allocation cut",
    "running · #2741 · run-abc",
  ])
  assert.deepEqual(cells(mount.rows.node[1]), [
    "T-b8fc3701",
    "Reconcile tick reads and fanout cut",
    "failure · #2742 · run-def",
  ])
  assert.equal(mount.rows.node[1].children[1].props.fg, "error", "a failed card is error text")
})

test("cardLine accepts the run id and PR names from the snapshot", () => {
  assert.deepEqual(cardLine({ ID: "T-1", Title: "A card", State: "success", PRNumber: 42, RunID: "run-1" }), {
    marker: "T-1", name: "A card", status: "success · #42 · run-1",
    color: "success", markerColor: "muted", statusColor: "muted", kind: "card",
  })
})

test("a second apply updates props in place and hides unused rows", () => {
  const created = []
  const mount = createOverlayMount(solidModule(created), deps())
  mount.apply({ title: "Performance", rows: ROWS })
  const before = created.length
  const title = mount.title
  const firstRow = mount.rows.node[0]

  mount.apply({ title: "Perf", rows: [ROWS[1]] })

  assert.equal(created.length, before)
  assert.equal(mount.title, title, "the title node is reused")
  assert.equal(mount.rows.node[0], firstRow, "the pool keeps its node identity")
  assert.deepEqual(cells(firstRow), [
    "T-b8fc3701", "Reconcile tick reads and fanout cut", "failure · #2742 · run-def",
  ])
  assert.equal(mount.rows.node[1].props.visible, false)
  assert.equal(mount.summary.props.visible, false, "an empty summary takes no space")
})

test("the detail pane uses the shared check helpers", () => {
  const created = []
  const mount = createOverlayMount(solidModule(created), deps())
  const before = created.length

  mount.apply({
    detail: { title: "Checks" },
    checks: [
      { name: "ci", state: "failure", durationSeconds: 28 },
      { name: "build", state: "success" },
    ],
  })

  assert.equal(created.length, before)
  assert.equal(mount.checkSummary.props.content, "1 failed · 1/2 · longest 28s")
  assert.deepEqual(cells(mount.detailRows.node[0]), ["✖ ", "ci failed", "28s"])
})

test("the hint and close glyph both call the stored close callback", () => {
  let closed = 0
  const mount = createOverlayMount(solidModule([]), deps(() => { closed += 1 }))

  assert.equal(mount.hint.props.content, "ctrl+o")
  assert.equal(mount.keys.props.content, "esc close")
  assert.equal(mount.close.props.content, "✕")
  mount.hint.props.onMouseUp()
  mount.close.props.onMouseUp()
  assert.equal(closed, 2)
})

test("an empty model leaves default chrome and no visible rows", () => {
  const mount = createOverlayMount(solidModule([]), deps())

  mount.apply()

  assert.equal(mount.title.props.content, "Performance")
  assert.equal(mount.detailTitle.props.content, "Checks")
  assert.equal(mount.rows.node[0].props.visible, false)
  assert.equal(mount.detailRows.node[0].props.visible, false)
})
