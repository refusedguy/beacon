// Slot construction for the Beacon status panel. A slot allocates its nodes
// only when the host first renders it and reuses them until the host destroys
// the tree.
import { createBox, createLineMount } from "./ui_nodes.js"
import { createSidebarMount } from "./ui_sidebar.js"
import { createDetailMount } from "./ui_detail_view.js"
import { cardModel, createCardMount } from "./ui_card_view.js"
import { blockedOpenFrom, detailModel, metricsLine, sidebarModel } from "./ui_view.js"

export const FOOTER_ROWS = 1
export const SLOT_KEYS = ["sidebar", "footer", "detail", "card"]

export function createSlots(env) {
  const { ui, theme, BOLD, color, timers, flags, signals, handlers } = env
  const slots = Object.create(null)

  function applySlot(key) {
    const slot = slots[key]
    if (!slot || slot.node.isDestroyed) return
    if (key === "sidebar") {
      slot.apply(
        sidebarModel(signals.m(), signals.error(), timers.now(), env.sidebarOpen, env.epic.open, blockedOpenFrom(env.epic.open)),
        signals.error(),
      )
    } else if (key === "footer") slot.apply([metricsLine(signals.m())])
    else if (key === "card") slot.apply(cardModel(flags.currentCard, signals.m().queue, env.epic.open))
    else slot.apply(detailModel(signals.selectedRun(), signals.m().metrics, timers.now(), env.detailSections), signals.selectedRun())
  }

  function applyAll() { for (const key of SLOT_KEYS) applySlot(key) }

  function ensureSlot(key) {
    const slot = slots[key]
    if (slot && !slot.node.isDestroyed) return slot
    let entry
    if (key === "sidebar") {
      entry = createSidebarMount(ui, {
        color, BOLD, onItem: handlers.onSidebarItem, onCard: handlers.onCard,
        hoverBg: theme.background.raised.high,
      })
    } else if (key === "card") {
      entry = createCardMount(ui, {
        color, BOLD, onCopy: handlers.copyCard, onClose: handlers.onClose, onBack: handlers.onBack,
        onItem: handlers.onCardItem, setProp: ui.setProp,
      })
    } else if (key === "footer") {
      const footer = createBox(ui, { flexDirection: "column", minWidth: 0 })
      const mount = createLineMount(ui, { color, BOLD, maxLines: FOOTER_ROWS })
      mount.node.forEach((node) => ui.insert(footer, node))
      entry = { node: footer, apply: (items) => mount.apply(items) }
    } else {
      entry = createDetailMount(ui, {
        setProp: ui.setProp, color, BOLD,
        onItem: handlers.onDetailItem,
        onAction: handlers.onDetailAction,
        onToggle: handlers.onToggle,
        onClose: handlers.onClose,
      })
    }
    slots[key] = entry
    applySlot(key)
    return entry
  }

  return { slots, applySlot, applyAll, ensureSlot }
}
