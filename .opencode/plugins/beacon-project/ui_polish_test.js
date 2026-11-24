// Final UI polish contract: sidebar state bar, dense sections, hero geometry
// and readable button contrast. Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  ACTION_NAMES, DETAIL_OPEN, EMPTY_SNAPSHOT, SIDEBAR_OPEN, STATE_BAR_WIDTH,
  createDetailMount, createSidebarMount, daemonAlertItem, detailModel,
  normalizeRun, runLineItems, sidebarModel, stateBarItem, truncate,
  truncateSidebarTitle,
} from "./ui.js"
import { NOW, hostModule, makeRun, solidModule } from "./ui_test_helpers.js"

const color = (key) => `theme.${key}`
const detailDeps = (onAction = () => {}) => ({
  color, BOLD: 1, setProp: (node, key, value) => { node.props[key] = value },
  onAction, onToggle: () => {}, onClose: () => {},
})
const sidebarDeps = (onItem = () => {}) => ({
  color, BOLD: 1, onItem, hoverBg: "theme.hover",
})
function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const hit = findText(child, content)
    if (hit) return hit
  }
  return null
}

test("hero geometry keeps every fixed box at flexShrink 0", () => {
  const mount = createDetailMount(solidModule([]), detailDeps())
  const run = normalizeRun(makeRun({ State: "blocked" }))
  mount.apply(detailModel(run, {}, NOW), run)
  const [badgeRow, heroTitle, fields, nextStep, actions, confirmBox] = mount.heroCard.children
  const fixed = {
    header: mount.node.children[0], heroCard: mount.heroCard, badgeRow,
    fields, nextStep, actions, confirmBox,
  }
  for (const [name, node] of Object.entries(fixed)) {
    assert.equal(node.props.flexShrink, 0, `${name} does not collapse under the scrollbox`)
  }
  for (const [i, row] of fields.children.entries()) {
    assert.equal(row.props.flexShrink, 0, `kvRow ${i} keeps its height`)
  }
  assert.equal(heroTitle.props.flexShrink, 0, "the hero title keeps its height")
  assert.equal(mount.node.children.at(-1).props.flexGrow, 1, "only the scrollbox grows")
})

test("buttons use raised plates, readable foregrounds and a darker hero", () => {
  const mount = createDetailMount(solidModule([]), detailDeps())
  const run = normalizeRun(makeRun({
    State: "running", PRURL: "https://github.com/o/r/pull/42",
  }))
  mount.apply(detailModel(run, {}, NOW), run)
  assert.equal(Object.keys(mount.buttonBoxes).length, ACTION_NAMES.length)
  const expected = {
    retry: "success", approve: "success", cancel: "error",
    pr: "base", help: "action", copy: "base",
  }
  for (const name of ACTION_NAMES) {
    const box = mount.buttonBoxes[name]
    const label = mount.buttons[name]
    assert.equal(box.props.backgroundColor, "theme.raised", `${name} sits on a raised plate`)
    assert.equal(box.props.flexShrink, 0, `${name} does not shrink`)
    assert.equal(label.props.fg, `theme.${expected[name]}`, `${name} uses a readable tone`)
    assert.notEqual(label.props.fg, box.props.backgroundColor, `${name} never hides its text`)
    assert.notEqual(box.props.backgroundColor, mount.heroCard.props.backgroundColor,
      `${name} contrasts with the hero card`)
    box.props.onMouseOver()
    assert.equal(box.props.backgroundColor, "theme.panel", `${name} hovers to a verified token`)
    box.props.onMouseOut()
    assert.equal(box.props.backgroundColor, "theme.raised", `${name} restores its plate`)
  }
  assert.equal(mount.heroCard.props.borderColor, "theme.action")
})

