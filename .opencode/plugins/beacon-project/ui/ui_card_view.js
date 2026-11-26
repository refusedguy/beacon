// Read-only task card view for session.panel. Nodes are allocated only inside
// the render callback that opens the panel. apply() changes props in place.
import { CARD_BODY_LINES, str } from "./ui_model.js"
import { createBox, createLineMount, createText } from "./ui_nodes.js"
import { taskTree } from "./ui_alerts.js"

const timeOf = (v) => {
  if (v == null || v === "") return 0
  if (typeof v === "number") return Number.isFinite(v) ? v : 0
  const n = Number(v)
  if (Number.isFinite(n) && n > 0) return n
  const t = Date.parse(str(v))
  return Number.isFinite(t) ? t : 0
}
export function durationText(ms) {
  if (!Number.isFinite(ms) || ms < 0) return ""
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`
}
// Timeline and decision events carry the `at` stamp. Their first and last
// stamps bound the whole run, which is the number the card header shows.
export function runEventTimes(run) {
  const out = []
  for (const event of [...(run?.timeline ?? []), ...(run?.decisions ?? [])]) {
    const t = timeOf(event?.at)
    if (t > 0) out.push(t)
  }
  return out.sort((a, b) => a - b)
}
// A label is always paired with the fallback value so a bare timestamp is not
// mistaken for an elapsed duration.
export function runTotalTime(run) {
  const times = runEventTimes(run)
  if (times.length) return { text: durationText(times[times.length - 1] - times[0]), label: "total" }
  const updated = run?.updatedAt
  if (updated != null && updated !== "") {
    const s = str(updated)
    return { text: s.length >= 19 ? s.slice(11, 19) : s, label: "updated" }
  }
  return { text: "", label: "" }
}

export const CARD_PANEL_NAME = "beacon.card"
export const CARD_LINES = CARD_BODY_LINES + 8
const fieldKey = (text) => text.padEnd(10, " ")
export const cardRelativePath = (card) => card?.relativePath || `docs/tasks/${card?.id}.md`

export function fixPrompt(card) {
  if (!card) return ""
  return `Fix ${card.id}: ${card.title} in ${cardRelativePath(card)}. Scope: ${card.scope}. Depends: ${card.depends}.`
}

const ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
const values = (v) => (Array.isArray(v) ? v : v ? [v] : []).map(str).filter((id) => ID_RE.test(id))

// The card owns its parent field. The queue index supplies the children so
// the panel and the sidebar draw the same family from one relation.
export function cardFamily(card, queue = []) {
  const id = str(card?.id)
  const meta = card?.meta ?? {}
  const deps = [...new Set([
    ...values(meta.depends), ...values(meta.depends_on), ...values(str(card?.depends).split(/[\s,]+/)),
  ])]
  const explicit = str(card?.parent) === id ? "" : str(card?.parent)
  const tree = taskTree([...(Array.isArray(queue) ? queue : []), { id, parent: explicit }])
  const parent = tree.parent.get(id) || ""
  const children = [...new Set([
    ...values(meta.children),
    ...(tree.children.get(id) ?? []),
    ...deps.filter((dep) => tree.parent.get(dep) === id),
  ])]
  return { deps, parent, children }
}

// The queue and card panels share one persisted set of expanded epic IDs.
// An absent map means every epic is collapsed, which is the operator default.
export function createEpicState(ctx, onChange = () => {}) {
  const store = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-epic-sections", { initial: {} }) : [{}, () => {}]
  const open = Object.create(null)
  for (const [id, value] of Object.entries(store[0] ?? {})) {
    if (typeof id === "string" && value === true) open[id] = true
  }
  const toggle = (id) => {
    if (!id) return
    open[id] = !open[id]
    void store[1]((draft) => { draft[id] = open[id] }).catch(() => {})
    onChange()
  }
  return { open, toggle }
}

// One compact identity line for the header. The PR is optional so a task with
// no run yet still shows its ID.
export function cardHeader(run) {
  if (!run) return { ref: "", taskID: "", pr: 0, duration: "", label: "" }
  const taskID = str(run.taskID)
  const pr = Number(run.pr) || 0
  const ref = [taskID, pr ? `#${pr}` : ""].filter(Boolean).join(" · ")
  const { text, label } = runTotalTime(run)
  return { ref, taskID, pr, duration: text, label }
}

