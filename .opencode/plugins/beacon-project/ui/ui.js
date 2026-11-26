// Beacon status panel and run detail for the OpenCode TUI. The view model,
// slots, navigation, refresh and action handlers live in sibling files so
// setup stays composition and each module stays small.
import { Plugin } from "@opencode/plugin/tui"
import { runtimeImport } from "./ui_runtime.js"

export * from "./ui_model.js"
export * from "./ui_view.js"
export * from "./ui_bridge.js"
export * from "./ui_nodes.js"
export * from "./ui_sidebar.js"
export * from "./ui_detail_view.js"
export * from "./ui_actions.js"
export * from "./ui_alerts.js"
export * from "./ui_card_view.js"
export * from "./ui_slots.js"
export * from "./ui_navigation.js"
export * from "./ui_refresh.js"

import { EMPTY_SNAPSHOT, PANEL_NAME, createStatusSource } from "./ui_model.js"
import { DETAIL_OPEN, SIDEBAR_OPEN, detailCommand, discardStoredEpicOpen } from "./ui_view.js"
import { sessionIdentity } from "../state/session_identity.js"
import { clipboardCopier } from "./ui_actions.js"
import { cleanupWrapper } from "../protocol/cleanup.js"
import { CARD_PANEL_NAME, cardBackCommand, createEpicState } from "./ui_card_view.js"
import { projectRoot } from "../state/location.js"
import { pluginOptions, snapshotLimits, statusPanelEnabled } from "../state/options.js"
import { createSlots } from "./ui_slots.js"
import { createRefresh } from "./ui_refresh.js"
import { createNavigation } from "./ui_navigation.js"

async function setup(ctx, deps = {}) {
  const options = deps.options ?? pluginOptions(ctx)
  if (!statusPanelEnabled(options)) return () => {}
  const load = deps.import ?? runtimeImport
  const [{ createElement, insert, setProp }, { createSignal }] = await Promise.all([
    load("@opentui/solid"), load("solid-js"),
  ])
  let BOLD = 0
  try { ({ TextAttributes: { BOLD } } = await load("@opentui/core")) } catch (error) {
    console.debug(`beacon status panel: optional TUI core import failed: ${error?.message ?? error}`)
  }
  const root = deps.root ?? projectRoot(ctx)
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
  const [selectedID, setSelectedID] = createSignal(store[0].id || "")
  const sidebarStore = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-sidebar-sections", { initial: { ...SIDEBAR_OPEN } }) : [{ ...SIDEBAR_OPEN }, () => {}]
  const detailStore = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-detail-sections", { initial: { ...DETAIL_OPEN } }) : [{ ...DETAIL_OPEN }, () => {}]
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
  const m = () => snapshot()
  const selectedRun = () => [...m().active, ...m().recent].find((r) => r.id === selectedID()) ?? null

  // Shared mutable state is explicit so slots, navigation and refresh can
  // coordinate without hidden closure variables.
  const flags = {
    detailOpen: false,
    cardOpen: false,
    currentCard: null,
    cardHistory: [],
    detailClose: null,
    cardClose: null,
    stopped: false,
    pollTimer: undefined,
    inFlight: false,
    pending: false,
    generation: 0,
    failures: 0,
    stale: false,
  }
  const env = {
    ctx, deps, theme, BOLD, color, timers, options, source,
    sidebarOpen, detailSections,
    copyText: clipboardCopier(ctx),
    signals: { snapshot, setSnapshot, error, setError, selectedID, setSelectedID, m, selectedRun },
    stores: { store, sidebarStore, detailStore },
    flags,
    lastSeenState: new Map(),
    handlers: {},
    epic: null,
    refresh: null,
    bridge: null,
  }
  const ui = { createElement, insert, setProp }
  env.ui = ui

  const slots = createSlots(env)
  env.epic = discardStoredEpicOpen(createEpicState(ctx, () => slots.applyAll()))

  const refresh = createRefresh(env, slots.applyAll)
  env.refresh = refresh.refresh
  env.bridge = refresh.bridge

  const nav = createNavigation(env, slots.applySlot)

  const removeKeymap = typeof ctx.keymap?.layer === "function"
    ? ctx.keymap.layer(() => ({ mode: "global", commands: [
      detailCommand(() => {
        if (nav.panelIsOpen()) nav.closePanel()
        else nav.openDetail()
      }),
      cardBackCommand(nav.backCard),
    ] }))
    : null
  const disposers = [
    ctx.ui.slot({ append: "sidebar.content", render: () => slots.ensureSlot("sidebar").node }),
    ctx.ui.slot({ append: "home.footer.status", render: () => slots.ensureSlot("footer").node }),
    ctx.ui.slot({ append: "session.panel", render: (input) => {
      const name = input?.name
      if (name === CARD_PANEL_NAME) {
        if (typeof input?.close === "function") flags.cardClose = input.close
        flags.cardOpen = true
        const slot = slots.ensureSlot("card")
        slots.applySlot("card")
        return slot.node
      }
      if (name !== PANEL_NAME) return null
      if (typeof input?.close === "function") flags.detailClose = input.close
      const slot = slots.ensureSlot("detail")
      flags.detailOpen = true
      slots.applySlot("detail")
      return slot.node
    } }),
    ctx.ui.slot({ append: "app", render: () => null }),
  ]
  const cleanup = cleanupWrapper("beacon status panel: ")
  return () => {
    flags.stopped = true
    cleanup("poll timer", () => timers.clearInterval(flags.pollTimer))
    cleanup("run watcher", () => env.bridge?.stop())
    cleanup("keymap", removeKeymap)
    disposers.forEach((dispose) => cleanup("slot", dispose))
  }
}

export const beaconStatusDefinition = { id: "beacon-project-status", setup }

// The caller supplies define so tests can build the plugin with a double. The
// host supplies Plugin here, so its own OpenTUI and Solid instances are used.
export function defineBeaconStatus(define) { return define(beaconStatusDefinition) }
export default Plugin.define(beaconStatusDefinition)
