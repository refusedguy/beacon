// Panel navigation for the Beacon status panel: selection, detail and card
// history, open/close, and the sidebar, card and detail click handlers.
import { PANEL_NAME, readCard, str } from "./ui_model.js"
import { toggleAllBlockedGroups, toggleBlockedGroup } from "./ui_view.js"
import { CARD_PANEL_NAME, cardRelativePath, fixPrompt } from "./ui_card_view.js"
import { createActionRunner } from "./ui_actions.js"

export function createNavigation(env, applySlot) {
  const { ctx, deps, source, sidebarOpen, detailSections, copyText, flags, signals, stores } = env

  function panelIsOpen() {
    const current = typeof ctx.ui.panel?.current === "function" ? ctx.ui.panel.current() : null
    if (current && typeof current === "object") {
      return current.name === PANEL_NAME || current.name === CARD_PANEL_NAME
    }
    return flags.detailOpen || flags.cardOpen
  }

  function closePanel() {
    flags.detailOpen = false
    flags.cardOpen = false
    flags.cardHistory.length = 0
    const close = flags.cardClose ?? flags.detailClose
    if (typeof close === "function") close()
    else if (typeof ctx.ui.panel?.close === "function") ctx.ui.panel.close()
  }

  function openDetail() {
    flags.detailOpen = true
    flags.cardOpen = false
    flags.cardClose = null
    flags.cardHistory.length = 0
    ctx.ui.panel.open(PANEL_NAME)
  }

  function showCard(id, { push = false } = {}) {
    const cardRoot = source.root()
    const card = cardRoot ? readCard(cardRoot, id, deps.cardDeps ?? {}) : null
    if (!card) {
      ctx.ui?.toast?.show?.({ message: `Card ${id} is not available on disk`, variant: "warning" })
      return
    }
    if (push && flags.currentCard && flags.currentCard.id !== card.id) flags.cardHistory.push(flags.currentCard)
    flags.currentCard = card
    flags.cardOpen = true
    flags.detailOpen = false
    flags.detailClose = null
    ctx.ui.panel.open(CARD_PANEL_NAME)
    applySlot("card")
  }

  function backCard() {
    if (flags.cardHistory.length) { showCard(flags.cardHistory.pop().id); return }
    if (!panelIsOpen()) return
    if (signals.selectedRun()) openDetail()
    else closePanel()
  }

  function toggleSection(scope, key) {
    const sectionFlags = scope === "sidebar" ? sidebarOpen : detailSections
    const persist = scope === "sidebar" ? stores.sidebarStore : stores.detailStore
    sectionFlags[key] = !sectionFlags[key]
    void Promise.resolve(persist[1]((draft) => { draft[key] = sectionFlags[key] })).catch(() => {})
    applySlot(scope === "sidebar" ? "sidebar" : "detail")
  }

  function select(id) {
    if (!id) return
    if (signals.selectedID() === id && panelIsOpen()) { closePanel(); return }
    signals.setSelectedID(id)
    void Promise.resolve(stores.store[1]((draft) => { draft.id = id })).catch(() => {})
    openDetail()
  }

  const runAction = createActionRunner(ctx, source, {
    refresh: () => env.refresh(),
    // A live watcher supplies the follow-up read. Without one, read again once
    // dispatch settles because the overlapping read may predate the new run.
    afterDispatch: () => (env.bridge?.active ? undefined : env.refresh()),
  })

  const handleSidebarItem = (item) => {
    if (item.sectionKey) toggleSection("sidebar", item.sectionKey)
    else if (item.action === "dispatch") void runAction("dispatch")
    else if (item.action === "blocked-toggle") toggleAllBlockedGroups(env.epic, signals.m().queue)
    else if (item.blockedKey) toggleBlockedGroup(env.epic, item.blockedKey)
    else if (item.epicKey) env.epic.toggle(item.epicKey)
    else select(item.runID)
  }

  const handleCardItem = (item) => {
    const action = str(item?.action ?? "")
    if (action.startsWith("epic:")) env.epic.toggle(action.slice(5))
    else if (action.startsWith("card:")) showCard(action.slice("card:".length), { push: true })
  }

  const copyCard = (kind) => {
    const card = flags.currentCard
    if (!card) return false
    if (kind === "id") return copyText(card.id, `ID ${card.id}`)
    if (kind === "path") {
      const path = cardRelativePath(card)
      return copyText(path, `path ${path}`)
    }
    if (kind === "fix") return copyText(fixPrompt(card), "fix prompt")
    return false
  }

  Object.assign(env.handlers, {
    onSidebarItem: handleSidebarItem,
    onCard: (id) => showCard(id, { push: true }),
    copyCard,
    onClose: closePanel,
    onBack: backCard,
    onCardItem: handleCardItem,
    onDetailItem: (item) => { if (item.sectionKey) toggleSection("detail", item.sectionKey) },
    onDetailAction: (name, opts) => void runAction(name, signals.selectedRun(), opts),
    onToggle: () => { if (panelIsOpen()) closePanel(); else openDetail() },
  })

  return { panelIsOpen, closePanel, openDetail, backCard, showCard, toggleSection, select }
}
