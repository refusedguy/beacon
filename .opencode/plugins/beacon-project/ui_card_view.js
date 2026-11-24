// Read-only task card view for session.panel. Nodes are allocated only inside
// the render callback that opens the panel. apply() changes props in place.
import { CARD_BODY_LINES } from "./ui_model.js"
import { createBox, createLineMount, createText } from "./ui_nodes.js"

export const CARD_PANEL_NAME = "beacon.card"
export const CARD_LINES = CARD_BODY_LINES + 8
const fieldKey = (text) => text.padEnd(10, " ")

export function fixPrompt(card) {
  if (!card) return ""
  return `Fix ${card.id}: ${card.title} in ${card.path}. Scope: ${card.scope}. Depends: ${card.depends}.`
}

export function cardModel(card) {
  if (!card) return null
  const rows = []
  const meta = (label, value) => rows.push({
    marker: fieldKey(label), name: value || "-", status: "", color: "base", kind: "card-meta",
  })
  meta("ID", card.id)
  meta("Title", card.title)
  meta("Priority", card.priority)
  meta("Status", card.status)
  meta("Depends", card.depends)
  meta("Path", card.path)
  rows.push({ marker: "", name: "", status: "", color: "muted", kind: "card-divider" })
  for (const line of card.body.split(/\r?\n/)) rows.push({
    marker: "", name: line, status: "", color: "muted", kind: "card-body",
  })
  return { card, rows }
}

const COPY_LABEL = Object.freeze({
  id: "⧉ Copy ID", path: "⧉ Copy path", fix: "⧉ Copy fix prompt",
})

export function createCardMount(ui, deps) {
  const { insert } = ui
  const { color, BOLD, onCopy, setProp } = deps
  const set = setProp ?? ((node, key, value) => { node.props[key] = value })
  const container = createBox(ui, { flexDirection: "column", minWidth: 0, flexGrow: 1 })

  const header = createBox(ui, {
    flexDirection: "row", justifyContent: "space-between", minWidth: 0, flexShrink: 0,
  })
  const title = createText(ui, { content: "Task card", fg: color("action"), bold: true })
  set(title, "attributes", BOLD)
  const hint = createText(ui, { content: "read-only", fg: color("muted") })
  insert(header, title); insert(header, hint)
  insert(container, header)

  // A bordered, padded card holds the full metadata and body. Body cells are
  // word-wrapped and never truncated, so the operator can read the whole card.
  const plate = createBox(ui, {
    border: true, borderStyle: "rounded", borderColor: color("action"),
    backgroundColor: color("panel"), paddingLeft: 1, paddingRight: 1,
    paddingTop: 1, paddingBottom: 1, marginTop: 1, flexShrink: 0,
    flexDirection: "column", minWidth: 0,
  })
  const scroll = createBox(ui, { scroll: true, flexGrow: 1, minWidth: 0 })
  const body = createLineMount(ui, { ...deps, maxLines: CARD_LINES, wrap: true })
  body.node.forEach((node) => insert(scroll, node))
  insert(plate, scroll)
  insert(container, plate)

  // Clipboard is the supported path into the current session. OpenCode 2.3.2
  // has no verified composer-insertion API, so no extra button is offered.
  // The operator pastes the copied fix prompt into the session.
  const buttons = Object.create(null)
  for (const name of Object.keys(COPY_LABEL)) {
    const baseBg = color("raised")
    const box = createBox(ui, {
      flexDirection: "row", flexShrink: 0, paddingLeft: 1, paddingRight: 1,
      marginRight: 1, backgroundColor: baseBg,
      onMouseOver: () => set(box, "backgroundColor", color("panel")),
      onMouseOut: () => set(box, "backgroundColor", baseBg),
    })
    const text = createText(ui, {
      content: COPY_LABEL[name], fg: color("base"), name: `beacon-card-copy-${name}`,
      onMouseUp: () => onCopy?.(name),
    })
    set(box, "onMouseUp", () => onCopy?.(name))
    insert(box, text); insert(container, box)
    buttons[name] = text
  }
  const note = createText(ui, {
    content: "Paste the copied fix prompt into the current session.", fg: color("muted"), wrap: true,
  })
  set(note, "marginTop", 1)
  insert(container, note)

  const apply = (model = null) => {
    set(title, "content", model?.card ? `Card ${model.card.id}` : "Task card")
    body.apply(model?.rows ?? [])
  }
  return { node: container, apply, buttons, body }
}
