// Fixed node pools built once inside a slot render callback. apply() only ever
// changes props; it never calls createElement or insert, so refresh() stays
// safe outside a renderer context. No TUI imports: the caller injects them.
export function createLineMount(ui, {
  color, BOLD = 0, onItem, hoverBg, maxLines, wrap = false,
}) {
  const { createElement, insert, setProp } = ui
  const cell = (grow = false) => {
    const node = createElement("text")
    setProp(node, "wrapMode", wrap && grow ? "word" : "none")
    setProp(node, "selectable", false)
    setProp(node, "truncate", !(wrap && grow))
    setProp(node, "flexShrink", grow ? 1 : 0)
    if (grow) { setProp(node, "flexGrow", 1); setProp(node, "minWidth", 0) }
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
    const status = cell()
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
  const apply = (items = []) => {
    lines.forEach((box, i) => {
      const item = items[i]
      box.item = item
      if (!item) { setProp(box, "visible", false); return }
      const [marker, name, status] = cellsByLine[i]
      const fg = color(item.color ?? "base")
      // The host does not apply the box gap between text cells. Run markers
      // carry markerSpace so "✔" never runs into the title as "✔Hotfix".
      setProp(marker, "content", item.markerSpace && item.marker ? `${item.marker} ` : item.marker ?? "")
      setProp(marker, "fg", fg)
      setProp(name, "content", item.text ?? item.name ?? "")
      setProp(name, "fg", fg)
      setProp(name, "attributes", item.bold ? BOLD : 0)
      setProp(status, "content", item.status ?? "")
      setProp(status, "fg", item.statusColor ? color(item.statusColor) : fg)
      setProp(box, "visible", true)
    })
  }
  return { node: lines, apply }
}

// One row of proportional segments, each with its own theme color. Five fixed
// cells: four states plus the muted remainder.
export function createSegmentBar(ui, color) {
  const { createElement, insert, setProp } = ui
  const box = createElement("box")
  setProp(box, "flexDirection", "row")
  setProp(box, "minWidth", 0)
  const cells = Array.from({ length: 5 }, () => {
    const node = createElement("text")
    setProp(node, "wrapMode", "none")
    setProp(node, "selectable", false)
    setProp(node, "truncate", true)
    insert(box, node)
    return node
  })
  const apply = (segments = []) => cells.forEach((node, i) => {
    const segment = segments[i]
    setProp(node, "content", segment?.text ?? "")
    setProp(node, "fg", color(segment?.color ?? "muted"))
  })
  return { node: box, apply }
}

export function createText(ui, { content = "", fg, wrap = false, grow = false, bold = false, onMouseUp, name }) {
  const { createElement, setProp } = ui
  const node = createElement("text")
  if (name) setProp(node, "name", name)
  setProp(node, "content", content)
  if (fg) setProp(node, "fg", fg)
  setProp(node, "wrapMode", wrap ? "word" : "none")
  setProp(node, "selectable", false)
  setProp(node, "truncate", !wrap)
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
