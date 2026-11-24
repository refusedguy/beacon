// Run: node --test .opencode/plugins/beacon-project/ui_view_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { NOW, makeRun } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  DECISION_LIMIT, DETAIL_OPEN, EMPTY_SNAPSHOT, PANEL_NAME, REASON_WIDTH,
  SIDEBAR_OPEN, STATE_BAR_WIDTH, cleanErrorMessage, compactReason, detailActions, detailCommand,
  detailLines, detailModel, elapsed, mergedReason, normalizeRun, phaseLabel,
  recentRuns, runLineItems, sidebarModel, stateBarItem, truncate,
} = ui

test("elapsed formats seconds, minutes, hours and days", () => {
  assert.equal(elapsed(500), "0s")
  assert.equal(elapsed(90_000), "1m")
  assert.equal(elapsed(3 * 60_000), "3m")
  assert.equal(elapsed(2 * 60 * 60_000 + 5 * 60_000), "2h")
  assert.equal(elapsed(26 * 60 * 60_000), "1d")
  assert.equal(elapsed(-1), "")
})

test("state bar builds proportional colored segments and a human summary", () => {
  const item = stateBarItem({
    active: [
      makeRun({ ID: "a", State: "running" }), makeRun({ ID: "b", State: "running" }),
      makeRun({ ID: "c", State: "waiting_ci" }), makeRun({ ID: "d", State: "blocked" }),
    ],
    recent: [makeRun({ ID: "e", TaskID: "T-E", State: "merged" })],
    metrics: { merge_count: 51, rebase_count: 2, fix_count: 1 },
  })
  assert.deepEqual(item.counts, { running: 2, waiting_ci: 1, blocked: 1, merged: 1 })
  assert.equal(item.filled, 5)
  assert.equal(item.bar.length, STATE_BAR_WIDTH)
  assert.equal(item.segments.reduce((n, s) => n + s.text.length, 0), STATE_BAR_WIDTH)
  assert.deepEqual(item.segments.map((s) => s.color), ["success", "info", "error", "success", "muted"])
  assert.equal(item.legend, "R2 C1 B1 M51")
  assert.equal(item.summary, "2 running · 1 in CI · 1 blocked · 1 done")
  assert.equal(item.fg, "error", "a blocked run colors the bar with the error tone")
  const big = stateBarItem({ active: Array.from({ length: STATE_BAR_WIDTH + 2 }, (_, i) => makeRun({ ID: `r-${i}`, State: "running" })), recent: [] })
  assert.equal(big.filled, STATE_BAR_WIDTH)
  assert.equal(big.segments.at(-1).text, "", "a full bar leaves no muted remainder")
  assert.ok(big.content.includes(`R${STATE_BAR_WIDTH + 2}`))
  const empty = stateBarItem(EMPTY_SNAPSHOT)
  assert.equal(empty.segments[0].text, "")
  assert.equal(empty.segments.at(-1).text, "─".repeat(STATE_BAR_WIDTH))
  assert.ok(empty.content.includes("R0 C0 B0 M0"))
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
  assert.ok(detail.marker.startsWith("#42"))
  assert.equal(mergedReason(merged), "automatic rebase")
  assert.equal(mergedReason(normalizeRun(makeRun({ State: "merged", Reason: "", PRNumber: 42 }))), "merged PR #42")
})

test("run rows keep the full title and put PR, duration and cause on line two", () => {
  const title = "Make worktree registration refresh reliable at every exit"
  const [first, second] = runLineItems(normalizeRun(makeRun({ State: "blocked", title })), NOW)
  assert.deepEqual(first, {
    marker: "▲", markerSpace: true, name: title, status: "BLK", color: "error", bold: true, runID: "run-1",
  })
  assert.deepEqual(second, {
    marker: "#42", markerSpace: true, name: "3m", status: "waiting CI", color: "muted",
    statusColor: "error", runID: "run-1",
  })
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
  const recent = recentRuns(snapshot)
  assert.deepEqual(recent.map((r) => r.id), ["r2", "r4"], "the newest cause survives and active tasks are hidden")
  const items = sidebarModel(snapshot, null, NOW).items
  assert.ok(items.some((i) => i.status === "dirty worktree"))
  assert.equal(items.some((i) => i.status === "old attempt"), false)
})

