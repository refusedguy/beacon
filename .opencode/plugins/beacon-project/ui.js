// Beacon status panel and run detail for the OpenCode TUI. The view model and
// the event bridge live in sibling files so every module stays under 350 lines.
export * from "./ui_model.js"
export * from "./ui_view.js"
export * from "./ui_bridge.js"

import {
  EVENT_IDLE_MS, MAX_ROWS, PANEL_NAME, REFRESH_MS, BeaconStatusRpc, EMPTY_SNAPSHOT,
  createStatusSource, errorMessage, normalizeSnapshot, str, tuiLocation,
} from "./ui_model.js"
import { detailCommand, detailLines, metricsLine, snapshotLineItems } from "./ui_view.js"
import { createRunChangeBridge } from "./ui_bridge.js"
import { pollIntervalMs, pluginOptions, snapshotLimits, statusPanelEnabled, watcherEnabled } from "./options.js"

// The footer always renders one line. The detail panel needs more than the
// sidebar pool, so each slot gets its own fixed node budget.
export const FOOTER_ROWS = 1
export const DETAIL_ROWS = 120
// Builds a fixed pool of row nodes once, while the caller is inside a slot
// render callback. Every later apply() only changes props on those nodes: it
// never calls createElement or insert. That keeps repeated calls from refresh()
// (a setInterval timer with no renderer context) from throwing
// "No renderer found" whenever the run count changes. Kept free of Solid so
// tests can drive it.
export function createRowMount(ui, { color, BOLD = 0, onRow, hoverBg, maxRows = MAX_ROWS }) {
  const { createElement, insert, setProp } = ui
  const cell = (grow = false) => {
    const t = createElement("text")
    setProp(t, "wrapMode", "none")
    setProp(t, "selectable", false)
    setProp(t, "truncate", true)
    setProp(t, "flexShrink", grow ? 1 : 0)
    if (grow) { setProp(t, "flexGrow", 1); setProp(t, "minWidth", 0) }
    return t
  }
  return (container, items, rowLimit = maxRows) => {
    const slots = []
    for (let i = 0; i < rowLimit; i++) {
      const box = createElement("box")
      setProp(box, "flexDirection", "row")
      setProp(box, "gap", 1)
      setProp(box, "minWidth", 0)
      const dot = cell()
      const name = cell(true)
      const status = cell()
      insert(box, dot); insert(box, name); insert(box, status)
      insert(container, box)
      const s = { box, dot, name, status }
      if (onRow) {
        setProp(box, "onMouseDown", () => { if (s.item?.runID) onRow(s.item) })
        setProp(box, "onMouseOver", () => setProp(box, "backgroundColor", hoverBg))
        setProp(box, "onMouseOut", () => setProp(box, "backgroundColor", undefined))
      }
      setProp(box, "visible", false)
      slots.push(s)
    }
    const apply = () => {
      const rows = items()
      rows.forEach((item, i) => {
        const s = slots[i]
        if (!s) return
        const isLine = item.text !== undefined
        s.item = item
        setProp(s.dot, "content", isLine ? "" : str(item.marker))
        setProp(s.dot, "fg", color(item.color))
        setProp(s.name, "content", isLine ? str(item.text) : str(item.name))
        setProp(s.name, "fg", item.color === "muted" ? color("muted") : color("base"))
        setProp(s.name, "attributes", item.bold ? BOLD : 0)
        setProp(s.status, "content", isLine ? "" : str(item.status))
        setProp(s.status, "fg", color("muted"))
        setProp(s.box, "visible", true)
      })
      slots.slice(rows.length).forEach((s) => { s.item = undefined; setProp(s.box, "visible", false) })
    }
    apply()
    return apply
  }
}

