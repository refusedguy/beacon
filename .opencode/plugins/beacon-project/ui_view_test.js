// Run: node --test .opencode/plugins/beacon-project/ui_view_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { NOW, lines, makeRun, solidModule, themeStub, signalStub } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  BEACON_STATUS_RPC_ID,
  BeaconStatusRpc,
  CliStatusSource,
  DECISION_LIMIT,
  EMPTY_SNAPSHOT,
  PANEL_NAME,
  RECENT_LIMIT,
  REASON_WIDTH,
  RULE,
  TITLE_WIDTH,
  beaconStatusDefinition,
  compactReason,
  createCliStatusSource,
  createRowMount,
  createStatusSource,
  defineBeaconStatusPlugin,
  detailCommand,
  detailLines,
  elapsed,
  enrichRuns,
  errorMessage,
  normalizeRun,
  normalizeSnapshot,
  parseSnapshot,
  phaseLabel,
  readTitle,
  renderSnapshotLines,
  runLineItems,
  snapshotLineItems,
  stateBarItem,
  truncate,
  default: beaconStatus,
} = ui

test("elapsed formats seconds, minutes, hours and days", () => {
  assert.equal(elapsed(500), "0s")
  assert.equal(elapsed(90_000), "1m")
  assert.equal(elapsed(3 * 60_000), "3m")
  assert.equal(elapsed(2 * 60 * 60_000 + 5 * 60_000), "2h")
  assert.equal(elapsed(26 * 60 * 60_000), "1d")
  assert.equal(elapsed(-1), "")
})

test("row items carry stable cells and colors by theme key", () => {
  const [head, detail] = runLineItems(normalizeRun(makeRun({ State: "blocked", title: "Blocked work" })), NOW)
  assert.deepEqual(head, {
    marker: "▲", name: "T-1 Blocked work", status: "BLOCK", color: "error", bold: true, runID: "run-1",
  })
  assert.deepEqual(detail, { marker: "", name: "#42 3m", status: "waiting CI", color: "muted", runID: "run-1" })
})

test("compactReason turns noisy failures into readable causes", () => {
  assert.equal(compactReason("workspace: conflict: refusing to remove dirty worktree at /tmp/wt"), "dirty worktree")
  assert.equal(compactReason("pull request closed"), "PR closed")
  assert.equal(compactReason("reconciler: required checks failed"), "required checks failed")
  assert.equal(compactReason("   "), "")
})

test("phaseLabel follows the live phase and merged rows drop the reason", () => {
  const waiting = normalizeRun(makeRun({
    State: "waiting_ci", Durations: { waiting_ci_seconds: 120, running_seconds: 5, blocked_seconds: 0 },
  }))
  assert.equal(phaseLabel(waiting, NOW), "ci 2m")
  const merged = normalizeRun(makeRun({ State: "merged", Reason: "automatic rebase" }))
  const [, detail] = runLineItems(merged, NOW, true)
  assert.equal(detail.status, "")
  assert.ok(detail.name.startsWith("#42"))
})

test("recent shows one terminal run per task and skips tasks active again", () => {
  const snapshot = {
    active: [makeRun({ ID: "a1", TaskID: "T-ACTIVE", State: "running" })],
    recent: [
      makeRun({ ID: "r1", TaskID: "T-ACTIVE", State: "closed", Reason: "PR closed" }),
      makeRun({ ID: "r2", TaskID: "T-B", State: "closed", Reason: "dirty worktree" }),
      makeRun({ ID: "r3", TaskID: "T-B", State: "closed", Reason: "old attempt" }),
      makeRun({ ID: "r4", TaskID: "T-C", State: "merged", Reason: "automatic rebase" }),
    ],
  }
  const rendered = lines(snapshot, null, NOW)
  assert.equal(rendered.filter((line) => line.includes("T-ACTIVE")).length, 1)
  assert.equal(rendered.filter((line) => line.includes("T-B")).length, 1)
  assert.ok(rendered.some((line) => line.includes("dirty worktree")), "the newest cause survives")
  assert.ok(rendered.some((line) => line.includes("T-C")))
})

test("state bar legend uses the merge metric instead of the capped recent window", () => {
  const item = stateBarItem({
    active: [makeRun({ ID: "r1", State: "running" })],
    recent: [makeRun({ ID: "r2", State: "merged" })],
    metrics: { merge_count: 37 },
  })
  assert.equal(item.counts.merged, 1)
  assert.equal(item.mergeTotal, 37)
  assert.ok(item.content.includes("M37"))
})

test("a stale transport error keeps the snapshot and clips the marker", () => {
  const snapshot = { active: [makeRun({ title: "Still here" })], recent: [] }
  const result = lines(snapshot, "rpc.unavailable", NOW)
  assert.ok(result.some((line) => line.includes("[stale] rpc.unavailable")))
  assert.ok(result.some((line) => line.includes("Still here")))
  const items = snapshotLineItems(snapshot, "x".repeat(200), NOW)
  assert.ok(items.find((item) => item.name.startsWith("[stale]")).name.length <= 9 + REASON_WIDTH)
})

test("empty snapshot still renders both sections behind a counted header", () => {
  assert.deepEqual(lines(EMPTY_SNAPSHOT), [
    "Beacon A0 B0 R0 F0 --:--:--", "▼ Active 0", "No active runs", "▼ Recent 0", "No recent runs",
  ])
})

