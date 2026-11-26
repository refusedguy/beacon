// Run: node --test .opencode/plugins/beacon-project/ui_view_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { NOW, makeRun } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  DECISION_LIMIT, DETAIL_OPEN, EMPTY_SNAPSHOT, PANEL_NAME, REASON_WIDTH,
  SIDEBAR_OPEN, STATE_BAR_WIDTH, cleanErrorMessage, compactReason, detailActions, detailCommand,
  detailLines, detailModel, elapsed, mergedReason, metricsLine, normalizeRun, phaseLabel, renderSnapshotLines,
  runLineItems, runTotalTime, sidebarModel, stateBarItem, truncate,
} = ui

test("elapsed formats seconds, minutes, hours and days", () => {
  assert.equal(elapsed(500), "0s")
  assert.equal(elapsed(90_000), "1m")
  assert.equal(elapsed(3 * 60_000), "3m")
  assert.equal(elapsed(5467_000), "1h 31m")
  assert.equal(elapsed(2 * 60 * 60_000 + 5 * 60_000), "2h 5m")
  assert.equal(elapsed(2 * 60 * 60_000), "2h")
  assert.equal(elapsed(26 * 60 * 60_000), "1d")
  assert.equal(elapsed(-1), "")
})

test("the card and sidebar render durations with the same strings", () => {
  const card = runTotalTime({ timeline: [{ at: 1000 }, { at: 1000 + 90_000 }] })
  assert.equal(card.text, elapsed(90_000))
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
  assert.deepEqual(item.counts, {
    pending: 0, running: 2, waiting_ci: 1, needs_rebase: 0,
    needs_agent_fix: 0, blocked: 1, merged: 1, closed: 0,
  })
  assert.equal(item.filled, 5)
  assert.equal(item.bar.length, STATE_BAR_WIDTH)
  assert.equal(item.segments.reduce((n, s) => n + s.text.length, 0), STATE_BAR_WIDTH)
  assert.deepEqual(item.segments.map((s) => s.color), [
    "muted", "success", "info", "warning", "warning", "error", "action", "muted", "muted",
  ])
  assert.equal(item.legend, "P0 R2 C1 E0 F0 B1 M51 X0")
  assert.equal(item.summary, "2 running · 1 in CI · 1 blocked · 1 done")
  assert.equal(item.fg, "error", "a blocked run colors the bar with the error tone")
  const big = stateBarItem({ active: Array.from({ length: STATE_BAR_WIDTH + 2 }, (_, i) => makeRun({ ID: `r-${i}`, State: "running" })), recent: [] })
  assert.equal(big.filled, STATE_BAR_WIDTH)
  assert.equal(big.segments.at(-1).text, "", "a full bar leaves no muted remainder")
  assert.ok(big.content.includes(`R${STATE_BAR_WIDTH + 2}`))
  const empty = stateBarItem(EMPTY_SNAPSHOT)
  assert.equal(empty.segments[0].text, "")
  assert.equal(empty.segments.at(-1).text, "─".repeat(STATE_BAR_WIDTH))
  assert.ok(empty.content.includes("P0 R0 C0 E0 F0 B0 M0 X0"))
})

test("RUN and MERGED keep distinct colors in the bar and recent rows", () => {
  const bar = stateBarItem({
    active: [makeRun({ ID: "a", TaskID: "T-A", State: "running" })],
    recent: [makeRun({ ID: "m", TaskID: "T-M", State: "merged" })],
  })
  const running = bar.segments.find((s) => s.color === "success")
  const merged = bar.segments.find((s) => s.color === "action")
  assert.ok(running.text, "running fills a success segment")
  assert.ok(merged.text, "merged fills an accent segment")
  assert.notEqual(running.color, merged.color)

  const runningRow = runLineItems(normalizeRun(makeRun({ State: "running" })), NOW)[0]
  const mergedRow = runLineItems(normalizeRun(makeRun({ State: "merged" })), NOW, true)[0]
  assert.equal(runningRow.color, "success")
  assert.equal(mergedRow.status, "OK")
  assert.equal(mergedRow.markerColor, "action")
  assert.notEqual(runningRow.color, mergedRow.markerColor)
})