async function setup(ctx, deps = {}) {
  const options = deps.options ?? pluginOptions(ctx)
  if (!statusPanelEnabled(options)) return () => {}
  // Keep the imports literal so the OpenCode runtime plugin can rewrite them.
  // A computed import(specifier) is invisible to its rewriter, and this
  // directory has no node_modules for Bun to resolve the TUI peers.
  const defaultImport = (specifier) => {
    if (specifier === "@opentui/solid") return import("@opentui/solid")
    if (specifier === "solid-js") return import("solid-js")
    if (specifier === "@opentui/core") return import("@opentui/core")
    throw new Error(`unexpected import: ${specifier}`)
  }
  const load = deps.import ?? defaultImport
  const [{ createElement, insert, setProp }, { createSignal }] = await Promise.all([
    load("@opentui/solid"), load("solid-js"),
  ])
  let BOLD = 0
  try { ({ TextAttributes: { BOLD } } = await load("@opentui/core")) } catch {}
  const source = createStatusSource(ctx, { limits: snapshotLimits(options) })
  const theme = ctx.theme
  const timers = {
    now: deps.now ?? (() => Date.now()),
    setTimeout: deps.setTimeout ?? globalThis.setTimeout.bind(globalThis),
    clearTimeout: deps.clearTimeout ?? globalThis.clearTimeout.bind(globalThis),
    setInterval: deps.setInterval ?? globalThis.setInterval.bind(globalThis),
    clearInterval: deps.clearInterval ?? globalThis.clearInterval.bind(globalThis),
  }
  const [snapshot, setSnapshot] = createSignal(EMPTY_SNAPSHOT)
  const [error, setError] = createSignal(null)
  const [detailItems, setDetailItems] = createSignal(detailLines(null))
  const [footerItems, setFooterItems] = createSignal([metricsLine(EMPTY_SNAPSHOT)])
  const store = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-selected-run", { initial: { id: "" } }) : [{ id: "" }, () => {}]
  const [selectedID, setSelectedID] = createSignal(store[0].id || "")
  let stopped = false
  let pollTimer
  let inFlight = false
  const pollMs = pollIntervalMs(options.statusPollMs)
  const color = (key) => key === "base" ? theme.text.base
    : key === "muted" ? theme.text.muted
    : key === "action" ? theme.text.action.primary.base
    : theme.text.feedback[key]?.base ?? theme.text.muted
  const mount = createRowMount({ createElement, insert, setProp }, {
    color, BOLD, onRow: (item) => select(item.runID), hoverBg: theme.background.raised.high,
  })
  const rows = () => snapshotLineItems(snapshot(), error())

  function select(id) {
    if (!id) return
    setSelectedID(id)
    void store[1]((draft) => { draft.id = id }).catch(() => {})
    ctx.ui.panel.open(PANEL_NAME)
  }
  function sync() {
    const snap = snapshot()
    setFooterItems([metricsLine(snap)])
    const run = [...snap.active, ...snap.recent].find((r) => r.id === selectedID())
    setDetailItems(detailLines(run, snap.metrics, timers.now()))
  }

  // The reconciler's renderer is only active inside a slot render callback.
  // Creating nodes here reaches it before any renderer exists and throws
  // "No renderer found", so the first render callback builds every node once.
  let nodes = null
  function ensureNodes() {
    if (nodes) return nodes
    const listBox = () => {
      const box = createElement("box")
      setProp(box, "flexDirection", "column")
      return box
    }
    const sidebar = listBox()
    const footer = listBox()
    const detail = createElement("box")
    setProp(detail, "flexDirection", "column")
    setProp(detail, "flexGrow", 1)
    nodes = {
      sidebar, footer, detail,
      applySidebar: mount(sidebar, rows, MAX_ROWS),
      applyFooter: mount(footer, footerItems, FOOTER_ROWS),
      applyDetail: mount(detail, detailItems, DETAIL_ROWS),
    }
    nodes.applySidebar()
    nodes.applyFooter()
    sync()
    nodes.applyDetail()
    return nodes
  }

  let pending = false
  const refresh = async () => {
    if (stopped) return
    if (inFlight) { pending = true; return }
    inFlight = true
    try {
      const next = await source.snapshot()
      if (stopped) return
      setSnapshot(next ?? EMPTY_SNAPSHOT)
      setError(null)
    } catch (cause) { if (!stopped) setError(errorMessage(cause)) }
    finally { inFlight = false }
    if (!stopped && nodes) {
      sync()
      nodes.applySidebar()
      nodes.applyFooter()
      nodes.applyDetail()
    }
    if (pending && !stopped) {
      pending = false
      void refresh()
    }
  }
  let rpc = null
  try { rpc = typeof ctx?.client?.rpc === "function" ? ctx.client.rpc(BeaconStatusRpc) : null } catch {}
  const bridge = rpc?.events && watcherEnabled(options)
    ? createRunChangeBridge(rpc, tuiLocation(ctx), () => void refresh(), timers) : null
  void refresh()
  if (pollMs > 0) {
    pollTimer = timers.setInterval(() => {
      if (stopped) return
      if (bridge?.active && timers.now() - bridge.lastEventAt < EVENT_IDLE_MS) return
      void refresh()
    }, pollMs)
  }
  const removeKeymap = typeof ctx.keymap?.layer === "function"
    ? ctx.keymap.layer(() => ({ mode: "global", commands: [detailCommand((name) => ctx.ui.panel.open(name))] }))
    : null
  const disposers = [
    ctx.ui.slot({ append: "sidebar.content", render: () => ensureNodes().sidebar }),
    ctx.ui.slot({ append: "home.footer.status", render: () => ensureNodes().footer }),
    ctx.ui.slot({ append: "session.panel", render: (input) => {
      if (input.name !== PANEL_NAME) return null
      const n = ensureNodes()
      sync()
      n.applyDetail()
      return n.detail
    } }),
    // Keymap registration happens once in setup; this slot only keeps the
    // registration point stable for the operator's layout.
    ctx.ui.slot({ append: "app", render: () => null }),
  ]
  return () => {
    stopped = true
    timers.clearInterval(pollTimer)
    bridge?.stop()
    if (typeof removeKeymap === "function") removeKeymap()
    disposers.forEach((d) => d?.())
  }
}

export const beaconStatusDefinition = { id: "beacon-project-status", setup }
export function defineBeaconStatusPlugin(Plugin) { return Plugin.define(beaconStatusDefinition) }
let Plugin
try { ;({ Plugin } = await import("@opencode/plugin/tui")) } catch { Plugin = { define: (d) => d } }
export default defineBeaconStatusPlugin(Plugin)
