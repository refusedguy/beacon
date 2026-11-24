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
  readTitle,
  renderSnapshotLines,
  runLineItems,
  snapshotLineItems,
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
