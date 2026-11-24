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

test("empty snapshot still renders both sections", () => {
  assert.deepEqual(lines(EMPTY_SNAPSHOT), ["Beacon", "▼ Active 0", "No active runs", "▼ Recent 0", "No recent runs"])
})

test("detail lines show identity, metrics, durations, attempts and history sorted by time", () => {
  const run = normalizeRun(makeRun({
    title: "Fix TUI plugin shape",
    Timeline: [
      { at: "2026-11-23T10:06:00Z", kind: "merge_checks", state: "waiting_ci" },
      { at: "2026-11-23T10:01:00Z", kind: "created", state: "running" },
    ],
  }))
  const metrics = { dispatchCount: 25, rebaseCount: 2, fixCount: 1, mergeCount: 3, ciWaitSeconds: 90 }
  const detail = detailLines(run, metrics, NOW)
  const text = detail.map((item) => item.text)
  assert.equal(text[0], "Run detail")
  assert.equal(text[1], "Metrics dispatch 25 · rebase 2 · fix 1 · merge 3 · ci_wait 1m")
  assert.ok(text[2].includes("ID run-1 · task T-1"))
  assert.ok(text[3].includes("● Fix TUI plugin shape"))
  assert.ok(text.some((line) => line.includes("PR #42")))
  assert.ok(text.some((line) => line.includes("Branch beacon/T-1")))
  assert.ok(text.some((line) => line.includes("SHA abcdef12345…")))
  assert.ok(text.some((line) => line.includes("Created 10:00:00 · Updated 10:07:00 · 3m")))
  assert.ok(text.some((line) => line.includes("wait_ci 1m · running 5s · blocked 0s")))
  assert.ok(text.some((line) => line.includes("Attempts 2/3 rebase · 1/3 fix")))
  assert.ok(text.some((line) => line.includes("Reason waiting CI")))
  const timeline = text.filter((line) => line.startsWith("  ") && line.includes("→"))
  assert.deepEqual(timeline, ["  10:01:00 created → running", "  10:06:00 merge_checks → waiting_ci"])
  assert.ok(text.includes("  10:06:00 reconcile.decision merge"))
  assert.equal(detail[0].bold, true)
})

test("detail caps decisions to the most recent 20 and marks the rest", () => {
  const decisions = Array.from({ length: 25 }, (_, i) => ({
    at: `2026-11-23T10:${String(i).padStart(2, "0")}:00Z`,
    kind: "reconcile.decision",
    verdict: `verdict-${i}`,
  }))
  const text = detailLines(normalizeRun(makeRun({ Decisions: decisions })), {}, NOW).map((item) => item.text)
  const rendered = text.filter((line) => line.includes("reconcile.decision"))
  assert.equal(rendered.length, DECISION_LIMIT)
  assert.ok(rendered[0].includes("verdict-5"), "drops the oldest five")
  assert.ok(rendered.at(-1).includes("verdict-24"), "keeps the newest decision")
  const marker = "  ... 5 earlier decisions"
  assert.ok(text.includes(marker), "marker carries the hidden count")
  assert.ok(text.indexOf(marker) < text.indexOf(rendered[0]), "marker precedes the shown decisions")

  const twenty = detailLines(normalizeRun(makeRun({ Decisions: decisions.slice(0, 20) })), {}, NOW).map((item) => item.text)
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