// A null map keeps the flat legacy rows for pure callers that never drive the
// toggle. The real panel passes the persisted map so epics start collapsed.
export function cardModel(card, queue = [], epicOpen = null, run = null) {
  if (!card) return null
  const { deps, parent, children } = cardFamily(card, queue)
  const queued = new Map((Array.isArray(queue) ? queue : []).map((item) => [str(item?.id), item]))
  const header = cardHeader(run ?? queued.get(str(card?.id))?.run ?? null)
  const titleOf = (id) => str(queued.get(id)?.title)
  const childIDs = new Set(children)
  const rows = []
  const meta = (label, value) => rows.push({
    marker: fieldKey(label), name: value || "-", status: "", color: "base", kind: "card-meta",
  })
  meta("ID", card.id)
  meta("Title", card.title)
  meta("Priority", card.priority)
  meta("Status", card.status)
  meta("Kind", str(card.kind) || "task")
  meta("Depends", card.depends)
  for (const dep of deps) {
    if (childIDs.has(dep)) continue
    rows.push({
      marker: "↳", name: dep, status: titleOf(dep), color: "muted", kind: "card-dep", action: `card:${dep}`,
    })
  }
  if (parent) {
    rows.push({ marker: "", name: "Parent epic", status: "", color: "action", kind: "card-section" })
    rows.push({
      marker: "↳", name: parent, status: titleOf(parent), color: "muted",
      kind: "card-parent", action: `card:${parent}`,
    })
  }
  if (children.length) {
    const expanded = epicOpen === null || epicOpen[str(card.id)] === true
    rows.push({
      marker: expanded ? "▼" : "▶", name: `Children (${children.length})`, status: "",
      color: "action", kind: "card-section", epicKey: str(card.id), action: `epic:${card.id}`,
    })
    if (expanded) for (const child of children) {
      const title = titleOf(child)
      rows.push({
        marker: "↳", name: title ? `${child} · ${title}` : child,
        status: str(queued.get(child)?.status), color: "muted",
        kind: "card-child", action: `card:${child}`,
      })
    }
  }
  meta("Path", cardRelativePath(card))
  rows.push({ marker: "", name: "", status: "", color: "muted", kind: "card-divider" })
  for (const line of str(card.body).split(/\r?\n/)) rows.push({
    marker: "", name: line, status: "", color: "muted", kind: "card-body",
  })
  return { card, header, rows }
}

const COPY_LABEL = Object.freeze({
  id: "⧉ Copy ID", path: "⧉ Copy path", fix: "⧉ Copy fix prompt",
})

export function cardBackCommand(back) {
  return {
    id: "beacon.card.back", title: "Beacon card back", description: "Go back to the previous card",
    group: "Beacon", bind: "ctrl+b", palette: true, slash: { name: "beacon-card-back" },
    run: () => back(),
  }
}

export function createCardMount(ui, deps) {
  const { insert } = ui
  const { color, BOLD, onCopy, onClose, onBack, onItem, setProp } = deps
  const set = setProp ?? ((node, key, value) => { node.props[key] = value })
  const container = createBox(ui, { flexDirection: "column", minWidth: 0, flexGrow: 1 })

  const header = createBox(ui, {
    flexDirection: "row", justifyContent: "space-between", minWidth: 0, flexShrink: 0,
  })
  const title = createText(ui, { content: "Task card", fg: color("action"), bold: true })
  set(title, "attributes", BOLD)
  // The title absorbs the free space, so the controls keep their old position
  // at the right and the duration sits just past them at the header edge.
  set(title, "flexGrow", 1)
  set(title, "minWidth", 0)
  const duration = createText(ui, { content: "", fg: color("muted") })
  set(duration, "paddingLeft", 1)
  set(duration, "visible", false)
  const controls = createBox(ui, {
    flexDirection: "row", minWidth: 0, flexShrink: 0, flexWrap: "wrap", rowGap: 1,
  })
  const back = createText(ui, { content: "← Back", fg: color("action"), onMouseUp: onBack })
  set(back, "paddingLeft", 1); set(back, "paddingRight", 1)
  const hint = createText(ui, { content: "ctrl+g", fg: color("action"), onMouseUp: onClose })
  set(hint, "paddingLeft", 1); set(hint, "paddingRight", 1)
  const readonly = createText(ui, { content: "read-only", fg: color("muted") })
  set(readonly, "paddingLeft", 1); set(readonly, "paddingRight", 1)
  const gap = createText(ui, { content: " ", fg: color("muted") })
  const close = createText(ui, { content: "✕", fg: color("error"), onMouseUp: onClose })
  set(close, "paddingLeft", 1); set(close, "paddingRight", 1)
  insert(controls, back); insert(controls, hint); insert(controls, readonly); insert(controls, gap); insert(controls, close)
  insert(header, title); insert(header, controls); insert(header, duration)
  insert(container, header)

  // A bordered, padded card holds the full metadata and body. Body cells are
  // word-wrapped and never truncated, so the operator can read the whole card.
  const plate = createBox(ui, {
    border: true, borderStyle: "rounded", borderColor: color("action"),
    backgroundColor: color("panel"), paddingLeft: 1, paddingRight: 1,
    paddingTop: 1, paddingBottom: 1, marginTop: 1,
    flexDirection: "column", minWidth: 0,
  })
  const scroll = createBox(ui, { scroll: true, flexGrow: 1, minWidth: 0, minHeight: 0 })
  const body = createLineMount(ui, {
    ...deps, onItem, maxLines: CARD_LINES, wrap: true, shrinkStatus: true,
  })
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
    const head = model?.header
    set(title, "content", head?.ref || (model?.card ? `Card ${model.card.id}` : "Task card"))
    const total = head?.duration ? [head.label, head.duration].filter(Boolean).join(" ") : ""
    set(duration, "content", total)
    set(duration, "visible", Boolean(total))
    body.apply(model?.rows ?? [])
  }
  return { node: container, apply, buttons, body, back }
}