test("detail groups fields into six sections with dim separators", () => {
  const run = normalizeRun(makeRun({
    title: "Fix TUI plugin shape",
    Timeline: [
      { at: "2026-11-23T10:06:00Z", kind: "merge_checks", state: "waiting_ci" },
      { at: "2026-11-23T10:01:00Z", kind: "created", state: "running" },
    ],
  }))
  const metrics = { dispatchCount: 25, rebaseCount: 2, fixCount: 1, mergeCount: 3, ciWaitSeconds: 90 }
  const detail = detailLines(run, metrics, NOW)
  const name = (item) => item.name ?? item.text
  assert.deepEqual(detail.filter((i) => i.bold && i.marker === "▼").map(name),
    ["Identity", "Timing", "Attempts", "Reason", "Timeline", "Decisions"])
  assert.equal(detail.filter((i) => i.name === RULE).length, 5)
  const field = (label) => detail.find((i) => i.name === label)?.status
  assert.equal(field("State"), "● running · RUN")
  assert.equal(field("PR"), "#42")
  assert.equal(field("Branch"), "beacon/T-1")
  assert.equal(field("SHA"), "abcdef12345…")
  assert.equal(field("Created"), "10:00:00")
  assert.equal(field("Updated"), "10:07:00")
  assert.equal(field("Age"), "3m")
  assert.equal(field("Wait CI"), "1m")
  assert.equal(field("Running"), "5s")
  assert.equal(field("Rebase"), "2/3")
  assert.equal(field("Fix"), "1/3")
  assert.equal(field("Dispatch"), "25")
  assert.equal(field("Merges"), "3")
  const notes = detail.filter((i) => i.name && i.name !== RULE && !i.status && i.color === "muted").map(name)
  assert.deepEqual(notes.filter((n) => n.includes("→")), [
    "10:01:00 created → running", "10:06:00 merge_checks → waiting_ci",
  ])
  assert.ok(notes.includes("10:06:00 reconcile.decision merge"))
})

test("collapsed detail sections show ▶ and drop their body rows", () => {
  const run = normalizeRun(makeRun())
  const metrics = { dispatchCount: 25 }
  const collapsed = { Timing: false, Decisions: false }
  const detail = detailLines(run, metrics, NOW, collapsed)
  const headers = detail.filter((i) => i.sectionKey)
  assert.deepEqual(headers.map((i) => [i.sectionKey, i.marker, i.name, i.bold]), [
    ["Identity", "▼", "Identity", true],
    ["Timing", "▶", "Timing", true],
    ["Attempts", "▼", "Attempts", true],
    ["Reason", "▼", "Reason", true],
    ["Timeline", "▼", "Timeline", true],
    ["Decisions", "▶", "Decisions", true],
  ])
  const names = detail.map((i) => i.name ?? i.text)
  for (const omitted of ["Created", "Updated", "Age", "Wait CI", "Running", "Blocked"]) {
    assert.equal(names.includes(omitted), false, `collapsed Timing omits ${omitted}`)
  }
  assert.equal(names.some((n) => n.includes("reconcile.decision")), false, "collapsed Decisions omits its rows")
  assert.equal(names.includes("State"), true, "Identity stays open")
  assert.equal(names.includes("Rebase"), true, "Attempts stays open")
  assert.equal(detail.filter((i) => i.name === RULE).length, 5, "separators stay put")
  const expanded = detailLines(run, metrics, NOW, {})
  assert.ok(expanded.some((i) => i.name === "Created"), "an unset flag shows the body")
  assert.ok(expanded.every((i) => !i.sectionKey || i.marker === "▼"), "unset flags show ▼")
})

test("detail caps decisions to the most recent 20 and marks the rest", () => {
  const decisions = Array.from({ length: 25 }, (_, i) => ({
    at: `2026-11-23T10:${String(i).padStart(2, "0")}:00Z`,
    kind: "reconcile.decision",
    verdict: `verdict-${i}`,
  }))
  const names = detailLines(normalizeRun(makeRun({ Decisions: decisions })), {}, NOW).map((i) => i.name ?? i.text)
  const rendered = names.filter((line) => line.includes("reconcile.decision"))
  assert.equal(rendered.length, DECISION_LIMIT)
  assert.ok(rendered[0].includes("verdict-5"), "drops the oldest five")
  assert.ok(rendered.at(-1).includes("verdict-24"), "keeps the newest decision")
  const marker = "... 5 earlier decisions"
  assert.ok(names.includes(marker), "marker carries the hidden count")
  assert.ok(names.indexOf(marker) < names.indexOf(rendered[0]), "marker precedes the shown decisions")

  const twenty = detailLines(normalizeRun(makeRun({ Decisions: decisions.slice(0, 20) })), {}, NOW).map((i) => i.name ?? i.text)
  assert.equal(twenty.filter((line) => line.includes("reconcile.decision")).length, 20)
  assert.equal(twenty.some((line) => line.includes("earlier decisions")), false)
})

test("detail with no run is explicit", () => {
  assert.deepEqual(detailLines(null), [{ text: "No run selected", color: "muted" }])
})

test("detail command binds ctrl+g, exposes a slash command and opens the panel", () => {
  const opened = []
  const cmd = detailCommand((name) => opened.push(name))
  assert.equal(PANEL_NAME, "beacon.detail")
  assert.equal(cmd.id, "beacon.run.detail")
  assert.equal(cmd.bind, "ctrl+g")
  assert.deepEqual(cmd.slash, { name: "beacon-detail" })
  assert.equal(cmd.palette, true)
  cmd.run()
  assert.deepEqual(opened, [PANEL_NAME])
})
