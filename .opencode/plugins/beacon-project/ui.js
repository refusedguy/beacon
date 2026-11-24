// Beacon status panel and run detail for the OpenCode TUI. The view model,
// node pools and action handlers live in sibling files so each stays small.
export * from "./ui_model.js"
export * from "./ui_view.js"
export * from "./ui_bridge.js"
export * from "./ui_nodes.js"
export * from "./ui_sidebar.js"
export * from "./ui_detail_view.js"
export * from "./ui_actions.js"

import {
  EVENT_IDLE_MS, PANEL_NAME, BeaconStatusRpc, EMPTY_SNAPSHOT,
  createStatusSource, errorMessage, tuiLocation,
} from "./ui_model.js"
import {
  DETAIL_OPEN, SIDEBAR_OPEN, detailCommand, detailModel, metricsLine, sidebarModel,
} from "./ui_view.js"
import { createBox, createLineMount } from "./ui_nodes.js"
import { createSidebarMount } from "./ui_sidebar.js"
import { createDetailMount } from "./ui_detail_view.js"
import { createActionRunner } from "./ui_actions.js"
import { createRunChangeBridge } from "./ui_bridge.js"
import { pollIntervalMs, pluginOptions, snapshotLimits, statusPanelEnabled, watcherEnabled } from "./options.js"