test("sidebar sections collapse without losing the dispatcher", () => {
  const snapshot = {
    active: [makeRun()], recent: [makeRun({ ID: "r2", State: "closed" })],
    queue: [{ id: "T-2", title: "Queued", status: "ready", priority: 1 }],
  }
  const open = sidebarModel(snapshot, null, NOW).items
  assert.ok(open.some((i) => i.action === "dispatch"))
  const closed = sidebarModel(snapshot, null, NOW, { active: false, recent: false }).items
  assert.equal(closed.some((i) => i.action === "dispatch"), false)
  assert.equal(closed.find((i) => i.sectionKey === "active").marker, "▶")
  assert.equal(closed.find((i) => i.sectionKey === "recent").marker, "▶")
  assert.equal(closed.some((i) => i.runID), false)
  assert.deepEqual(SIDEBAR_OPEN, { active: true, queue: false, recent: true })
})

test("a stale transport error keeps the snapshot and clips the marker", () => {
  const snapshot = { active: [makeRun({ title: "Still here" })], recent: [] }
  const items = sidebarModel(snapshot, "x".repeat(200), NOW).items
  const stale = items.find((i) => i.name.startsWith("[stale]"))
  assert.ok(stale)
  assert.ok(stale.name.length <= 9 + REASON_WIDTH)
  assert.ok(items.some((i) => i.name === "Still here"))
})

test("a sanitized stale line keeps the prefix and never shows help text", () => {
  const clean = cleanErrorMessage(new Error("Error: bad flag\nUsage:\n  beacon --help"))
  const stale = sidebarModel(EMPTY_SNAPSHOT, clean, NOW).items.find((i) => i.name.startsWith("[stale]"))
  assert.equal(stale.name, "[stale] beacon status temporarily unavailable")
  assert.equal(stale.name.includes("--help"), false)
  assert.ok(stale.name.length <= 9 + REASON_WIDTH)
})

test("empty snapshot shows one idle line and counted headers", () => {
  const items = sidebarModel(EMPTY_SNAPSHOT, null, NOW).items
  assert.deepEqual(items.map((i) => i.name), [
    "Beacon", "R0 C0 B0 M0", "Active", "Idle · no ready tasks",
    "Queue", "Recent", "No recent runs",
  ])
  assert.deepEqual(items.filter((i) => i.sectionKey).map((i) => [i.name, i.status]), [
    ["Active", "0"], ["Queue", "0"], ["Recent", "0"],
  ])
})

test("detail model uses four sections with full wrapping text", () => {
  const longTitle = "Working collapsible sections and honest close hint in detail panel"
  const longReason = "workspace: conflict: refusing to remove dirty worktree at /mnt/projects/beacon-handoff/beacon-impl/.beacon/worktrees/T-1"
  const run = normalizeRun(makeRun({
    title: longTitle, State: "blocked", Reason: longReason,
    Checks: [{ name: "ci", state: "success", duration_seconds: 28 }],
    Timeline: [
      { at: "2026-11-23T10:06:00Z", kind: "merge_checks", state: "waiting_ci" },
      { at: "2026-11-23T10:01:00Z", kind: "created", state: "running" },
    ],
  }))
  const metrics = { dispatchCount: 25, rebaseCount: 2, fixCount: 1, mergeCount: 3 }
  const model = detailModel(run, metrics, NOW, { ...DETAIL_OPEN, technical: true })
  assert.equal(model.title, longTitle, "the hero uses the full title")
  assert.equal(model.badge, "▲ BLOCKED · 3m", "the hero badge carries the full state and live duration")
  assert.equal(model.pr, "#42")
  assert.equal(model.branch, "beacon/T-1")
  assert.equal(model.task, "T-1")
  assert.ok(model.phase.includes("Coding 5s") && model.phase.includes("CI 1m"))
  assert.deepEqual(model.rows.filter((i) => i.sectionKey).map((i) => i.sectionKey), ["why", "checks", "timeline", "technical"])
  const names = model.rows.map((i) => i.name ?? i.text)
  assert.ok(names.includes(longReason), "the reason is not cut to a fixed width")
  assert.ok(names.includes("ci"), "successful checks hide the word success")
  assert.equal(model.rows.find((i) => i.sectionKey === "checks").status, "1/1 · 28s")
  assert.ok(names.includes("Run created"), "timeline events use human copy")
  assert.ok(model.rows.some((i) => i.marker === "10:01:00  "), "the timeline time sits in its own padded column")
  assert.ok(names.some((n) => n.includes("reconcile.decision merge")))
  assert.deepEqual(detailActions(run), ["retry", "copy"])
})