test("English confirm box uses raised plates, a visible spacer and flexShrink 0", () => {
  const actions = []
  const mount = createDetailMount(solidModule([]), detailDeps((name, opts) => actions.push([name, opts])))
  const run = normalizeRun(makeRun({ State: "running" }))
  mount.apply(detailModel(run, {}, NOW), run)
  assert.ok(findText(mount.confirmBox, "Cancel this run?"), "the prompt is in English")
  assert.ok(findText(mount.confirmBox, "[ Confirm Cancel ]"))
  assert.ok(findText(mount.confirmBox, "[ Keep ]"))
  assert.equal(mount.confirmYes.props.fg, "theme.error")
  assert.equal(mount.confirmNo.props.fg, "theme.muted")
  assert.equal(mount.confirmSpacer.props.content, " ")
  assert.equal(mount.confirmSpacer.props.flexShrink, 0)
  const confirmActions = mount.confirmBox.children.at(-1)
  assert.deepEqual(confirmActions.children.map((box) => box.props.backgroundColor), [
    "theme.raised", undefined, "theme.raised",
  ])
  assert.ok(confirmActions.children.every((box) => box.props.flexShrink === 0))
  mount.buttonBoxes.cancel.props.onMouseUp()
  assert.equal(mount.confirmBox.props.visible, true)
  mount.confirmNo.props.onMouseUp()
  assert.equal(mount.confirmBox.props.visible, false)
  mount.buttonBoxes.cancel.props.onMouseUp()
  mount.confirmYes.props.onMouseUp()
  assert.deepEqual(actions, [["cancel", { confirmed: true }]])
  assert.equal(mount.confirmBox.props.visible, false)
})

test("state bar uses thin glyphs, never exceeds 30 cells and hides empty segments", () => {
  assert.ok(STATE_BAR_WIDTH <= 30)
  const busy = stateBarItem({
    active: Array.from({ length: 40 }, (_, i) => makeRun({ ID: `r-${i}`, State: "running" })),
    recent: [],
  })
  assert.equal(busy.bar.length, STATE_BAR_WIDTH)
  assert.equal(busy.segments.reduce((n, s) => n + s.text.length, 0), STATE_BAR_WIDTH)
  assert.ok(!busy.bar.includes("…"), "no middle ellipsis")
  assert.ok(!/[█░]/.test(busy.bar), "thick blocks and shade blocks are gone")
  const sidebar = createSidebarMount(solidModule([]), sidebarDeps())
  sidebar.apply(sidebarModel(busy, null, NOW))
  const cells = sidebar.bar.children
  assert.equal(cells.length, 5)
  const nonEmpty = busy.segments.filter((s) => s.text).length
  assert.equal(cells.filter((cell) => cell.props.visible === true).length, nonEmpty)
  for (const cell of cells) {
    assert.equal(cell.props.flexShrink, 0)
    assert.equal(cell.props.truncate, false)
  }
  const empty = stateBarItem(EMPTY_SNAPSHOT)
  sidebar.apply(sidebarModel(EMPTY_SNAPSHOT, null, NOW))
  assert.equal(sidebar.bar.children[0].props.visible, false, "empty segments take no column")
  assert.equal(sidebar.bar.children.at(-1).props.visible, true)
  assert.equal(empty.bar, "─".repeat(STATE_BAR_WIDTH))
  assert.equal(sidebar.legend.props.content, "R0 C0 B0 M0")
  assert.equal(sidebar.legend.props.paddingLeft, undefined, "legend starts at column 0")
  assert.equal(sidebar.legend.props.marginLeft, undefined)
})