export const FOOTER_ROWS = 1
// Compatibility adapter for tests and callers that drive one pooled row list.
export function createRowMount(ui, { color, BOLD = 0, onRow, hoverBg, maxRows }) {
  return (container, items, rowLimit = maxRows) => {
    const mount = createLineMount(ui, { color, BOLD, onItem: onRow, hoverBg, maxLines: rowLimit })
    mount.node.forEach((node) => ui.insert(container, node))
    const apply = () => mount.apply(items())
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
  const store = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-selected-run", { initial: { id: "" } }) : [{ id: "" }, () => {}]
  const sidebarStore = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-sidebar-sections", { initial: { ...SIDEBAR_OPEN } }) : [{ ...SIDEBAR_OPEN }, () => {}]
  const detailStore = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-detail-sections", { initial: { ...DETAIL_OPEN } }) : [{ ...DETAIL_OPEN }, () => {}]
  const [selectedID, setSelectedID] = createSignal(store[0].id || "")
  const sidebarOpen = { ...SIDEBAR_OPEN, ...(sidebarStore[0] ?? {}) }
  // A stale stored object must never reopen Technical on load. The other
  // sections keep their stored value, defaulting to open via DETAIL_OPEN.
  const detailSections = { ...DETAIL_OPEN, ...(detailStore[0] ?? {}), technical: false }
  const color = (key) => key === "base" ? theme.text.base
    : key === "muted" ? theme.text.muted
      : key === "action" ? theme.text.action.primary.base
        : key === "raised" ? theme.background.raised.high
          : key === "border" ? (theme.border?.raised?.base ?? theme.text.action.primary.base)
            : theme.text.feedback[key]?.base ?? theme.text.muted
  let detailOpen = false
  let stopped = false
  let pollTimer
  let inFlight = false
  let panelClose = null
  const ui = { createElement, insert, setProp }
  const m = () => snapshot()
  const selectedRun = () => [...m().active, ...m().recent].find((r) => r.id === selectedID()) ?? null

  function applySlot(key) {
    const slot = slots[key]
    if (!slot || slot.node.isDestroyed) return
    if (key === "sidebar") slot.apply(sidebarModel(m(), error(), timers.now(), sidebarOpen), error())
    else if (key === "footer") slot.apply([metricsLine(m())])
    else slot.apply(detailModel(selectedRun(), m().metrics, timers.now(), detailSections), selectedRun())
  }
  function applyAll() { for (const key of SLOT_KEYS) applySlot(key) }
  function toggleSection(scope, key) {
    const flags = scope === "sidebar" ? sidebarOpen : detailSections
    const persist = scope === "sidebar" ? sidebarStore : detailStore
    flags[key] = !flags[key]
    void persist[1]((draft) => { draft[key] = flags[key] }).catch(() => {})
    applySlot(scope === "sidebar" ? "sidebar" : "detail")
  }
  function panelIsOpen() {
    const current = typeof ctx.ui.panel?.current === "function" ? ctx.ui.panel.current() : null
    if (current && typeof current === "object") return current.name === PANEL_NAME
    return detailOpen
  }
  function closeDetail() {
    detailOpen = false
    if (typeof panelClose === "function") panelClose()
    else if (typeof ctx.ui.panel?.close === "function") ctx.ui.panel.close()
  }
  function openDetail() {
    detailOpen = true
    ctx.ui.panel.open(PANEL_NAME)
  }
  function select(id) {
    if (!id) return
    if (selectedID() === id && panelIsOpen()) { closeDetail(); return }
    setSelectedID(id)
    void store[1]((draft) => { draft.id = id }).catch(() => {})
    openDetail()
  }
  const runAction = createActionRunner(ctx, source, { refresh: () => refresh() })
  const handleSidebarItem = (item) => {
    if (item.sectionKey) toggleSection("sidebar", item.sectionKey)
    else if (item.action === "dispatch") void runAction("dispatch")
    else select(item.runID)
  }

  // Every node is allocated on the first render callback for its slot. OpenCode
  // unmounts slot trees when a panel opens, so each slot rebuilds only when its
  // own node reports isDestroyed.
  const slots = Object.create(null)
  const SLOT_KEYS = ["sidebar", "footer", "detail"]
  function ensureSlot(key) {
    const slot = slots[key]
    if (slot && !slot.node.isDestroyed) return slot
    let entry
    if (key === "sidebar") {
      entry = createSidebarMount(ui, {
        color, BOLD, onItem: handleSidebarItem, hoverBg: theme.background.raised.high,
      })
    } else if (key === "footer") {
      const footer = createBox(ui, { flexDirection: "column", minWidth: 0 })
      const mount = createLineMount(ui, { color, BOLD, maxLines: FOOTER_ROWS })
      mount.node.forEach((node) => insert(footer, node))
      entry = { node: footer, apply: (items) => mount.apply(items) }
    } else {
      entry = createDetailMount(ui, {
        setProp, color, BOLD,
        onItem: (item) => { if (item.sectionKey) toggleSection("detail", item.sectionKey) },
        onAction: (name, opts) => void runAction(name, selectedRun(), opts),
        onToggle: () => { if (panelIsOpen()) closeDetail(); else openDetail() },
        onClose: closeDetail,
      })
    }
    slots[key] = entry
    applySlot(key)
    return entry
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
    if (!stopped) applyAll()
    if (pending && !stopped) { pending = false; void refresh() }
  }
  let rpc = null
  try { rpc = typeof ctx?.client?.rpc === "function" ? ctx.client.rpc(BeaconStatusRpc) : null } catch {}
  const bridge = rpc?.events && watcherEnabled(options)
    ? createRunChangeBridge(rpc, tuiLocation(ctx), () => void refresh(), timers) : null
  void refresh()
  const pollMs = pollIntervalMs(options.statusPollMs)
  if (pollMs > 0) {
    pollTimer = timers.setInterval(() => {
      if (stopped) return
      if (bridge?.active && timers.now() - bridge.lastEventAt < EVENT_IDLE_MS) return
      void refresh()
    }, pollMs)
  }
  const removeKeymap = typeof ctx.keymap?.layer === "function"
    ? ctx.keymap.layer(() => ({ mode: "global", commands: [detailCommand(() => {
      if (panelIsOpen()) closeDetail()
      else openDetail()
    })] }))
    : null
  const disposers = [
    ctx.ui.slot({ append: "sidebar.content", render: () => ensureSlot("sidebar").node }),
    ctx.ui.slot({ append: "home.footer.status", render: () => ensureSlot("footer").node }),
    ctx.ui.slot({ append: "session.panel", render: (input) => {
      if (input.name !== PANEL_NAME) return null
      if (typeof input?.close === "function") panelClose = input.close
      const slot = ensureSlot("detail")
      detailOpen = true
      applySlot("detail")
      return slot.node
    } }),
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
