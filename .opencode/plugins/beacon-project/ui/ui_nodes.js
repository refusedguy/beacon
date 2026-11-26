// Fixed node pools built once inside a slot render callback. apply() only ever
// changes props; it never calls createElement or insert, so refresh() stays
// safe outside a renderer context. No TUI imports: the caller injects them.
import { STATE_BAR_KEYS } from "./ui_view.js"

// The four props every text baseline shares. Callers add flex and colour on
// top, so a wrapping cell and a static label start from the same host state.
export const baseTextProps = (wrap = false) => ({
  wrapMode: wrap ? "word" : "none",
  selectable: false,
  truncate: !wrap,
})

export function createLineMount(ui, {
  color, BOLD = 0, onItem, hoverBg, maxLines, wrap = false, shrinkStatus = false,
}) {
  const { createElement, insert, setProp } = ui
  const cell = (grow = false, shrinkable = false, right = false) => {
    const node = createElement("text")
    for (const [key, value] of Object.entries(baseTextProps(wrap && grow))) setProp(node, key, value)
    setProp(node, "flexShrink", grow || shrinkable ? 1 : 0)
    if (grow || shrinkable || right) setProp(node, "minWidth", 0)
    if (grow || right) setProp(node, "flexGrow", 1)
    if (right) setProp(node, "textAlign", "right")
    return node
  }
  const lines = []
  const cellsByLine = []
  for (let i = 0; i < maxLines; i++) {
    const box = createElement("box")
    setProp(box, "flexDirection", "row")
    setProp(box, "gap", 1)
    setProp(box, "minWidth", 0)
    const marker = cell()
    const name = cell(true)
    // Fixed rows anchor the status at the panel's right edge: the cell takes
    // the remaining width and right-aligns its text. Wrapping card rows keep
    // their natural status width so long values wrap in place instead.
    const status = cell(false, shrinkStatus, !wrap)
    insert(box, marker); insert(box, name); insert(box, status)
    cellsByLine.push([marker, name, status])
    if (onItem) {
      setProp(box, "onMouseDown", () => {
        if (box.item?.runID || box.item?.sectionKey || box.item?.action) onItem(box.item)
      })
      setProp(box, "onMouseOver", () => setProp(box, "backgroundColor", hoverBg))
      setProp(box, "onMouseOut", () => setProp(box, "backgroundColor", undefined))
    }
    setProp(box, "visible", false)
    lines.push(box)
  }
  // A render usually changes a handful of rows. Remember the rendered props
  // per pooled slot so an unchanged row costs an item assignment, not six
  // setProp calls that invalidate host nodes.
  const appliedKeys = []
  const rowKey = (item) => item ? [
    item.text ?? item.name ?? "", item.marker ?? "", item.status ?? "",
    item.color ?? "base", item.markerColor ?? "", item.nameColor ?? "",
    item.statusColor ?? "", item.markerSpace === true ? 1 : Number(item.markerSpace) || 0,
    (item.nameBold ?? item.bold) ? 1 : 0,
  ].join("\u0000") : ""
  const apply = (items = []) => {
    lines.forEach((box, i) => {
      const item = items[i]
      box.item = item
      const key = rowKey(item)
      if (appliedKeys[i] === key) return
      appliedKeys[i] = key
      if (!item) { setProp(box, "visible", false); return }
      const [marker, name, status] = cellsByLine[i]
      const fg = color(item.color ?? "base")
      // Per-cell overrides let the sidebar color a marker by state while the
      // title stays in the base tone. Defaults match the original line pool.
      const markerFG = color(item.markerColor ?? item.color ?? "base")
      const nameFG = color(item.nameColor ?? item.color ?? "base")
      // The host does not apply the box gap between text cells. Run markers
      // carry markerSpace so "✔" never runs into the title as "✔Hotfix".
      // A numeric markerSpace aligns a second line under its title.
      const gap = item.markerSpace === true ? 1 : Number(item.markerSpace) || 0
      setProp(marker, "content", gap && item.marker ? `${item.marker}${" ".repeat(gap)}` : item.marker ?? "")
      setProp(marker, "fg", markerFG)
      setProp(name, "content", item.text ?? item.name ?? "")
      setProp(name, "fg", nameFG)
      setProp(name, "attributes", (item.nameBold ?? item.bold) ? BOLD : 0)
      setProp(status, "content", item.status ?? "")
      setProp(status, "fg", item.statusColor ? color(item.statusColor) : fg)
      setProp(box, "visible", true)
    })
  }
  return { node: lines, apply }
}

