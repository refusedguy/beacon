// Regressions for T-20261124-5875d3a4: parallel CI jobs, the waiting-ci label
// and the read-only card panel from the live screenshot.
// Run: node --test .opencode/plugins/beacon-project/*_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  CARD_LINES, cardModel, cardRelativePath, checksSummary, createCardMount,
  detailModel, fixPrompt, normalizeRun, phaseLabel, readCard,
} from "./ui.js"
import { NOW, makeRun, solidModule } from "./ui_test_helpers.js"

const color = (key) => `theme.${key}`
const cardDeps = (onClose = () => {}) => ({
  color, BOLD: 1, onCopy: () => {}, onClose,
  setProp: (node, key, value) => { node.props[key] = value },
})
function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const hit = findText(child, content)
    if (hit) return hit
  }
  return null
}

test("CI summary reports the longest parallel job, not the summed durations", () => {
  const durations = [28, 20, 14, 23, 26]
  const checks = durations.map((durationSeconds, i) => ({
    name: `job-${i}`, state: "success", durationSeconds,
  }))
  const summary = checksSummary(checks)
  assert.equal(summary, "5/5 · longest 28s")
  assert.match(summary, /28s/)
  assert.doesNotMatch(summary, /1m51s|111s/, "the 111s sum never reaches the header")
})

test("a failed CI summary names the failure and keeps the longest-job fallback", () => {
  const checks = [
    { name: "vet", state: "success", durationSeconds: 28 },
    { name: "lint", state: "failure", durationSeconds: 20 },
    { name: "build", state: "success", durationSeconds: 23 },
    { name: "test", state: "success", durationSeconds: 26 },
  ]
  assert.equal(checksSummary(checks), "1 failed · 3/4 · longest 28s")
})

test("the waiting-ci phase reads as a wait, not CI execution time", () => {
  const waiting = normalizeRun(makeRun({
    State: "waiting_ci",
    Durations: { waiting_ci_seconds: 120, running_seconds: 0, blocked_seconds: 0 },
  }))
  assert.equal(phaseLabel(waiting, NOW), "CI wait 2m")
  assert.match(detailModel(waiting, {}, NOW).phase, /CI wait 2m/, "the hero Phase text says CI wait")
  const running = normalizeRun(makeRun({
    State: "running",
    Durations: { waiting_ci_seconds: 0, running_seconds: 5, blocked_seconds: 0 },
  }))
  assert.equal(phaseLabel(running, NOW), "run 5s", "other phases stay unchanged")
})

test("the card scroll box stays bounded while the body has more lines than fit", () => {
  const created = []
  const mount = createCardMount(solidModule(created), cardDeps())
  const body = Array.from({ length: 120 }, (_, i) => `body line ${i}`).join("\n")
  mount.apply(cardModel({ id: "T-42", title: "t", priority: "7", status: "ready", depends: "", body }))
  const scroll = created.find((node) => node.tag === "scrollbox")
  assert.ok(scroll, "the scroll node is mounted")
  assert.equal(scroll.props.flexGrow, 1)
  assert.equal(scroll.props.minHeight, 0, "minHeight: 0 lets the scroll box shrink")
  const plate = created.find((node) => node.props.borderStyle === "rounded")
  assert.ok(plate, "the plate keeps its rounded border")
  assert.equal(plate.props.flexShrink, undefined, "the plate yields instead of forcing its full height")
  assert.equal(mount.body.node.length, CARD_LINES, "the pooled body matches the panel height")
  assert.ok(body.split(/\r?\n/).length > mount.body.node.length, "the body overflows")
})

test("the card header carries ctrl+g and a close button wired to onClose", () => {
  let closes = 0
  const mount = createCardMount(solidModule([]), cardDeps(() => { closes += 1 }))
  mount.apply(cardModel(null))
  const hint = findText(mount.node, "ctrl+g")
  const close = findText(mount.node, "✕")
  assert.ok(hint, "the ctrl+g hint is present")
  assert.ok(close, "the close button is present")
  assert.ok(findText(mount.node, "read-only"), "read-only stays visible")
  hint.props.onMouseUp()
  close.props.onMouseUp()
  assert.equal(closes, 2, "both header controls call onClose")
})

