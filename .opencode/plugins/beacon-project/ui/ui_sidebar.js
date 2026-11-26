// Sidebar slot: a dedicated Beacon header row, a wide state bar, a muted
// legend and the pooled Active/Recent line list. The header and Dispatch are
// real boxes so they align and hover like the native Context/MCP sections.
import { createBox, createLineMount, createSegmentBar, createText } from "./ui_nodes.js"
import { REASON_WIDTH } from "./ui_model.js"
import { truncate } from "./ui_alerts.js"
import { truncateSidebarTitle } from "./ui_view.js"
import { propSetter } from "./ui_test_helpers.js"

export const SIDEBAR_LINES = 34

// Presentation stays out of the renderer. Active titles keep the base tone and
// go bold only on attention states; Recent rows stay calm and unbolded. Long
// names are clipped from the end instead of using the host's middle ellipsis.
export function sidebarPresentation(items = []) {
  return items.map((item) => {
    if (item.banner) return {
      ...item,
      nameColor: item.color, nameBold: true,
      markerColor: item.color, statusColor: "muted",
    }
    if (!item.runID) {
      const isDispatch = item.action === "dispatch"
      const isSection = Boolean(item.sectionKey)
      return {
        ...item,
        nameColor: item.nameColor ?? (isDispatch ? "success" : isSection ? "action" : "muted"),
        nameBold: item.nameBold ?? false,
        markerColor: item.color ?? "muted",
        statusColor: "muted",
      }
    }
    const attention = item.color === "error" || item.color === "warning"
    const recent = item.recent === true
    const isRef = /^[#T]/.test(String(item.marker ?? ""))
    return {
      ...item,
      name: truncateSidebarTitle(item.name),
      nameColor: recent ? "muted" : "base",
      nameBold: recent ? false : attention,
      markerColor: item.markerColor ?? (recent ? "muted" : item.color ?? "muted"),
      statusColor: item.statusColor ?? (recent || isRef ? "muted" : item.color ?? "muted"),
    }
  })
}

export function createSidebarMount(ui, deps) {
  const { insert } = ui
  const { color, BOLD, onItem, onCard, hoverBg } = deps
  const set = propSetter(ui)
  // Queue rows carry a card action instead of a run ID. Route them to the
  // card reader and leave every other row with the existing handler.
  const handle = (item) => {
    if (typeof item?.action === "string" && item.action.startsWith("card:")) {
      if (typeof onCard === "function") onCard(item.action.slice("card:".length))
      return
    }
    onItem?.(item)
  }
  const container = createBox(ui, { flexDirection: "column", minWidth: 0 })

  // Standalone header box at column 0, not a cell of the shared line pool.
  const header = createBox(ui, {
    flexDirection: "row", justifyContent: "space-between", minWidth: 0, marginTop: 1,
  })
  const headerName = createText(ui, { content: "Beacon", fg: color("action") })
  set(headerName, "attributes", BOLD)
  const headerSummary = createText(ui, { content: "", fg: color("muted") })
  insert(header, headerName); insert(header, headerSummary)
  insert(container, header)

  const bar = createSegmentBar(ui, color)
  insert(container, bar.node)

  // The legend is a plain text node at column 0, not a pooled marker cell.
  const legend = createText(ui, { content: "", fg: color("muted") })
  set(legend, "flexShrink", 0)
  insert(container, legend)

  const meta = createLineMount(ui, { ...deps, maxLines: 1 })
  meta.node.forEach((node) => insert(container, node))

  const list = createLineMount(ui, { ...deps, onItem: handle, maxLines: SIDEBAR_LINES })
  list.node.forEach((node) => insert(container, node))

  const raised = color("raised")
  // The Dispatch line becomes a padded, themed button. Pooled rows reset every
  // apply, so a row that used to be Dispatch never keeps the button styling.
  const decorateRows = () => list.node.forEach((box) => {
    const item = box.item
    const dispatch = item?.action === "dispatch"
    const banner = item?.banner === true
    const baseBg = dispatch || banner ? raised : undefined
    set(box, "marginTop", dispatch || banner ? 1 : 0)
    set(box, "paddingLeft", dispatch || banner ? 1 : 0)
    set(box, "paddingRight", dispatch || banner ? 1 : 0)
    set(box, "backgroundColor", baseBg)
    set(box, "onMouseUp", dispatch ? () => handle(box.item) : undefined)
    set(box, "onMouseOver", () => set(box, "backgroundColor", hoverBg))
    set(box, "onMouseOut", () => set(box, "backgroundColor", baseBg))
  })

  const apply = (model, error = null) => {
    // The header carries its item for the click/refresh contract even though
    // it is no longer allocated by createLineMount.
    header.item = { name: "Beacon", status: model.summary, color: "action" }
    set(headerName, "content", "Beacon")
    set(headerSummary, "content", model.summary ?? "")
    bar.apply(model.segments)
    set(legend, "content", model.legend ?? "")
    const metaItems = []
    if (error) metaItems.push({ marker: "", name: `[stale] ${truncate(error, REASON_WIDTH)}`, status: "", color: "warning" })
    meta.apply(metaItems)
    list.apply(sidebarPresentation(model.listItems))
    decorateRows()
  }
  return { node: container, apply, header, headerName, headerSummary, legend, bar: bar.node }
}