test("detail sections collapse to ▶ and Technical defaults to collapsed", () => {
  const run = normalizeRun(makeRun())
  const open = detailModel(run, {}, NOW, {})
  assert.deepEqual(open.rows.filter((i) => i.sectionKey).map((i) => [i.sectionKey, i.marker]), [
    ["why", "▼"], ["checks", "▼"], ["timeline", "▼"], ["technical", "▶"],
  ])
  assert.equal(open.rows.some((i) => i.name === "waiting CI"), true)
  assert.equal(open.rows.some((i) => i.sectionKey === "technical"), true)
  const collapsed = detailModel(run, {}, NOW, { why: false, checks: false, timeline: false, technical: false })
  const names = collapsed.rows.map((i) => i.name)
  assert.equal(names.some((n) => n.includes("created")), false)
  assert.equal(names.some((n) => n.includes("reconcile.decision")), false)
  assert.equal(names.includes("ci"), false)
  assert.equal(collapsed.rows.filter((i) => i.sectionKey).every((i) => i.marker === "▶"), true)
  assert.deepEqual(DETAIL_OPEN, { why: true, checks: true, timeline: true, technical: false })
})

test("empty CI checks show (none), and merged runs never show a dash reason", () => {
  const run = normalizeRun(makeRun({ State: "merged", Reason: "" }))
  const names = detailModel(run, {}, NOW, {}).rows.map((i) => i.name)
  assert.ok(names.includes("(none)"), "no checks means a readable placeholder")
  assert.ok(names.includes("merged PR #42"))
  assert.equal(names.includes("-"), false)
})

test("detail caps decisions to the most recent 20 and marks the rest", () => {
  const decisions = Array.from({ length: 25 }, (_, i) => ({
    at: `2026-11-23T10:${String(i).padStart(2, "0")}:00Z`,
    kind: "reconcile.decision", verdict: `verdict-${i}`,
  }))
  const rows = detailLines(normalizeRun(makeRun({ Decisions: decisions })), {}, NOW, { technical: true })
  const rendered = rows.filter((i) => i.name.includes("reconcile.decision")).map((i) => i.name)
  assert.equal(rendered.length, DECISION_LIMIT)
  assert.ok(rendered[0].includes("verdict-5"))
  assert.ok(rendered.at(-1).includes("verdict-24"))
  assert.ok(rows.some((i) => i.name === "... 5 earlier decisions"))
})

test("detailActions follows the run state", () => {
  assert.deepEqual(detailActions(normalizeRun(makeRun({ State: "running" }))), ["approve", "cancel", "help", "copy"])
  assert.deepEqual(detailActions(normalizeRun(makeRun({ State: "waiting_ci" }))), ["approve", "cancel", "help", "copy"])
  assert.deepEqual(detailActions(normalizeRun(makeRun({ State: "blocked" }))), ["retry", "copy"])
  assert.deepEqual(detailActions(normalizeRun(makeRun({ State: "closed" }))), ["retry", "copy"])
  assert.deepEqual(detailActions(normalizeRun(makeRun({ State: "merged" }))), ["copy"])
  assert.deepEqual(detailActions(normalizeRun(makeRun({ State: "running", PRURL: "https://x/pull/42" }))), ["approve", "cancel", "pr", "help", "copy"])
  assert.deepEqual(detailActions(null), [])
})

test("detail with no run is explicit", () => {
  const model = detailModel(null)
  assert.equal(model.title, "No run selected")
  assert.deepEqual(model.actions, [])
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

test("truncate keeps short text unchanged and clips long text", () => {
  assert.equal(truncate("short", 10), "short")
  assert.equal(truncate("abcdefghijklmnopqrstuvwxyz0123456789", 10), "abcdefghi…")
})
