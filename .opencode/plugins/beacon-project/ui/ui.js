// Beacon status panel and run detail for the OpenCode TUI. The view model,
// node pools and action handlers live in sibling files so each stays small.
import { Plugin } from "@opencode/plugin/tui"
import { runtimeImport } from "./ui_runtime.js"

export * from "./ui_model.js"
export {
  BeaconStatusRpc, RUN_CHANGED_EVENT,
  createCliStatusSource, normalizeSnapshot,
} from "./status_rpc.js"
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
  DETAIL_OPEN, SIDEBAR_OPEN, blockedOpenFrom, detailCommand, detailModel, discardStoredEpicOpen,
  metricsLine, sidebarModel, toggleAllBlockedGroups, toggleBlockedGroup,
} from "./ui_view.js"
import { compactReason } from "./ui_alerts.js"
import { sessionIdentity } from "../state/session_identity.js"
import { createBox, createLineMount } from "./ui_nodes.js"
import { createSidebarMount } from "./ui_sidebar.js"
import { createDetailMount } from "./ui_detail_view.js"
import { clipboardCopier, createActionRunner } from "./ui_actions.js"
import { cleanupWrapper } from "./ui_test_helpers.js"
import { CARD_PANEL_NAME, cardBackCommand, cardModel, cardRelativePath, createCardMount, createEpicState, fixPrompt } from "./ui_card_view.js"
import { createRunChangeBridge } from "./ui_bridge.js"
import { projectRoot } from "../state/location.js"
import { pollIntervalMs, pluginOptions, snapshotLimits, statusPanelEnabled, watcherEnabled } from "../state/options.js"

