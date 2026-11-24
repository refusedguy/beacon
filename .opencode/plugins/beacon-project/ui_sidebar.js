// Sidebar slot: a flat list of fixed line nodes beside the native Context and
// MCP sections. Header, proportional state bar and the two collapsible groups
// are allocated once in the render callback and only updated by apply().
import { createBox, createLineMount, createSegmentBar } from "./ui_nodes.js"

export const SIDEBAR_ACTIVE_LIMIT = 8
export const SIDEBAR_LINES = 34

export function createSidebarMount(ui, deps) {
  const { insert } = ui
  const container = createBox(ui, { flexDirection: "column", minWidth: 0 })
  const title = createLineMount(ui, { ...deps, maxLines: 1 })
  const bar = createSegmentBar(ui, deps.color)
  const meta = createLineMount(ui, { ...deps, maxLines: 2 })
  const list = createLineMount(ui, { ...deps, maxLines: SIDEBAR_LINES })
  title.node.forEach((node) => insert(container, node))
  insert(container, bar.node)
  meta.node.forEach((node) => insert(container, node))
  list.node.forEach((node) => insert(container, node))
  const apply = (model, error = null) => {
    title.apply([{ marker: "", name: "Beacon", status: model.summary, color: "action", bold: true }])
    bar.apply(model.segments)
    const metaItems = [{ marker: "", name: model.legend, status: "", color: "muted" }]
    if (error) metaItems.push({ marker: "", name: `[stale] ${error}`, status: "", color: "warning" })
    meta.apply(metaItems)
    list.apply(model.listItems)
  }
  return { node: container, apply }
}
