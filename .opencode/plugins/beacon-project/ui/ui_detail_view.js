// Detail panel slot. The hero card, action buttons and scrollbox section rows
// are fixed nodes built in one render callback. apply() changes props only, so
// a collapse, a refresh or a new selection never allocates a node.
import { ACTION_NAMES, DETAIL_OPEN } from "./ui_view.js"
import { createBox, createLineMount, createText } from "./ui_nodes.js"
import { createButtonPlate, createPanelChrome } from "./ui_card_view.js"

export const DETAIL_LINES = 80

// The real host supplies setProp; test doubles may only expose node.props.
const propSetter = (source) => source?.setProp ?? ((node, key, value) => { node.props[key] = value })
const ACTION_LABEL = Object.freeze({
  retry: "↻ Retry", approve: "✔ Approve", cancel: "✖ Cancel",
  pr: "⧉ PR URL", help: "💬 Help", copy: "⧉ Copy ID",
})
// Cancel is error text on a raised plate, never error text on an error plate.
const BUTTON_FG = Object.freeze({
  retry: "success", approve: "success", cancel: "error",
  pr: "base", help: "action", copy: "base",
})
// The detail panel owns its header labels; the shared builder owns the layout.
const HEADER_LABEL = Object.freeze({ hint: "ctrl+g", close: "✕" })
export function createDetailMount(ui, deps) {
  const { insert } = ui
  const { color, BOLD, onAction, onToggle, onClose } = deps
  const set = propSetter(deps)
  const chrome = createPanelChrome(ui, deps, {
    title: "Beacon", onHint: onToggle, onClose, padding: 8,
    hintLabel: HEADER_LABEL.hint, closeLabel: HEADER_LABEL.close,
  })
  const { container, fieldKey } = chrome

  // The hero uses the darker panel token so the raised button plates stand out.
  const heroCard = createBox(ui, {
    border: true, borderStyle: "rounded", borderColor: color("action"),
    backgroundColor: color("panel"), paddingLeft: 1, paddingRight: 1,
    paddingTop: 1, paddingBottom: 1, marginTop: 1, flexShrink: 0,
    flexDirection: "column", minWidth: 0, overflow: "hidden",
  })
  const badgeRow = createBox(ui, {
    flexDirection: "row", justifyContent: "space-between", minWidth: 0, flexShrink: 0,
    flexWrap: "wrap", rowGap: 1,
  })
  const badge = createText(ui, { content: "", fg: color("base"), bold: true })
  set(badge, "attributes", BOLD)
  const pr = createText(ui, { content: "", fg: color("info") })
  const fixer = createText(ui, { content: "", fg: color("warning"), bold: true })
  set(fixer, "attributes", BOLD)
  insert(badgeRow, badge); insert(badgeRow, pr); insert(badgeRow, fixer)
  insert(heroCard, badgeRow)

  // A non-growing wrapped text node keeps its own height inside the column
  // card. A growing one collapsed to zero height in the real host.
  const heroTitle = createText(ui, { content: "", fg: color("base"), wrap: true })
  set(heroTitle, "attributes", BOLD)
  insert(heroCard, heroTitle)

  const fields = createBox(ui, { flexDirection: "column", minWidth: 0, marginTop: 1, flexShrink: 0 })
  const kvRow = (name) => {
    const row = createBox(ui, { flexDirection: "row", minWidth: 0, flexShrink: 0 })
    const key = createText(ui, { content: fieldKey(name), fg: color("muted") })
    const spacer = createText(ui, { content: " ", fg: color("muted") })
    const value = createText(ui, { content: "", fg: color("base"), wrap: true, grow: true })
    insert(row, key); insert(row, spacer); insert(row, value)
    insert(fields, row)
    return value
  }
  const taskValue = kvRow("Task")
  const branchValue = kvRow("Branch")
  const phaseValue = kvRow("Phase")
  insert(heroCard, fields)

  const nextStep = createText(ui, { content: "", fg: color("warning"), wrap: true })
  set(nextStep, "flexShrink", 0)
  insert(heroCard, nextStep)

  // Each action is a padded raised plate. Copy ID stays mounted so merged and
  // closed runs can still be copied. Cancel shows an inline confirmation.
  const showConfirm = (visible) => set(confirmBox, "visible", visible)
  const actions = createBox(ui, {
    flexDirection: "row", minWidth: 0, marginTop: 1, flexShrink: 0,
    flexWrap: "wrap", rowGap: 1,
  })
  const buttons = Object.create(null)
  const buttonBoxes = Object.create(null)
  for (const name of ACTION_NAMES) {
    const { box, text } = createButtonPlate(ui, deps, {
      content: ACTION_LABEL[name], fg: BUTTON_FG[name], name: `beacon-action-${name}`,
      marginRight: name === "copy" ? 0 : 1, boxClick: true,
      onMouseUp: () => (name === "cancel" ? showConfirm(true) : onAction(name)),
      visible: name === "copy",
    })
    insert(actions, box)
    buttons[name] = text
    buttonBoxes[name] = box
  }
  insert(heroCard, actions)

  const confirmBox = createBox(ui, {
    border: true, borderStyle: "rounded", borderColor: color("error"),
    backgroundColor: color("raised"), paddingLeft: 1, paddingRight: 1,
    paddingTop: 1, paddingBottom: 1, marginTop: 1, flexShrink: 0,
    flexDirection: "column", minWidth: 0, visible: false,
  })
  const confirmText = createText(ui, { content: "Cancel this run?", fg: color("error"), bold: true })
  set(confirmText, "attributes", BOLD)
  const confirmActions = createBox(ui, {
    flexDirection: "row", minWidth: 0, marginTop: 1, flexShrink: 0,
  })
  const confirmYesPlate = createButtonPlate(ui, deps, {
    content: "[ Confirm Cancel ]", fg: "error", marginRight: 1, hover: false,
    onMouseUp: () => { showConfirm(false); onAction("cancel", { confirmed: true }) },
  })
  const confirmYes = confirmYesPlate.text
  const confirmSpacer = createText(ui, { content: " ", fg: color("muted") })
  set(confirmSpacer, "flexShrink", 0)
  const confirmNoPlate = createButtonPlate(ui, deps, {
    content: "[ Keep ]", fg: "muted", hover: false, onMouseUp: () => showConfirm(false),
  })
  const confirmNo = confirmNoPlate.text
  insert(confirmActions, confirmYesPlate.box)
  insert(confirmActions, confirmSpacer)
  insert(confirmActions, confirmNoPlate.box)
  insert(confirmBox, confirmText); insert(confirmBox, confirmActions)
  insert(heroCard, confirmBox)
  insert(container, heroCard)

  const scroll = createBox(ui, { scroll: true, flexGrow: 1, minWidth: 0, minHeight: 0 })
  const body = createLineMount(ui, { ...deps, maxLines: DETAIL_LINES, wrap: true })
  body.node.forEach((node) => insert(scroll, node))
  insert(container, scroll)

  // Section cards and padded keys are host-side props on the pooled row box.
  // Nothing here reads box.children, so the real host without children works.
  const decorate = (box, item) => {
    for (const key of ["border", "borderStyle", "borderColor", "backgroundColor",
      "paddingLeft", "paddingRight", "paddingTop", "paddingBottom", "marginTop"]) set(box, key, undefined)
    if (!item) return
    if (item.kind === "section") set(box, "marginTop", item.name === "Why / Blocker" ? 0 : 1)
    if (item.kind === "reason") {
      set(box, "border", true)
      set(box, "borderStyle", "rounded")
      set(box, "borderColor", color(item.color === "error" ? "error" : "action"))
      set(box, "backgroundColor", color("raised"))
      set(box, "paddingLeft", 1)
      set(box, "paddingRight", 1)
      set(box, "paddingTop", 1)
      set(box, "paddingBottom", 1)
      set(box, "marginTop", 1)
    }
  }

  const apply = (model = {}, run = null) => {
    set(badge, "content", model.badge ?? "")
    set(badge, "fg", color(model.badgeColor ?? "base"))
    set(pr, "content", model.pr ?? "")
    set(fixer, "content", model.fixer ?? "")
    set(nextStep, "content", model.nextStep ?? "")
    set(nextStep, "visible", Boolean(model.nextStep))
    set(branchValue, "content", model.branch ?? "")
    set(heroTitle, "content", model.title ?? "")
    set(taskValue, "content", model.task ?? "")
    set(phaseValue, "content", model.phase ?? "")
    const available = new Set(model.actions ?? [])
    set(actions, "visible", Boolean(run))
    for (const name of ACTION_NAMES) {
      const visible = Boolean(run) && (name === "copy" || available.has(name))
      set(buttons[name], "visible", visible)
      set(buttonBoxes[name], "visible", visible)
    }
    if (!available.has("cancel")) set(confirmBox, "visible", false)
    const rows = (model.rows ?? []).map((item) => item.kind === "check"
      ? { ...item, markerColor: item.marker === "✖" ? "error" : item.marker === "✔" ? "success" : "info" }
      : item)
    body.apply(rows)
    body.node.forEach((box, i) => decorate(box, rows[i]))
  }
  return {
    node: container, apply, buttons, buttonBoxes, heroCard, confirmBox,
    confirmYes, confirmNo, confirmSpacer, body, fixer, nextStep, open: DETAIL_OPEN,
  }
}