test("pending checks are waits, never failures", () => {
  const states = ["pending", "in_progress", "queued", "unrecognised"]
  const checks = states.map((state, i) => ({ name: `job-${i}`, state }))
  const summary = checksSummary(checks)
  assert.equal(summary, "0/4 · 4 pending")
  assert.doesNotMatch(summary, /failed/, "an unknown state is not a failed check")
})

test("skipped checks are counted but never called failed", () => {
  const checks = [{ name: "a", state: "skipped" }, { name: "b", state: "neutral" }]
  assert.equal(checksSummary(checks), "0/2 · 2 skipped")
  assert.equal(checksSummary([]), "", "no checks keep the header empty")
  assert.equal(checksSummary(null), "")
})

test("mixed check states separate failures, passes, skips and waits", () => {
  const checks = [
    { name: "vet", state: "success" },
    { name: "unit", state: "success" },
    { name: "lint", state: "failure" },
    { name: "e2e", state: "skipped" },
  ]
  assert.equal(checksSummary(checks), "1 failed · 2/4 · 1 skipped")
  const mixed = [
    { name: "vet", state: "success" },
    { name: "unit", state: "error" },
    { name: "lint", state: "queued" },
    { name: "e2e", state: "neutral" },
  ]
  assert.equal(checksSummary(mixed), "1 failed · 1/4 · 1 skipped · 1 pending")
})

test("the header uses Go wall time when the snapshot provides it", () => {
  const checks = [
    { name: "vet", state: "success", durationSeconds: 9 },
    { name: "unit", state: "success", durationSeconds: 11 },
  ]
  const run = normalizeRun(makeRun({ ci_wall_seconds: 31, Checks: checks }))
  assert.equal(run.ciWallSeconds, 31)
  assert.equal(checksSummary(run.checks), "2/2 · 31s")
  assert.equal(checksSummary(checks, 31), "2/2 · 31s")
  const header = detailModel(run, {}, NOW).rows.find((i) => i.sectionKey === "checks")
  assert.equal(header.status, "2/2 · 31s", "the wall time reaches the live detail header")
})

test("a missing or zero wall time falls back to the longest check", () => {
  const checks = [
    { name: "vet", state: "success", durationSeconds: 9 },
    { name: "unit", state: "success", durationSeconds: 11 },
  ]
  assert.equal(checksSummary(checks), "2/2 · longest 11s")
  for (const wall of [undefined, 0]) {
    const run = normalizeRun(makeRun({ ci_wall_seconds: wall, Checks: checks }))
    const header = detailModel(run, {}, NOW).rows.find((i) => i.sectionKey === "checks")
    assert.equal(header.status, "2/2 · longest 11s")
  }
  const empty = normalizeRun(makeRun({ ci_wall_seconds: 31 }))
  assert.equal(detailModel(empty, {}, NOW).rows.find((i) => i.sectionKey === "checks").status, "")
})

test("the Path row and fix prompt use the path relative to the project root", () => {
  const card = readCard("/p", "T-42", {
    existsSync: () => true,
    readFileSync: () => "---\nid: T-42\ntitle: Fix the thing\nstatus: ready\npriority: 7\n---\nGoal: work.\n",
  })
  assert.equal(card.path, "/p/docs/tasks/T-42.md", "the absolute path stays internal")
  assert.equal(cardRelativePath(card), "docs/tasks/T-42.md")
  const pathRow = cardModel(card).rows.find((row) => row.marker.trim() === "Path")
  assert.equal(pathRow.name, "docs/tasks/T-42.md")
  const prompt = fixPrompt(card)
  assert.match(prompt, /in docs\/tasks\/T-42\.md\./)
  assert.doesNotMatch(prompt, /\/p\//, "no absolute prefix reaches the clipboard")
})