test("Queue header matches Active and Recent without a duplicate count", () => {
  const model = sidebarModel(EMPTY_SNAPSHOT, null, NOW)
  const headers = model.listItems.filter((i) => i.sectionKey)
  assert.deepEqual(headers.map((i) => [i.name, i.status, i.statusColor]), [
    ["Active", "0", "muted"], ["Queue", "0", "muted"], ["Recent", "0", "muted"],
  ])
  for (const header of headers) assert.equal(/\(/.test(header.name), false)
  assert.equal(model.listItems.find((i) => i.sectionKey === "queue").marker, "▶")
})

test("an idle sidebar shows one muted line and no dispatch plate", () => {
  const idle = sidebarModel(EMPTY_SNAPSHOT, null, NOW).listItems
  const lines = idle.filter((i) => /Idle|No active runs|No tasks ready/.test(i.name))
  assert.deepEqual(lines.map((i) => i.name), ["Idle · no ready tasks"])
  assert.equal(lines[0].color, "muted")
  assert.equal(idle.some((i) => i.action === "dispatch"), false)
  const ready = sidebarModel({
    active: [], recent: [], queue: [{ id: "T-2", title: "Queued", status: "ready", priority: 1 }],
  }, null, NOW).listItems
  assert.ok(ready.some((i) => i.name === "No active runs"))
  assert.ok(ready.some((i) => i.action === "dispatch"), "the plate returns with ready work")
})

test("recent rows use calm markers and a two-space second line", () => {
  const model = sidebarModel({
    active: [],
    recent: [
      makeRun({ ID: "m", TaskID: "T-M", State: "merged", title: "Merged work" }),
      makeRun({ ID: "c", TaskID: "T-C", State: "closed", title: "Closed work", Reason: "PR closed" }),
      makeRun({ ID: "d", TaskID: "T-D", State: "blocked", title: "Duplicate work", Reason: "PR already merged" }),
    ],
  }, null, NOW)
  const rows = model.listItems.filter((i) => i.recent && (i.marker === "✔" || i.marker === "✖"))
  assert.deepEqual(rows.map((i) => [i.marker, i.status, i.markerColor]), [
    ["✔", "OK", "success"], ["✖", "ERR", "error"], ["✖", "DUP", "muted"],
  ])
  assert.ok(rows.every((i) => i.markerSpace === true && i.nameBold === false && i.nameColor === "muted"))
  const second = model.listItems.filter((i) => i.recent && i.marker.startsWith("#"))
  assert.equal(second.length, 3)
  assert.ok(second.every((i) => i.markerSpace === 2 && i.markerColor === "muted"))
  assert.equal(runLineItems(normalizeRun(makeRun({ State: "merged" })), NOW, true)[1].markerSpace, 2)
  const sidebar = createSidebarMount(solidModule([]), sidebarDeps())
  sidebar.apply(model)
  const refRow = sidebar.node.children.find((box) => box.item?.recent && box.item.marker === "#42")
  assert.equal(refRow.children[0].props.content, "#42  ", "the two-space gap is in the live cell")
})

test("CI checks hide success, keep short failures and carry a right summary", () => {
  const checks = [
    { name: "vet", state: "success", duration_seconds: 19 },
    { name: "unit", state: "success", duration_seconds: 19 },
    { name: "lint", state: "success", duration_seconds: 19 },
    { name: "build", state: "success", duration_seconds: 19 },
    { name: "e2e", state: "success", duration_seconds: 19 },
  ]
  const run = normalizeRun(makeRun({ Checks: checks }))
  const model = detailModel(run, {}, NOW)
  assert.equal(model.rows.find((i) => i.sectionKey === "checks").status, "5/5 · 1m35s")
  assert.ok(model.rows.some((i) => i.kind === "check" && i.name === "vet"))
  assert.equal(model.rows.some((i) => i.name.includes("success")), false)
  const failed = detailModel(normalizeRun(makeRun({
    Checks: [{ name: "vet", state: "success" }, { name: "e2e", state: "failure" }],
  })), {}, NOW)
  assert.equal(failed.rows.find((i) => i.sectionKey === "checks").status, "1 failed · 1/2")
  assert.ok(failed.rows.some((i) => i.name === "e2e failed"))
  const mount = createDetailMount(solidModule([]), detailDeps())
  mount.apply(model, run)
  const check = mount.body.node.find((box) => box.item?.kind === "check" && box.item.marker === "✔")
  assert.equal(check.children[0].props.content, "✔ ", "✔ never runs into the check name")
  assert.equal(check.item.markerSpace, true)
  assert.equal(check.children[2].props.fg, "theme.muted", "duration stays muted")
})

test("Why / Blocker is forced open for problem states and keeps its error card", () => {
  for (const state of ["blocked", "closed", "needs_agent_fix"]) {
    const run = normalizeRun(makeRun({ State: state, Reason: "dirty worktree" }))
    const model = detailModel(run, {}, NOW, { ...DETAIL_OPEN, why: false })
    assert.equal(model.rows.find((i) => i.sectionKey === "why").marker, "▼", state)
    assert.ok(model.rows.some((i) => i.kind === "reason" && i.name === "dirty worktree"))
    const mount = createDetailMount(solidModule([]), detailDeps())
    mount.apply(model, run)
    const reason = mount.body.node.find((box) => box.item?.kind === "reason")
    assert.equal(reason.props.borderStyle, "rounded")
    assert.equal(reason.props.borderColor, "theme.error", state)
    assert.equal(reason.props.paddingTop, 1)
    assert.equal(reason.props.paddingBottom, 1)
  }
  const merged = normalizeRun(makeRun({ State: "merged", Reason: "" }))
  const closed = detailModel(merged, {}, NOW, { ...DETAIL_OPEN, why: false })
  assert.equal(closed.rows.find((i) => i.sectionKey === "why").marker, "▶")
  assert.equal(closed.rows.some((i) => i.name === "-"), false)
})

test("Timeline keeps a padded time column and human copy that wraps", () => {
  const run = normalizeRun(makeRun({
    Timeline: [{ at: "2026-11-23T10:01:00Z", kind: "worker_started" }],
  }))
  const mount = createDetailMount(solidModule([]), detailDeps())
  mount.apply(detailModel(run, {}, NOW), run)
  const row = mount.body.node.find((box) => box.item?.kind === "timeline")
  assert.equal(row.item.marker, "10:01:00  ")
  assert.equal(row.children[0].props.fg, "theme.muted")
  assert.equal(row.children[1].props.content, "Worker started")
  assert.equal(row.children[1].props.wrapMode, "word")
  assert.equal(row.children[1].props.truncate, false)
})

test("a missing daemon raises no alert while a base incident stays clickable", () => {
  assert.equal(daemonAlertItem(null), null)
  assert.equal(daemonAlertItem({ state: "alive" }), null)
  assert.equal(sidebarModel({ active: [], recent: [] }, null, NOW).listItems.some((i) => i.banner), false)
  const picked = []
  const sidebar = createSidebarMount(solidModule([]), sidebarDeps((item) => picked.push(item)))
  const model = sidebarModel({
    active: [], recent: [],
    base_incident: { id: "inc-1", base_branch: "main", failed_checks: 1, fixer_run_id: "fixer-1" },
  }, null, NOW)
  sidebar.apply(model)
  const banner = sidebar.node.children.find((box) => box.item?.banner && box.item.runID === "fixer-1")
  assert.ok(banner, "the incident banner is rendered")
  banner.props.onMouseDown()
  assert.equal(picked[0].runID, "fixer-1")
  assert.equal(sidebarModel({ active: [], recent: [], daemon: { state: "dead", error: "lost" } },
    null, NOW).listItems.find((i) => i.banner).color, "error")
})

test("clipped titles end their ellipsis and never put it in the middle", () => {
  const long = truncateSidebarTitle("Make worktree registration refresh reliable at every exit")
  assert.ok(long.endsWith("…"), "the ellipsis is the last glyph")
  assert.equal(long.indexOf("…"), long.length - 1)
  assert.equal(truncate("abcdefghij", 5), "abcd…")
})

test("polished mounts never allocate nodes on refresh without box.children", () => {
  const created = []
  const ui = hostModule(created)
  const sidebar = createSidebarMount(ui, sidebarDeps())
  const detail = createDetailMount(ui, detailDeps())
  const run = normalizeRun(makeRun({ State: "blocked" }))
  assert.doesNotThrow(() => sidebar.apply(sidebarModel({ active: [run], recent: [] }, null, NOW)))
  assert.doesNotThrow(() => detail.apply(detailModel(run, {}, NOW), run))
  const before = created.length
  assert.doesNotThrow(() => {
    sidebar.apply(sidebarModel(EMPTY_SNAPSHOT, null, NOW))
    detail.apply(detailModel(normalizeRun(makeRun({ State: "merged" })), {}, NOW), run)
  })
  assert.equal(created.length, before)
  assert.equal(SIDEBAR_OPEN.queue, false, "the queue still starts collapsed")
})
