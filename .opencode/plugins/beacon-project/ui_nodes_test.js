// Run: node --test .opencode/plugins/beacon-project/ui_nodes_test.js
// Regression for the sidebar crash after PR #2362: a real @opentui/solid box
// has no .children array, so apply() must read its cells from the local pool
// built in createLineMount, never from box.children.
import { test } from "node:test"
import assert from "node:assert/strict"
import { createLineMount } from "./ui_nodes.js"
import { createSidebarMount } from "./ui_sidebar.js"
import { sidebarModel } from "./ui_view.js"
import { EMPTY_SNAPSHOT, NOW, hostModule, makeRun } from "./ui_test_helpers.js"

const deps = {
  color: (key) => `theme.${key}`,
  BOLD: 1,
  hoverBg: "theme.hover",
}

test("line mount applies cells when boxes have no children array", () => {
  const created = []
  const ui = hostModule(created)
  const mount = createLineMount(ui, { ...deps, onItem: () => {}, maxLines: 2 })
  const cells = created.filter((node) => node.tag === "text")
  const before = created.length

  assert.doesNotThrow(() => mount.apply([
    { marker: "▲", text: "run-1", status: "running", color: "success" },
    { marker: "", text: "Dispatch", status: "", color: "action", bold: true },
  ]))
  assert.equal(created.length, before, "apply must not call createElement or insert")
  assert.equal(cells.length, 6, "two lines of three cells are built once")
  assert.deepEqual(cells.map((cell) => cell.props.content), [
    "▲", "run-1", "running", "", "Dispatch", "",
  ])
  assert.equal(cells[1].props.fg, "theme.success")
  assert.equal(cells[4].props.attributes, 1, "bold item sets the name attribute")
})

test("line mount hides empty rows against a host without box.children", () => {
  const created = []
  const mount = createLineMount(hostModule(created), { ...deps, maxLines: 2 })
  const boxes = created.filter((node) => node.tag === "box")

  assert.doesNotThrow(() => mount.apply([{ marker: "", name: "only", color: "base" }]))
  assert.equal(boxes[0].props.visible, true)
  assert.equal(boxes[1].props.visible, false)
})

test("sidebar renders and refreshes against a host without box.children", () => {
  const created = []
  const sidebar = createSidebarMount(hostModule(created), { ...deps, onItem: () => {} })
  const before = created.length
  const texts = created.filter((node) => node.tag === "text")

  assert.doesNotThrow(() => sidebar.apply(sidebarModel({
    active: [makeRun({ ID: "r-1", State: "running" })], recent: [],
  }, null, NOW)))
  assert.ok(texts.some((node) => node.props.content === "Beacon"), "title cell gets content")
  assert.ok(texts.some((node) => String(node.props.content ?? "").includes("running")), "status cell gets content")

  assert.doesNotThrow(() => sidebar.apply(sidebarModel({
    active: [makeRun({ ID: "r-1", State: "blocked" })], recent: [],
  }, null, NOW)))
  assert.equal(created.length, before, "refresh must not create nodes")
  assert.ok(texts.some((node) => String(node.props.content ?? "").includes("blocked")), "refresh updates the status")
})

test("sidebar renders an empty snapshot without a children array", () => {
  const created = []
  const sidebar = createSidebarMount(hostModule(created), { ...deps, onItem: () => {} })
  assert.doesNotThrow(() => sidebar.apply(sidebarModel(EMPTY_SNAPSHOT, null, NOW)))
})
