// Detail panel slot. The hero card, action buttons and scrollbox section rows
// are fixed nodes built in one render callback. apply() changes props only, so
// a collapse, a refresh or a new selection never allocates a node.
import { ACTION_NAMES, DETAIL_OPEN } from "./ui_view.js"
import { createBox, createLineMount, createText } from "./ui_nodes.js"

export const DETAIL_LINES = 80
const ACTION_LABEL = Object.freeze({
  retry: "↻ Retry", approve: "✔ Approve", cancel: "✖ Cancel", copy: "⧉ Copy ID",
})
const label = (text) => text.padEnd(10, " ")

export function createDetailMount(ui, deps) {
  const { insert } = ui
  const { color, BOLD, onAction, onToggle, onClose, setProp } = deps
  const set = setProp ?? ((node, key, value) => { node.props[key] = value })
  const container = createBox(ui, { flexDirection: "column", minWidth: 0, flexGrow: 1 })
  const header = createBox(ui, { flexDirection: "row", justifyContent: "space-between", minWidth: 0 })
  const title = createText(ui, { content: "Beacon", fg: color("base"), bold: true })
  set(title, "attributes", BOLD)
  insert(header, title)
  const controls = createBox(ui, { flexDirection: "row", gap: 1, minWidth: 0 })
  const hint = createText(ui, { content: "ctrl+g", fg: color("action"), onMouseUp: onToggle })
  const close = createText(ui, { content: "✕", fg: color("error"), onMouseUp: onClose })
  insert(controls, hint); insert(controls, close)
  insert(header, controls)
  insert(container, header)

  const card = createBox(ui, { border: true, borderStyle: "single", borderColor: color("action"), flexDirection: "column", minWidth: 0 })
  const badgeRow = createBox(ui, { flexDirection: "row", justifyContent: "space-between", gap: 1, minWidth: 0 })
  const badge = createText(ui, { content: "", fg: color("base"), bold: true })
  set(badge, "attributes", BOLD)
  const pr = createText(ui, { content: "", fg: color("info") })
  insert(badgeRow, badge); insert(badgeRow, pr)
  insert(card, badgeRow)
  // A non-growing wrapped text node keeps its own height inside the column
  // card. A growing one collapsed to zero height in the real host.
  const heroTitle = createText(ui, { content: "", fg: color("base"), wrap: true })
  set(heroTitle, "attributes", BOLD)
  insert(card, heroTitle)
  const kvRow = (name) => {
    const row = createBox(ui, { flexDirection: "row", gap: 1, minWidth: 0 })
    const key = createText(ui, { content: label(name), fg: color("muted") })
    const value = createText(ui, { content: "", fg: color("base"), wrap: true, grow: true })
    insert(row, key); insert(row, value)
    insert(card, row)
    return value
  }
  const taskValue = kvRow("Task")
  const branchValue = kvRow("Branch")
  const phaseValue = kvRow("Phase")
  const actions = createBox(ui, { flexDirection: "row", gap: 1, minWidth: 0 })
  const buttons = Object.create(null)
  for (const name of ACTION_NAMES) {
    const node = createText(ui, {
      content: ACTION_LABEL[name], fg: color(name === "cancel" ? "error" : "action"),
      name: `beacon-action-${name}`, onMouseUp: () => onAction(name),
    })
    // Copy ID must be visible from the first mount. The host does not reveal a
    // button that was created with visible=false on a merged run.
    set(node, "visible", name === "copy")
    buttons[name] = node
    insert(actions, node)
  }
  insert(card, actions)
  insert(container, card)

  const scroll = createBox(ui, { scroll: true, flexGrow: 1, minWidth: 0 })
  const body = createLineMount(ui, { ...deps, maxLines: DETAIL_LINES, wrap: true })
  body.node.forEach((node) => insert(scroll, node))
  insert(container, scroll)

  const apply = (model = {}, run = null) => {
    set(badge, "content", model.badge ?? "")
    set(badge, "fg", color(model.badgeColor ?? "base"))
    set(pr, "content", model.pr ?? "")
    set(branchValue, "content", model.branch ?? "")
    set(heroTitle, "content", model.title ?? "")
    set(taskValue, "content", model.task ?? "")
    set(phaseValue, "content", model.phase ?? "")
    const available = new Set(model.actions ?? [])
    set(actions, "visible", Boolean(run))
    for (const name of ACTION_NAMES) {
      set(buttons[name], "visible", Boolean(run) && (name === "copy" || available.has(name)))
    }
    body.apply(model.rows ?? [])
  }
  return { node: container, apply, buttons, open: DETAIL_OPEN }
}