export const FOOTER_ROWS = 1

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
  const sidebarStore = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-sidebar-sections", { initial: { ...SIDEBAR_OPEN } }) : [{ ...SIDEBAR_OPEN }, () => {}]
  const detailStore = typeof ctx.storage?.store === "function"
    ? ctx.storage.store("beacon-detail-sections", { initial: { ...DETAIL_OPEN } }) : [{ ...DETAIL_OPEN }, () => {}]
  const epic = discardStoredEpicOpen(createEpicState(ctx, () => applyAll()))
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
  const cardHistory = []
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
    if (key === "sidebar") slot.apply(sidebarModel(m(), error(), timers.now(), sidebarOpen, epic.open, blockedOpenFrom(epic.open)), error())
    else if (key === "footer") slot.apply([metricsLine(m())])
    else if (key === "card") slot.apply(cardModel(currentCard, m().queue, epic.open))
    else slot.apply(detailModel(selectedRun(), m().metrics, timers.now(), detailSections), selectedRun())
  }
  function applyAll() { for (const key of SLOT_KEYS) applySlot(key) }
  function toggleSection(scope, key) {
    const flags = scope === "sidebar" ? sidebarOpen : detailSections
    const persist = scope === "sidebar" ? sidebarStore : detailStore
    flags[key] = !flags[key]
    void Promise.resolve(persist[1]((draft) => { draft[key] = flags[key] })).catch(() => {})
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
    cardHistory.length = 0
    const close = cardClose ?? detailClose
    if (typeof close === "function") close()
    else if (typeof ctx.ui.panel?.close === "function") ctx.ui.panel.close()
  }
  function openDetail() {
    detailOpen = true
    cardOpen = false
    cardClose = null
    cardHistory.length = 0
    ctx.ui.panel.open(PANEL_NAME)
  }
  function showCard(id, { push = false } = {}) {
    const cardRoot = source.root()
    const card = cardRoot ? readCard(cardRoot, id, deps.cardDeps ?? {}) : null
    if (!card) {
      ctx.ui?.toast?.show?.({ message: `Card ${id} is not available on disk`, variant: "warning" })
      return
    }
    if (push && currentCard && currentCard.id !== card.id) cardHistory.push(currentCard)
    currentCard = card
    cardOpen = true
    detailOpen = false
    detailClose = null
    ctx.ui.panel.open(CARD_PANEL_NAME)
    applySlot("card")
  }
  function backCard() {
    if (cardHistory.length) { showCard(cardHistory.pop().id); return }
    if (!panelIsOpen()) return
    if (selectedRun()) openDetail()
    else closePanel()
  }
  const handleCardItem = (item) => {
    const action = str(item?.action ?? "")
    if (action.startsWith("epic:")) epic.toggle(action.slice(5))
    else if (action.startsWith("card:")) showCard(action.slice("card:".length), { push: true })
  }
  const copyCard = (kind) => {
    const card = currentCard
    if (!card) return false
    if (kind === "id") return copyText(card.id, `ID ${card.id}`)
    if (kind === "path") {
      const path = cardRelativePath(card)
      return copyText(path, `path ${path}`)
    }
    if (kind === "fix") return copyText(fixPrompt(card), "fix prompt")
    return false
  }
  function select(id) {
    if (!id) return
    if (selectedID() === id && panelIsOpen()) { closePanel(); return }
    setSelectedID(id)
    void Promise.resolve(store[1]((draft) => { draft.id = id })).catch(() => {})
    openDetail()
  }
  const runAction = createActionRunner(ctx, source, {
    refresh: () => refresh(),
    // A live watcher supplies the follow-up read. Without one, read again once
    // dispatch settles because the overlapping read may predate the new run.
    afterDispatch: () => (bridge?.active ? undefined : refresh()),
  })
  const handleSidebarItem = (item) => {
    if (item.sectionKey) toggleSection("sidebar", item.sectionKey)
    else if (item.action === "dispatch") void runAction("dispatch")
    else if (item.action === "blocked-toggle") toggleAllBlockedGroups(epic, m().queue)
    else if (item.blockedKey) toggleBlockedGroup(epic, item.blockedKey)
    else if (item.epicKey) epic.toggle(item.epicKey)
    else select(item.runID)
  }

  // Slots allocate nodes on the first render and rebuild only when OpenCode
  // unmounts and destroys their tree.
  const slots = Object.create(null)
  const SLOT_KEYS = ["sidebar", "footer", "detail", "card"]
  function ensureSlot(key) {
    const slot = slots[key]
    if (slot && !slot.node.isDestroyed) return slot
    let entry
    if (key === "sidebar") {
      entry = createSidebarMount(ui, {
        color, BOLD, onItem: handleSidebarItem, onCard: (id) => showCard(id, { push: true }),
        hoverBg: theme.background.raised.high,
      })
    } else if (key === "card") {
      entry = createCardMount(ui, {
        color, BOLD, onCopy: copyCard, onClose: closePanel, onBack: backCard,
        onItem: handleCardItem, setProp,
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
        onToggle: () => { if (panelIsOpen()) closePanel(); else openDetail() },
        onClose: closePanel,
      })
    }
    slots[key] = entry
    applySlot(key)
    return entry
  }

  let pending = false
  // Every refresh request advances the generation. A read that was started by
  // an earlier request is discarded when a newer one arrives, so only the final
  // snapshot wins. Dispatch starts a read while the worker is still running;
  // the watcher's follow-up read supersedes it without rendering stale data.
  let generation = 0
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
    const mine = ++generation
    if (inFlight) { pending = true; return }
    inFlight = true
    let rendered = false
    try {
      const next = await source.snapshot()
      if (stopped) return
      if (mine === generation) {
        // An empty cwd snapshot is not a baseline. A snapshot taken during an
        // outage may miss transitions, so toasts wait for a confirmed poll.
        const empty = !next || (!next.active?.length && !next.recent?.length && !next.queue?.length)
        if (next != null && !(empty && !source.verified())) { if (!stale) notify(next); setSnapshot(next); rendered = true }
        failures = 0
        stale = false
        setError(null)
      }
    } catch (cause) {
      if (stopped) return
      if (mine === generation) {
        failures += 1
        if (failures >= 2) { stale = true; setError(cleanErrorMessage(cause)) }
        rendered = true
      }
    }
    finally { inFlight = false }
    if (rendered && !stopped) applyAll()
    if (pending && !stopped) { pending = false; void refresh() }
  }
  let rpc = null
  try { rpc = typeof ctx?.client?.rpc === "function" ? ctx.client.rpc(BeaconStatusRpc) : null } catch (error) {
    console.debug(`beacon status panel: RPC client unavailable: ${error?.message ?? error}`)
  }
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
    ? ctx.keymap.layer(() => ({ mode: "global", commands: [
      detailCommand(() => {
        if (panelIsOpen()) closePanel()
        else openDetail()
      }),
      cardBackCommand(backCard),
    ] }))
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
  const cleanup = cleanupWrapper("beacon status panel: ")
  return () => {
    stopped = true
    cleanup("poll timer", () => timers.clearInterval(pollTimer))
    cleanup("run watcher", () => bridge?.stop())
    cleanup("keymap", removeKeymap)
    disposers.forEach((dispose) => cleanup("slot", dispose))
  }
}

export const beaconStatusDefinition = { id: "beacon-project-status", setup }

// The caller supplies define so tests can build the plugin with a double. The
// host supplies Plugin here, so its own OpenTUI and Solid instances are used.
export function defineBeaconStatus(define) { return define(beaconStatusDefinition) }
export default Plugin.define(beaconStatusDefinition)