// The visual props of every rounded plate. These nine keys stay identical
// across the card hero, the detail hero, the confirm box and pooled reason
// rows. Layout props are supplied by the caller.
const PLATE_KEYS = [
  "border", "borderStyle", "borderColor", "backgroundColor",
  "paddingLeft", "paddingRight", "paddingTop", "paddingBottom", "marginTop",
]

export function createCardPlate(ui, deps, options = {}) {
  const { color } = deps
  const { border = "action", background = "panel", target, ...props } = options
  const plate = {
    border: true, borderStyle: "rounded", borderColor: color(border),
    backgroundColor: color(background),
    paddingLeft: 1, paddingRight: 1, paddingTop: 1, paddingBottom: 1, marginTop: 1,
  }
  // A pooled row keeps its node identity, so the same plate props are applied
  // through setProp instead of allocating a new box.
  if (target) {
    const set = deps.setProp ?? ((node, key, value) => { node.props[key] = value })
    for (const [key, value] of Object.entries(plate)) set(target, key, value)
    return target
  }
  return createBox(ui, { ...plate, ...props })
}

// A scroll host and a fixed line pool are mounted together in both panels.
// Sidebar passes scroll:false so its rows stay direct children of the slot.
export function mountLines(ui, parent, deps, options = {}) {
  const { insert } = ui
  const { scroll = false, ...lineOptions } = options
  const host = scroll
    ? createBox(ui, { scroll: true, flexGrow: 1, minWidth: 0, minHeight: 0 })
    : parent
  const mount = createLineMount(ui, { ...deps, ...lineOptions })
  mount.node.forEach((node) => insert(host, node))
  if (scroll) insert(parent, host)
  return { ...mount, scroll: scroll ? host : null }
}

// Pooled boxes must lose the plate styling of a previous row. Resetting the
// same key list keeps a plain row from inheriting a border or padding.
export function resetProps(set, node, keys = PLATE_KEYS) {
  for (const key of keys) set(node, key, undefined)
}

// One row of proportional segments, each with its own theme color. One fixed
// cell per state plus the muted remainder, allocated once so refresh never
// creates a node.
export function createSegmentBar(ui, color) {
  const { createElement, insert, setProp } = ui
  const box = createElement("box")
  setProp(box, "flexDirection", "row")
  setProp(box, "minWidth", 0)
  const cells = Array.from({ length: STATE_BAR_KEYS.length + 1 }, () => {
    const node = createElement("text")
    setProp(node, "wrapMode", "none")
    setProp(node, "selectable", false)
    setProp(node, "truncate", false)
    setProp(node, "flexShrink", 0)
    setProp(node, "visible", false)
    insert(box, node)
    return node
  })
  const apply = (segments = []) => cells.forEach((node, i) => {
    const segment = segments[i]
    setProp(node, "content", segment?.text ?? "")
    setProp(node, "fg", color(segment?.color ?? "muted"))
    // An empty segment stays hidden so it takes no width and leaves no indent.
    setProp(node, "visible", Boolean(segment?.text))
  })
  return { node: box, apply }
}

export function createText(ui, { content = "", fg, wrap = false, grow = false, bold = false, onMouseUp, name }) {
  const { createElement, setProp } = ui
  const node = createElement("text")
  if (name) setProp(node, "name", name)
  setProp(node, "content", content)
  if (fg) setProp(node, "fg", fg)
  for (const [key, value] of Object.entries(baseTextProps(wrap))) setProp(node, key, value)
  setProp(node, "flexShrink", grow ? 1 : 0)
  if (grow) { setProp(node, "flexGrow", 1); setProp(node, "minWidth", 0) }
  if (bold) setProp(node, "attributes", 1)
  if (onMouseUp) setProp(node, "onMouseUp", onMouseUp)
  return node
}

export function createBox(ui, props = {}) {
  const { createElement, setProp } = ui
  const node = createElement(props.scroll ? "scrollbox" : "box")
  for (const [key, value] of Object.entries(props)) {
    if (key !== "scroll") setProp(node, key, value)
  }
  return node
}
