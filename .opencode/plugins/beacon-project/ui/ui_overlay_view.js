// Pure view model for the full-screen performance overlay. It reads a
// normalized snapshot and allocates no TUI nodes, so a refresh can rebuild the
// table without mounting anything.
import { str } from "./ui_model.js"
import { elapsed, timeOf } from "./time_format.js"

export const OVERLAY_PANEL_NAME = "beacon.overlay"
export const OVERLAY_TITLE = "Beacon performance"

const ACTIVE_STATES = new Set([
  "pending", "running", "waiting_ci", "needs_rebase", "needs_agent_fix", "blocked",
])

// Perf cards carry `perf: true` in card frontmatter and a `[perf]` title
// prefix. The snapshot carries runs, not frontmatter, so a caller that owns
// the perf card list passes it in. A snapshot that already carries perf cards
// wins over the injected list.
const hasPerfMark = (card) => {
  if (!card || typeof card !== "object") return false
  if (card.perf === true || card.perf === "true" || card.meta?.perf === true || card.meta?.perf === "true") return true
  return /^\[perf\]/i.test(str(card.title).trim())
}
function perfCards(snapshot, cards) {
  const fromSnapshot = Array.isArray(snapshot.perfCards) ? snapshot.perfCards
    : Array.isArray(snapshot.cards) ? snapshot.cards : null
  if (fromSnapshot) return fromSnapshot.filter(hasPerfMark)
  const injected = Array.isArray(cards) ? cards : []
  return injected.filter((card) => card?.perf !== false)
}

// One run per task ID. An active run takes priority over a recent run, and a
// run attached to a queue row fills in cards that have not started yet.
function runsByTask(snapshot) {
  const runs = new Map()
  for (const run of [...(Array.isArray(snapshot.active) ? snapshot.active : []),
    ...(Array.isArray(snapshot.recent) ? snapshot.recent : [])]) {
    const taskID = str(run?.taskID)
    if (taskID && !runs.has(taskID)) runs.set(taskID, run)
  }
  for (const item of Array.isArray(snapshot.queue) ? snapshot.queue : []) {
    const taskID = str(item?.run?.taskID)
    if (taskID && !runs.has(taskID)) runs.set(taskID, item.run)
  }
  return runs
}

const stateOf = (card, run) => str(run?.state) || str(card?.state) || str(card?.status) || "unknown"
const prOf = (card, run) => {
  const number = run?.pr ?? card?.pr ?? card?.PRNumber
  return number ? `#${str(number)}` : "-"
}

export function overlayModel(snapshot, now, cards = []) {
  const source = snapshot && typeof snapshot === "object" ? snapshot : {}
  const runs = runsByTask(source)
  const rows = perfCards(source, cards).map((card) => {
    const run = runs.get(str(card?.id)) ?? null
    return {
      id: str(card?.id),
      title: str(card?.title) || str(card?.id),
      state: stateOf(card, run),
      pr: prOf(card, run),
      runID: str(run?.id ?? card?.runID ?? card?.runId),
    }
  })
  const active = rows.filter((row) => ACTIVE_STATES.has(row.state)).length
  const generated = timeOf(source.generatedAt)
  const age = generated ? elapsed(Math.max(0, timeOf(now) - generated)) : ""
  const plural = rows.length === 1 ? "" : "s"
  const summary = `${rows.length} perf card${plural} · ${active} active${age ? ` · updated ${age} ago` : ""}`
  return { title: OVERLAY_TITLE, summary, rows }
}

export function overlayCommand(open) {
  return {
    id: "beacon.perf.overlay", title: "Beacon performance overlay",
    description: "Open the full-screen Beacon performance overlay",
    group: "Beacon", bind: "ctrl+o", palette: true, slash: { name: "beacon-overlay" },
    run: () => open(OVERLAY_PANEL_NAME),
  }
}
