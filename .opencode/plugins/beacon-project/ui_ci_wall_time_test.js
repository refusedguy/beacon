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

test("a failed CI summary keeps its original wording", () => {
  const checks = [
    { name: "vet", state: "success", durationSeconds: 28 },
    { name: "lint", state: "failure", durationSeconds: 20 },
    { name: "build", state: "success", durationSeconds: 23 },
    { name: "test", state: "success", durationSeconds: 26 },
  ]
  assert.equal(checksSummary(checks), "1 failed · 3/4")
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
