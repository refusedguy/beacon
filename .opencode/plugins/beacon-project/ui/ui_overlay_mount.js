// Full-screen perf overlay. The caller calls createOverlayMount inside the
// session.panel render callback, so every node is allocated in that one render.
// apply() only writes props through setProp, so a refresh never creates a node.
import { checkRow, checksSummary } from "./ui_alerts.js"
import { createPanelChrome } from "./ui_card_view.js"
import { createBox, createCardPlate, createText, mountLines } from "./ui_nodes.js"

export const OVERLAY_LINES = 40
export const OVERLAY_DETAIL_LINES = 12

const propSetter = (source) => source?.setProp ?? ((node, key, value) => { node.props[key] = value })
const list = (value) => (Array.isArray(value) ? value : value ? [value] : [])
const str = (value) => value == null ? "" : String(value)
const first = (...values) => values.find((value) => value !== undefined && value !== null && value !== "")

const stateColor = (state) => {
  const value = str(state).toLowerCase()
  if (["failed", "failure", "error"].includes(value)) return "error"
  if (["success", "merged", "done"].includes(value)) return "success"
  if (["running", "waiting_ci", "waiting", "blocked", "pending", "queued"].includes(value)) return "warning"
  return "muted"
}

// The id keeps its own cell so the overlay never clips it the way the sidebar
// does. State, PR and run id share the right-aligned status cell.
export function cardLine(card = {}) {
  const state = str(first(card.state, card.State))
  const pr = first(card.pr, card.PR, card.prNumber, card.PRNumber)
  const run = first(card.runID, card.runId, card.RunID, card.run)
  const status = [state, pr === undefined ? "" : `#${str(pr)}`, str(run)].filter(Boolean).join(" · ")
  return {
    marker: str(first(card.id, card.ID)),
    name: str(first(card.title, card.Title, card.name)),
    status,
    color: stateColor(state),
    markerColor: "muted",
    statusColor: "muted",
    kind: "card",
  }
}

export function createOverlayMount(ui, deps = {}) {
  const { insert } = ui
  const { color, BOLD = 0, onClose } = deps
  const set = propSetter(deps)

  // createPanelChrome owns the container, title, key hint and close glyph. The
  // hint and the glyph both call the close callback captured by the host.
  const keys = createText(ui, { content: "esc close", fg: color("muted") })
  const chrome = createPanelChrome(ui, deps, {
    title: "Performance", titleColor: "action", onClose,
    hintLabel: "ctrl+o", padding: 8, afterHint: [keys],
  })
  const { container, title, hint, close } = chrome
  set(title, "flexGrow", 1)
  set(title, "minWidth", 0)

  const summary = createText(ui, { content: "", fg: color("muted"), wrap: true })
  set(summary, "flexShrink", 0)
  insert(container, summary)

  const table = createBox(ui, { flexDirection: "column", minWidth: 0, flexShrink: 0 })
  const heading = createText(ui, { content: "ID · Card · State · PR · Run", fg: color("muted") })
  insert(table, heading)
  const rows = mountLines(ui, table, deps, { maxLines: OVERLAY_LINES })
  insert(container, table)

  const detail = createCardPlate(ui, deps, {
    flexDirection: "column", minWidth: 0, flexShrink: 0, overflow: "hidden",
  })
  const detailTitle = createText(ui, { content: "Checks", fg: color("action"), bold: true })
  set(detailTitle, "attributes", BOLD)
  const checkSummary = createText(ui, { content: "", fg: color("muted") })
  const detailRows = mountLines(ui, detail, deps, {
    maxLines: OVERLAY_DETAIL_LINES, wrap: true, shrinkStatus: true,
  })
  insert(detail, detailTitle)
  insert(detail, checkSummary)
  insert(container, detail)

  const apply = (model = {}) => {
    const summaryText = str(first(model.summary, model.summaryLine))
    set(title, "content", str(first(model.title, model.heading)) || "Performance")
    set(summary, "content", summaryText)
    set(summary, "visible", Boolean(summaryText))

    rows.apply(list(first(model.rows, model.cards)).map(cardLine))

    const checks = list(first(model.checks, model.detail?.checks))
    const summaryOfChecks = checksSummary(checks)
    set(detailTitle, "content", str(model.detail?.title) || "Checks")
    set(checkSummary, "content", summaryOfChecks)
    set(checkSummary, "visible", Boolean(summaryOfChecks))
    const measures = checks.length ? checks.map(checkRow)
      : list(model.detail?.rows ?? model.measurements)
    detailRows.apply(measures)
  }

  return {
    node: container, apply, rows, detailRows, title, summary, hint, close, keys,
    checkSummary, detailTitle, header: chrome.header, controls: chrome.controls,
    detail,
  }
}
