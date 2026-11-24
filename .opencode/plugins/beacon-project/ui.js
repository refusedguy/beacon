// Beacon status panel and run detail for the OpenCode TUI. The view model,
// node pools and action handlers live in sibling files so each stays small.
export * from "./ui_model.js"
export * from "./ui_view.js"
export * from "./ui_bridge.js"
export * from "./ui_nodes.js"
export * from "./ui_sidebar.js"
export * from "./ui_detail_view.js"
export * from "./ui_actions.js"
export * from "./ui_alerts.js"
export * from "./ui_card_view.js"

import {
  EVENT_IDLE_MS, PANEL_NAME, BeaconStatusRpc, EMPTY_SNAPSHOT,
  cleanErrorMessage, createStatusSource, readCard, str, tuiLocation,
} from "./ui_model.js"
import {
  DETAIL_OPEN, SIDEBAR_OPEN, detailCommand, detailModel,
  metricsLine, sidebarModel,
} from "./ui_view.js"
import { compactReason } from "./ui_alerts.js"
import { sessionIdentity } from "./session_identity.js"
import { createBox, createLineMount } from "./ui_nodes.js"
import { createSidebarMount } from "./ui_sidebar.js"
import { createDetailMount } from "./ui_detail_view.js"
import { clipboardCopier, createActionRunner } from "./ui_actions.js"
import { CARD_PANEL_NAME, cardModel, createCardMount, fixPrompt } from "./ui_card_view.js"
import { createRunChangeBridge } from "./ui_bridge.js"
import { projectRoot } from "./location.js"
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

// One notification per run that reaches a terminal or blocked state. The
// caller owns the state map so each setup keeps its own first-render baseline.
export function collectTransitionToasts(previous, snapshot) {
  const runs = new Map()
  for (const run of [...(snapshot?.active ?? []), ...(snapshot?.recent ?? [])]) {
    if (run?.id && !runs.has(run.id)) runs.set(run.id, run)
  }
  const toasts = []
  for (const [id, run] of runs) {
    const state = str(run.state).toLowerCase()
    const before = previous.get(id)
    previous.set(id, state)
    if (before === undefined || before === state) continue
    if (state === "merged") {
      toasts.push({ message: `✔ Merged #${run.pr || "?"}: ${str(run.title) || str(run.taskID)}`, variant: "success" })
    } else if (state === "blocked" || state === "closed") {
      toasts.push({ message: `▲ ${str(run.taskID) || id}: ${compactReason(run.reason)}`, variant: state === "blocked" ? "error" : "warning" })
    }
  }
  return toasts
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
  const root = deps.root ?? projectRoot(ctx) ?? process.cwd()
  const { sessionID, model } = await sessionIdentity(ctx)
  const source = createStatusSource(ctx, {
    root, limits: snapshotLimits(options), deps: deps.cardDeps ?? {}, sessionID, model,
  })
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
          : key === "panel" ? theme.background?.raised?.base
            : key === "border" ? (theme.border?.raised?.base ?? theme.text.action.primary.base)
            : theme.text.feedback[key]?.base ?? theme.text.muted
  let detailOpen = false
  let cardOpen = false
  let currentCard = null
  let detailClose = null
  let cardClose = null
  let stopped = false
  let pollTimer
  let inFlight = false
  const ui = { createElement, insert, setProp }
  const copyText = clipboardCopier(ctx)
  const m = () => snapshot()
  const selectedRun = () => [...m().active, ...m().recent].find((r) => r.id === selectedID()) ?? null

  function applySlot(key) {
    const slot = slots[key]
    if (!slot || slot.node.isDestroyed) return
    if (key === "sidebar") slot.apply(sidebarModel(m(), error(), timers.now(), sidebarOpen), error())
    else if (key === "footer") slot.apply([metricsLine(m())])
    else if (key === "card") slot.apply(cardModel(currentCard))
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
    if (current && typeof current === "object") {
      return current.name === PANEL_NAME || current.name === CARD_PANEL_NAME
    }
    return detailOpen || cardOpen
  }
  function closePanel() {
    detailOpen = false
    cardOpen = false
    const close = cardClose ?? detailClose
    if (typeof close === "function") close()
    else if (typeof ctx.ui.panel?.close === "function") ctx.ui.panel.close()
  }
  function openDetail() {
    detailOpen = true
    cardOpen = false
    cardClose = null
    ctx.ui.panel.open(PANEL_NAME)
  }
  function showCard(id) {
    const card = readCard(root, id, deps.cardDeps ?? {})
    if (!card) {
      ctx.ui?.toast?.show?.({ message: `Card ${id} is not available on disk`, variant: "warning" })
      return
    }
    currentCard = card
    cardOpen = true
    detailOpen = false
    detailClose = null
    ctx.ui.panel.open(CARD_PANEL_NAME)
    applySlot("card")
  }
  const copyCard = (kind) => {
    const card = currentCard
    if (!card) return false
    if (kind === "id") return copyText(card.id, `ID ${card.id}`)
    if (kind === "path") return copyText(card.path, `path ${card.path}`)
    if (kind === "fix") return copyText(fixPrompt(card), "fix prompt")
    return false
  }
  function select(id) {
    if (!id) return
    if (selectedID() === id && panelIsOpen()) { closePanel(); return }
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
  const SLOT_KEYS = ["sidebar", "footer", "detail", "card"]
  function ensureSlot(key) {
    const slot = slots[key]
    if (slot && !slot.node.isDestroyed) return slot
    let entry
    if (key === "sidebar") {
      entry = createSidebarMount(ui, {
        color, BOLD, onItem: handleSidebarItem, onCard: showCard, hoverBg: theme.background.raised.high,
      })
    } else if (key === "card") {
      entry = createCardMount(ui, { color, BOLD, onCopy: copyCard, setProp })
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
        onToggle: () => { if (panelIsOpen()) closePanel(); else openDetail() },
        onClose: closePanel,
      })
    }
    slots[key] = entry
    applySlot(key)
    return entry
  }

  let pending = false
  let failures = 0
  let stale = false
  const lastSeenState = new Map()
  const notify = (snapshot) => {
    for (const toast of collectTransitionToasts(lastSeenState, snapshot)) {
      ctx.ui?.toast?.show?.(toast)
    }
  }
  const refresh = async () => {
    if (stopped) return
    if (inFlight) { pending = true; return }
    inFlight = true
    try {
      const next = await source.snapshot()
      if (stopped) return
      const snap = next ?? EMPTY_SNAPSHOT
      // A snapshot taken during an outage may miss transitions. Skip toasts
      // until a later, confirmed poll reports them.
      if (!stale) notify(snap)
      setSnapshot(snap)
      failures = 0
      stale = false
      setError(null)
    } catch (cause) {
      if (stopped) return
      failures += 1
      if (failures >= 2) {
        stale = true
        setError(cleanErrorMessage(cause))
      }
    }
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
      if (panelIsOpen()) closePanel()
      else openDetail()
    })] }))
    : null
  const disposers = [
    ctx.ui.slot({ append: "sidebar.content", render: () => ensureSlot("sidebar").node }),
    ctx.ui.slot({ append: "home.footer.status", render: () => ensureSlot("footer").node }),
    ctx.ui.slot({ append: "session.panel", render: (input) => {
      const name = input?.name
      if (name === CARD_PANEL_NAME) {
        if (typeof input?.close === "function") cardClose = input.close
        cardOpen = true
        const slot = ensureSlot("card")
        applySlot("card")
        return slot.node
      }
      if (name !== PANEL_NAME) return null
      if (typeof input?.close === "function") detailClose = input.close
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