test("every run state gets a solid segment and its own legend count", () => {
  const states = [
    "pending", "running", "waiting_ci", "needs_rebase",
    "needs_agent_fix", "blocked", "merged", "closed",
  ]
  const item = stateBarItem({
    active: states.slice(0, 6).map((state, i) => makeRun({ ID: `a-${i}`, TaskID: `T-${i}`, State: state })),
    recent: states.slice(6).map((state, i) => makeRun({ ID: `r-${i}`, TaskID: `T-R-${i}`, State: state })),
  })
  assert.deepEqual(Object.keys(item.counts), states)
  assert.equal(item.total, 8)
  assert.equal(item.filled, 8)
  assert.equal(item.bar.length, STATE_BAR_WIDTH)
  assert.equal(item.segments.length, 9, "eight states plus the empty remainder")
  const stateSegments = item.segments.slice(0, 8)
  assert.ok(stateSegments.every((segment) => segment.text), "each state renders one visible segment")
  assert.ok(stateSegments.every((segment) => /^█+$/.test(segment.text)), "filled cells use a solid block")
  assert.equal(item.segments[8].text, "─".repeat(STATE_BAR_WIDTH - 8), "the rest stays empty")
  assert.equal(item.legend, "P1 R1 C1 E1 F1 B1 M1 X1")
  assert.equal(new Set(item.legend.split(" ").map((entry) => entry[0])).size, 8, "each state has its own letter")
  assert.ok(item.legend.length <= STATE_BAR_WIDTH, "the legend fits the sidebar width")
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
  assert.equal(phaseLabel(waiting, NOW), "CI wait 2m")
  const longWait = normalizeRun(makeRun({
    State: "waiting_ci", Durations: { waiting_ci_seconds: 5467, running_seconds: 5, blocked_seconds: 0 },
  }))
  assert.equal(phaseLabel(longWait, NOW), "CI wait 1h 31m")
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
  assert.equal(first.marker, "▲")
  assert.equal(first.name, title)
  assert.equal(first.status, "BLK")
  assert.equal(first.color, "error")
  assert.equal(first.runID, "run-1")
  assert.equal(second.marker, "#42")
  assert.equal(second.name, "3m")
  assert.equal(second.status, "waiting CI")
  assert.equal(second.color, "muted")
  assert.equal(second.runID, "run-1")
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
  const recent = sidebarModel(snapshot, null, NOW).recent
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

test("view helpers with an omitted snapshot use the empty snapshot", () => {
  assert.deepEqual(stateBarItem().counts, stateBarItem(EMPTY_SNAPSHOT).counts)
  assert.deepEqual(sidebarModel().items.map((i) => i.name), sidebarModel(EMPTY_SNAPSHOT).items.map((i) => i.name))
  assert.deepEqual(metricsLine(), {
    marker: "", name: "Beacon 0 active", status: "P0 R0 C0 E0 F0 B0 M0 X0", color: "muted",
  })
})

test("empty snapshot shows one queue line and counted headers", () => {
  const items = sidebarModel(EMPTY_SNAPSHOT, null, NOW).items
  assert.deepEqual(items.map((i) => i.name), [
    "Beacon", "P0 R0 C0 E0 F0 B0 M0 X0", "Active", "No active runs",
    "Queue", "Recent", "No recent runs",
  ])
  assert.deepEqual(items.filter((i) => i.sectionKey).map((i) => [i.name, i.status]), [
    ["Active", "0"], ["Recent", "0"],
  ])
})

test("an empty queue collapses into one non-expandable line", () => {
  for (const snapshot of [EMPTY_SNAPSHOT, { active: [makeRun()], recent: [], queue: [] }]) {
    const items = sidebarModel(snapshot, null, NOW).items
    const queueLines = items.filter((i) => /queue|ready/i.test(`${i.name} ${i.status}`))
    assert.deepEqual(queueLines.map((i) => [i.name, i.status]), [["Queue", "no tasks ready"]])
    assert.equal(queueLines[0].sectionKey, undefined, "an empty queue cannot be expanded")
    assert.equal(queueLines[0].marker, "", "no chevron sits on a line that cannot open")
    const rendered = renderSnapshotLines(snapshot, null, NOW).filter((line) => /queue|ready/i.test(line))
    assert.deepEqual(rendered, ["Queue no tasks ready"])
  }
})

test("ready tasks keep the expandable queue section and its count", () => {
  const snapshot = {
    active: [], recent: [],
    queue: [
      { id: "T-1", title: "Ready one", status: "ready", priority: 2 },
      { id: "T-2", title: "Blocked one", status: "waiting_deps", priority: 1 },
    ],
  }
  const collapsed = sidebarModel(snapshot, null, NOW).listItems
  const header = collapsed.find((i) => i.sectionKey === "queue")
  assert.deepEqual([header.name, header.status, header.marker], ["Queue", "2", "▶"])
  assert.ok(collapsed.some((i) => i.action === "dispatch"))
  const open = sidebarModel(snapshot, null, NOW, { active: true, queue: true, recent: true }).listItems
  assert.equal(open.find((i) => i.sectionKey === "queue").marker, "▼")
  assert.ok(open.some((i) => i.kind === "queue-title"))
})

test("a blocked queue stays expandable without a standalone ready row", () => {
  const snapshot = {
    active: [], recent: [],
    queue: [{ id: "T-1", title: "Blocked", status: "waiting_deps", priority: 1, depends_on: ["T-B"] }],
  }
  const items = sidebarModel(snapshot, null, NOW).listItems
  const header = items.find((i) => i.sectionKey === "queue")
  assert.deepEqual([header.name, header.status], ["Queue", "1"])
  assert.deepEqual(items.filter((i) => /ready/i.test(`${i.name} ${i.status}`)), [])
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
  assert.ok(model.phase.includes("Coding 5s") && model.phase.includes("CI wait 1m"))
  assert.deepEqual(model.rows.filter((i) => i.sectionKey).map((i) => i.sectionKey), ["why", "checks", "timeline", "technical"])
  const names = model.rows.map((i) => i.name ?? i.text)
  assert.ok(names.includes(longReason), "the reason is not cut to a fixed width")
  assert.ok(names.includes("ci"), "successful checks hide the word success")
  assert.equal(model.rows.find((i) => i.sectionKey === "checks").status, "1/1 · longest 28s")
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

test("queue epics collapse by default and expand through the stored map", () => {
  const snap = {
    active: [], recent: [],
    queue: [
      { id: "T-EPIC", title: "Harness epic", status: "ready", priority: 9, kind: "epic" },
      { id: "T-EPIC-01", title: "Child one", status: "available", priority: 8 },
      { id: "T-EPIC-02", title: "Child two", status: "waiting_deps", priority: 8 },
    ],
  }
  const open = { active: true, queue: true, recent: true }
  const closed = ui.sidebarModel(snap, null, NOW, open, {}).listItems
  const epic = closed.find((i) => i.kind === "queue-title" && i.cardID === "T-EPIC")
  assert.deepEqual([epic.marker, epic.status, epic.action], ["▶", "P9", "epic:T-EPIC"])
  assert.equal(closed.some((i) => i.cardID === "T-EPIC-01"), false)
  assert.equal(closed.some((i) => i.cardID === "T-EPIC-02"), false)
  const expanded = ui.sidebarModel(snap, null, NOW, open, { "T-EPIC": true }).listItems
  assert.equal(expanded.find((i) => i.kind === "queue-title" && i.cardID === "T-EPIC").marker, "▼")
  assert.ok(expanded.some((i) => i.cardID === "T-EPIC-01"))
  assert.ok(expanded.some((i) => i.cardID === "T-EPIC-02"))
  assert.equal(ui.sidebarModel(snap, null, NOW, open).listItems.some((i) => i.cardID === "T-EPIC-01"), true,
    "callers without a map keep the flat legacy list")
})

test("card children collapse by default and a child ID is drawn once", () => {
  const card = {
    id: "T-EPIC", title: "Epic", status: "ready", priority: 9, kind: "epic", depends: "T-EPIC-01",
  }
  const queue = [{ id: "T-EPIC-01", title: "Child", status: "available" }]
  const closed = ui.cardModel(card, queue, {}).rows
  const toggle = closed.find((r) => r.kind === "card-section" && r.epicKey === "T-EPIC")
  assert.deepEqual([toggle.marker, toggle.name, toggle.action], ["▶", "Children (1)", "epic:T-EPIC"])
  assert.equal(closed.some((r) => r.kind === "card-child"), false)
  assert.equal(closed.some((r) => r.kind === "card-dep" && r.name === "T-EPIC-01"), false)
  const expanded = ui.cardModel(card, queue, { "T-EPIC": true }).rows
  assert.equal(expanded.find((r) => r.kind === "card-section" && r.epicKey === "T-EPIC").marker, "▼")
  assert.ok(expanded.some((r) => r.kind === "card-child" && r.name === "T-EPIC-01 · Child"))
})

test("stored open epics are stale at startup and only a click expands them", () => {
  const saved = []
  const value = { "T-EPIC": true, "T-OTHER": true }
  const ctx = { storage: { store: (name) => {
    assert.equal(name, "beacon-epic-sections")
    return [value, async (fn) => { fn(value); saved.push({ ...value }) }]
  } } }
  const epic = ui.discardStoredEpicOpen(ui.createEpicState(ctx))
  assert.equal(epic.open["T-EPIC"], undefined, "a stored open value does not reopen an epic")
  const snap = {
    active: [], recent: [],
    queue: [
      { id: "T-EPIC", title: "Harness epic", status: "ready", priority: 9, kind: "epic" },
      { id: "T-EPIC-01", title: "Child one", status: "available", priority: 8 },
    ],
  }
  const open = { active: true, queue: true, recent: true }
  const closed = ui.sidebarModel(snap, null, NOW, open, epic.open).listItems
  assert.equal(closed.find((i) => i.cardID === "T-EPIC").marker, "▶")
  assert.equal(closed.some((i) => i.cardID === "T-EPIC-01"), false)
  epic.toggle("T-EPIC")
  assert.equal(saved.at(-1)["T-EPIC"], true, "an explicit click still persists")
  const expanded = ui.sidebarModel(snap, null, NOW, open, epic.open).listItems
  assert.equal(expanded.find((i) => i.cardID === "T-EPIC").marker, "▼")
  assert.ok(expanded.some((i) => i.cardID === "T-EPIC-01"))
})

test("an expanded epic caps child rows and keeps Recent reachable", () => {
  const epicID = "T-20261124-51f36"
  const children = Array.from({ length: 5 }, (_, i) => ({
    id: `${epicID}-${String(i + 1).padStart(2, "0")}`,
    title: `Child ${i + 1}`, status: "ready", priority: 8,
  }))
  const snap = {
    active: [], recent: [makeRun({ ID: "r1", TaskID: "T-RECENT", State: "merged" })],
    queue: [
      { id: epicID, title: "Harness epic", status: "ready", priority: 9, kind: "epic" },
      ...children,
    ],
  }
  const open = { active: true, queue: true, recent: true }
  const items = ui.sidebarModel(snap, null, NOW, open, { [epicID]: true }).listItems
  const shown = children.filter((child) => items.some((i) => i.kind === "queue-title" && i.cardID === child.id))
  assert.equal(shown.length, ui.QUEUE_CHILD_LIMIT, "only the first children render")
  assert.equal(items.some((i) => i.cardID === children[4].id), false, "later children stay hidden")
  const more = items.find((i) => i.kind === "queue-more")
  assert.equal(more.name, "+2 more")
  assert.equal(more.marker, "")
  assert.ok(items.some((i) => i.sectionKey === "recent"), "Recent stays reachable")
  assert.ok(items.some((i) => i.recent === true && i.runID === "r1"), "a recent run stays clickable")
})

test("epic state reads only true IDs from storage and persists toggles", () => {
  const saved = []
  const value = { "T-EPIC": true, stale: "yes" }
  const ctx = { storage: { store: (name) => {
    assert.equal(name, "beacon-epic-sections")
    return [value, async (fn) => { fn(value); saved.push({ ...value }) }]
  } } }
  let changes = 0
  const epic = ui.createEpicState(ctx, () => { changes += 1 })
  assert.equal(epic.open["T-EPIC"], true)
  assert.equal(epic.open.stale, undefined, "a malformed stored value is ignored")
  epic.toggle("T-NEW")
  assert.equal(saved.at(-1)["T-NEW"], true)
  epic.toggle("T-NEW")
  assert.equal(saved.at(-1)["T-NEW"], false)
  epic.toggle()
  assert.equal(changes, 2, "an empty ID does not toggle")
})

const blockedQueue = () => ({
  active: [], recent: [makeRun({ ID: "r1", TaskID: "T-RECENT", State: "merged" })],
  queue: [
    { id: "T-1", title: "Alpha one", status: "waiting_deps", priority: 5, depends_on: ["T-BLOCK-A"] },
    { id: "T-2", title: "Alpha two", status: "waiting_deps", priority: 4, depends_on: ["T-BLOCK-A"] },
    { id: "T-3", title: "Beta one", status: "waiting_deps", priority: 3, depends_on: ["T-BLOCK-B"] },
  ],
})
const queueOpen = () => ({ active: true, queue: true, recent: true })

test("blocked groups start collapsed and Recent stays reachable", () => {
  const snap = blockedQueue()
  const epicOpen = {}
  const items = ui.sidebarModel(snap, null, NOW, queueOpen(), epicOpen, ui.blockedOpenFrom(epicOpen)).listItems
  const headers = items.filter((i) => i.kind === "queue-blocker")
  assert.deepEqual(headers.map((i) => [i.marker, i.name]), [
    ["▶", "blocked by T-BLOCK-A · 2 tasks"], ["▶", "blocked by T-BLOCK-B · 1 task"],
  ])
  assert.equal(items.some((i) => i.kind === "queue-title"), false, "collapsed groups hide their task titles")
  assert.equal(items.some((i) => i.kind === "queue-dep"), false, "a dependency ID is never a repeated row")
  assert.ok(items.some((i) => i.sectionKey === "recent"), "Recent stays in the sidebar")
  assert.ok(items.some((i) => i.recent === true && i.runID === "r1"), "Recent stays clickable")
  assert.equal(items[items.findIndex((i) => i.sectionKey === "recent") - 1].kind, "spacer")
})

test("expanding one blocked group leaves the others collapsed", () => {
  const snap = blockedQueue()
  const epic = { open: {}, toggle(id) { this.open[id] = !this.open[id] } }
  ui.toggleBlockedGroup(epic, "T-BLOCK-A")
  const epicOpen = epic.open
  const items = ui.sidebarModel(snap, null, NOW, queueOpen(), epicOpen, ui.blockedOpenFrom(epicOpen)).listItems
  assert.deepEqual(items.filter((i) => i.kind === "queue-blocker").map((i) => i.marker), ["▼", "▶"])
  assert.deepEqual(items.filter((i) => i.kind === "queue-title").map((i) => i.name), ["Alpha one", "Alpha two"])
})

test("stored open blocked groups are stale at startup but a click persists", () => {
  const saved = []
  const value = { "blocked:T-BLOCK-A": true, "T-EPIC": true }
  const ctx = { storage: { store: (name) => {
    assert.equal(name, "beacon-epic-sections")
    return [value, async (fn) => { fn(value); saved.push({ ...value }) }]
  } } }
  const epic = ui.discardStoredEpicOpen(ui.createEpicState(ctx))
  assert.equal(epic.open["blocked:T-BLOCK-A"], undefined, "a stored blocked group does not reopen")
  const snap = blockedQueue()
  const open = () => ui.sidebarModel(snap, null, NOW, queueOpen(), epic.open, ui.blockedOpenFrom(epic.open)).listItems
  assert.equal(open().find((i) => i.kind === "queue-blocker").marker, "▶")
  ui.toggleBlockedGroup(epic, "T-BLOCK-A")
  assert.equal(saved.at(-1)["blocked:T-BLOCK-A"], true, "an explicit click still persists")
  assert.equal(open().find((i) => i.kind === "queue-blocker").marker, "▼")
})

test("the Queue toggle switches every blocked group and shares their state", () => {
  const snap = blockedQueue()
  const epic = { open: {}, toggle(id) { this.open[id] = !this.open[id] } }
  const model = () => ui.sidebarModel(snap, null, NOW, queueOpen(), epic.open, ui.blockedOpenFrom(epic.open)).listItems
  const toggle = model().find((i) => i.kind === "queue-blocked-toggle")
  assert.deepEqual([toggle.marker, toggle.name, toggle.action], ["▶", "Expand all blocked", "blocked-toggle"])
  ui.toggleAllBlockedGroups(epic, snap.queue)
  assert.equal(model().find((i) => i.kind === "queue-blocked-toggle").name, "Collapse all blocked")
  assert.equal(model().filter((i) => i.kind === "queue-title").length, 3)
  ui.toggleAllBlockedGroups(epic, snap.queue)
  assert.equal(model().some((i) => i.kind === "queue-title"), false)
  assert.equal(model().find((i) => i.kind === "queue-blocked-toggle").name, "Expand all blocked")
})

test("an epic inside an expanded blocked group still collapses its children", () => {
  const epicID = "T-EPIC"
  const snap = {
    active: [], recent: [],
    queue: [
      { id: epicID, title: "Harness epic", status: "ready", priority: 9, kind: "epic", depends_on: ["T-BLOCK"] },
      { id: `${epicID}-01`, title: "Child one", status: "waiting_deps", priority: 8 },
      { id: `${epicID}-02`, title: "Child two", status: "waiting_deps", priority: 8 },
    ],
  }
  const open = queueOpen()
  const view = (map) => ui.sidebarModel(snap, null, NOW, open, map, ui.blockedOpenFrom(map)).listItems
  const collapsed = view({ "blocked:T-BLOCK": true })
  assert.deepEqual(collapsed.filter((i) => i.kind === "queue-blocker").map((i) => i.name), ["blocked by T-BLOCK · 1 task"])
  assert.ok(collapsed.some((i) => i.kind === "queue-title" && i.cardID === epicID))
  assert.equal(collapsed.some((i) => i.cardID === `${epicID}-01`), false, "children of a collapsed epic stay hidden")
  const expanded = view({ "blocked:T-BLOCK": true, [epicID]: true })
  assert.deepEqual(expanded.filter((i) => i.kind === "queue-blocker").map((i) => i.name), ["blocked by T-BLOCK · 3 tasks"])
  assert.deepEqual(expanded.filter((i) => i.kind === "queue-title").map((i) => i.name), ["Harness epic", "Child one", "Child two"])
})

test("an expanded blocked group caps rows and keeps Recent clickable", () => {
  const children = Array.from({ length: 5 }, (_, i) => ({
    id: `T-${i + 1}`, title: `Task ${i + 1}`, status: "waiting_deps", priority: 9 - i, depends_on: ["T-BLOCK"],
  }))
  const snap = { active: [], recent: [makeRun({ ID: "r1", TaskID: "T-RECENT", State: "merged" })], queue: children }
  const epicOpen = { "blocked:T-BLOCK": true }
  const items = ui.sidebarModel(snap, null, NOW, queueOpen(), epicOpen, ui.blockedOpenFrom(epicOpen)).listItems
  const shown = children.filter((c) => items.some((i) => i.kind === "queue-title" && i.cardID === c.id))
  assert.equal(shown.length, ui.QUEUE_CHILD_LIMIT, "only the first members render")
  assert.equal(items.find((i) => i.kind === "queue-more").name, "+2 more")
  assert.ok(items.some((i) => i.sectionKey === "recent"))
  assert.ok(items.some((i) => i.recent === true && i.runID === "r1"))
})

test("one snapshot renders the same through every public entry point", () => {
  const snapshot = {
    active: [
      makeRun({ ID: "a1", State: "running" }),
      makeRun({ ID: "a2", State: "waiting_ci" }),
      makeRun({ ID: "a3", State: "blocked", Reason: "dirty worktree" }),
    ],
    recent: [makeRun({ ID: "r1", TaskID: "T-R", State: "merged" })],
    queue: [
      { id: "T-E", title: "Epic", status: "ready", priority: 9, kind: "epic" },
      { id: "T-E-1", title: "Child", status: "available", priority: 8 },
    ],
    metrics: { merge_count: 12 },
  }
  const model = sidebarModel(snapshot, null, NOW)
  assert.deepEqual(model.active.map((r) => r.id), ["a1", "a2", "a3"])
  assert.deepEqual(model.recent.map((r) => r.id), ["r1"])
  assert.deepEqual(model.counts, stateBarItem(snapshot).counts)
  const lines = model.items
    .map((i) => [i.marker, i.name, i.status].filter((x) => x !== "" && x != null).join(" "))
  assert.deepEqual(lines, renderSnapshotLines(snapshot, null, NOW))
})

