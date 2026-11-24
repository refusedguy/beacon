// Pure view helpers for the Beacon panels. No TUI imports live here so these
// functions stay testable with plain Node and shared by every slot.
import {
  DECISION_LIMIT, EMPTY_SNAPSHOT, PANEL_NAME, REASON_WIDTH, RECENT_LIMIT,
  normalizeSnapshot, str,
} from "./ui_model.js"

export const STATE_BAR_WIDTH = 12
export const STATE_BAR_KEYS = Object.freeze(["running", "waiting_ci", "blocked", "merged"])
const STATE_BAR_COLOR = Object.freeze({ running: "success", waiting_ci: "info", blocked: "error", merged: "success" })
const STATE_BAR_LETTER = Object.freeze({ running: "R", waiting_ci: "C", blocked: "B", merged: "M" })

const STATE_VIEW = Object.freeze({
  pending: { marker: "○", color: "muted", label: "PEND" },
  running: { marker: "●", color: "success", label: "RUN" },
  waiting_ci: { marker: "◐", color: "info", label: "CI" },
  needs_rebase: { marker: "◆", color: "warning", label: "REBASE" },
  needs_agent_fix: { marker: "◆", color: "warning", label: "FIX" },
  blocked: { marker: "▲", color: "error", label: "BLK" },
  merged: { marker: "✔", color: "success", label: "MERGED" },
  closed: { marker: "✖", color: "muted", label: "CLOSED" },
})
export const stateView = (state) => STATE_VIEW[str(state).toLowerCase()] ?? { marker: "·", color: "muted", label: "?" }
export const SIDEBAR_OPEN = Object.freeze({ active: true, recent: true })
export const DETAIL_OPEN = Object.freeze({ why: true, checks: true, timeline: true, technical: false })
export const ACTION_NAMES = Object.freeze(["retry", "approve", "cancel", "copy"])

export function truncate(value, width) {
  const text = str(value)
  return text.length <= width ? text : width <= 1 ? text.slice(0, width) : `${text.slice(0, width - 1)}…`
}
export function shortTaskID(value) {
  const id = str(value)
  return id ? (id.length > 10 ? id.slice(-8) : id) : "-"
}
export function elapsed(ms) {
  if (!Number.isFinite(ms) || ms < 0) return ""
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`
}
function timeOf(v) {
  if (v == null || v === "") return 0
  if (typeof v === "number") return Number.isFinite(v) ? v : 0
  const n = Number(v)
  if (Number.isFinite(n) && n > 0) return n
  const t = Date.parse(str(v))
  return Number.isFinite(t) ? t : 0
}
export const ageOf = (run, now) => { const t = Math.max(timeOf(run?.updatedAt), timeOf(run?.createdAt)); return t ? elapsed(now - t) : "" }
export const tookOf = (run) => { const a = timeOf(run?.createdAt), b = timeOf(run?.updatedAt); return a && b ? elapsed(b - a) : "" }
const REASON_RULES = [
  [/^workspace:\s*conflict:\s*refusing to remove dirty worktree\b.*/i, "dirty worktree"],
  [/^workspace:\s*conflict:\s*/i, ""],
  [/^workspace:\s*/i, ""],
  [/^conflict:\s*/i, ""],
  [/^pull request closed\b.*/i, "PR closed"],
  [/pull request closed/i, "PR closed"],
  [/^reconciler:\s*/i, ""],
  [/^harness:\s*/i, ""],
  [/^mark in workspace:\s*/i, ""],
  [/^spawn:\s*/i, "spawn: "],
  [/^status:\s*/i, ""],
]
export function compactReason(reason) {
  let text = str(reason).replace(/\s+/g, " ").trim()
  if (!text) return ""
  for (const [pattern, replacement] of REASON_RULES) {
    if (pattern.test(text)) { text = text.replace(pattern, replacement); break }
  }
  return truncate(text.replace(/^:\s*/, "").replace(/\s+at\s+\/.*$/i, "").trim(), REASON_WIDTH)
}
export function phaseLabel(run, now = Date.now()) {
  const d = run.durations ?? {}
  const state = str(run.state).toLowerCase()
  const phase = state === "waiting_ci" ? ["ci", d.waitingCI]
    : state === "blocked" ? ["block", d.blocked]
      : state === "running" ? ["run", d.running] : null
  if (phase && Number(phase[1]) > 0) return `${phase[0]} ${elapsed(Number(phase[1]) * 1000)}`
  return ageOf(run, now)
}
export const mergedReason = (run) => str(run?.reason).trim() || (run?.pr ? `merged PR #${run.pr}` : "merged to main")

export function activeRuns(snapshot = EMPTY_SNAPSHOT) {
  const seen = new Set()
  return normalizeSnapshot(snapshot).active.filter((run) => {
    if (!run.id || seen.has(run.id)) return false
    seen.add(run.id)
    return true
  })
}
// A closed attempt is noise once the task is active again. Repeated closed
// attempts of one task collapse into its newest terminal run.
export function recentRuns(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const activeTasks = new Set(activeRuns(view).map((r) => str(r.taskID)).filter(Boolean))
  const seenTasks = new Set()
  const seenIDs = new Set()
  return view.recent.filter((run) => {
    const task = str(run.taskID)
    if (!run.id || seenIDs.has(run.id) || activeTasks.has(task) || seenTasks.has(task)) return false
    seenIDs.add(run.id)
    if (task) seenTasks.add(task)
    return true
  }).slice(0, RECENT_LIMIT)
}

export function stateBarItem(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const active = activeRuns(view)
  const recent = recentRuns(view)
  const counts = { running: 0, waiting_ci: 0, blocked: 0, merged: 0 }
  for (const run of [...active, ...recent]) {
    const state = str(run.state).toLowerCase()
    if (Object.hasOwn(counts, state)) counts[state] += 1
  }
  const total = STATE_BAR_KEYS.reduce((sum, key) => sum + counts[key], 0)
  const filled = Math.min(total, STATE_BAR_WIDTH)
  const share = total ? filled / total : 0
  const exact = STATE_BAR_KEYS.map((key) => counts[key] * share)
  const cells = exact.map(Math.floor)
  let spare = filled - cells.reduce((sum, n) => sum + n, 0)
  const byFraction = exact.map((value, i) => ({ i, fraction: value - cells[i] })).sort((a, b) => b.fraction - a.fraction)
  for (let i = 0; spare > 0; i += 1, spare -= 1) cells[byFraction[i].i] += 1
  const segments = STATE_BAR_KEYS.map((key, i) => ({ text: "█".repeat(cells[i]), color: STATE_BAR_COLOR[key] }))
  segments.push({ text: "░".repeat(STATE_BAR_WIDTH - filled), color: "muted" })
  const bar = segments.map((s) => s.text).join("")
  const mergeTotal = view.metrics?.mergeCount ?? counts.merged
  const legend = STATE_BAR_KEYS.map((key) => `${STATE_BAR_LETTER[key]}${key === "merged" ? mergeTotal : counts[key]}`).join(" ")
  const parts = []
  if (!active.length) parts.push("0 active")
  if (counts.running) parts.push(`${counts.running} running`)
  if (counts.waiting_ci) parts.push(`${counts.waiting_ci} in CI`)
  if (counts.blocked) parts.push(`${counts.blocked} blocked`)
  if (counts.merged) parts.push(`${counts.merged} done`)
  const summary = parts.length ? parts.join(" · ") : "no runs"
  return {
    segments, bar, counts, mergeTotal, total, filled, legend, summary,
    content: `states ${bar} ${legend}`,
    fg: counts.blocked > 0 ? "error" : "base",
  }
}

// Two pooled line slots per run: identity on line one, reference and live
// duration and cause on line two. No fixed title width: the name cell grows.
export function runLineItems(run, now = Date.now(), recent = false) {
  const v = stateView(run.state)
  const state = str(run.state).toLowerCase()
  const title = str(run.title) || shortTaskID(run.taskID)
  const ref = run.pr ? `#${run.pr}` : shortTaskID(run.taskID)
  const duration = recent ? tookOf(run) : phaseLabel(run, now)
  const reason = state === "merged" ? "" : compactReason(run.reason)
  return [
    { marker: v.marker, markerSpace: true, name: title, status: v.label, color: v.color, bold: true, runID: run.id },
    { marker: ref, markerSpace: true, name: duration, status: reason, color: "muted", runID: run.id },
  ]
}
const sectionItem = (label, count, key, open) => ({
  marker: open ? "▼" : "▶", name: label, status: String(count),
  color: "action", bold: true, sectionKey: key,
})

export function sidebarModel(snapshot = EMPTY_SNAPSHOT, error = null, now = Date.now(), open = SIDEBAR_OPEN) {
  const view = normalizeSnapshot(snapshot)
  const bar = stateBarItem(view)
  const active = activeRuns(view)
  const recent = recentRuns(view)
  const items = [
    { marker: "", name: "Beacon", status: bar.summary, color: "action", bold: true },
    { marker: "", name: bar.legend, status: "", color: "muted" },
  ]
  if (error) items.push({ marker: "", name: `[stale] ${truncate(error, REASON_WIDTH)}`, status: "", color: "warning" })
  const listItems = []
  listItems.push(sectionItem("Active", active.length, "active", open.active !== false))
  if (open.active !== false) {
    if (!active.length) listItems.push({ marker: "", name: "No active runs", status: "", color: "muted" })
    active.forEach((run) => listItems.push(...runLineItems(run, now)))
    listItems.push({ marker: "▶", name: "Dispatch", status: "", color: "success", bold: true, action: "dispatch" })
  }
  listItems.push(sectionItem("Recent", recent.length, "recent", open.recent !== false))
  if (open.recent !== false) {
    if (!recent.length) listItems.push({ marker: "", name: "No recent runs", status: "", color: "muted" })
    recent.forEach((run) => listItems.push(...runLineItems(run, now, true)))
  }
  return { ...bar, items: [...items, ...listItems], listItems, active, recent, view }
}
export const snapshotLineItems = (snapshot, error, now) => sidebarModel(snapshot, error, now).items
export const renderSnapshotLines = (snapshot, error, now) => snapshotLineItems(snapshot, error, now)
  .map((i) => [i.marker, i.name, i.status].filter((x) => x !== "" && x != null).join(" "))
export const metricsLine = (snapshot = EMPTY_SNAPSHOT) => {
  const item = stateBarItem(snapshot)
  return { marker: "", name: `Beacon ${item.summary}`, status: item.legend, color: "muted" }
}

const stamp = (value) => { const t = str(value); return t.length >= 19 ? t.slice(11, 19) : t || "?" }
export function detailActions(run) {
  if (!run) return []
  const state = str(run.state).toLowerCase()
  const active = ["pending", "running", "waiting_ci", "needs_rebase", "needs_agent_fix"].includes(state)
  const actions = []
  if (state === "blocked") actions.push("retry")
  if (state === "waiting_ci" || (run.pr && active)) actions.push("approve")
  if (active) actions.push("cancel")
  actions.push("copy")
  return actions
}
function phaseSummary(run) {
  const d = run.durations ?? {}
  return `Run ${shortTaskID(run.id)} · Coding ${elapsed((d.running ?? 0) * 1000)} · CI ${elapsed((d.waitingCI ?? 0) * 1000)} · Blocked ${elapsed((d.blocked ?? 0) * 1000)}`
}
export function detailModel(run, metrics = {}, now = Date.now(), open = DETAIL_OPEN) {
  if (!run) return {
    badge: "", pr: "", branch: "", title: "No run selected", task: "", phase: "",
    actions: [], rows: [{ text: "No run selected", color: "muted" }],
  }
  const v = stateView(run.state)
  const state = str(run.state).toLowerCase()
  const rows = []
  const isOpen = (key) => key === "technical" ? open.technical === true : open[key] !== false
  const header = (label, key) => rows.push({
    marker: isOpen(key) ? "▼" : "▶", name: label, status: "",
    color: "action", bold: true, sectionKey: key,
  })
  const row = (name, status = "", color = "muted", marker = "") => rows.push({ marker, name, status, color })
  header("Why / Blocker", "why")
  if (isOpen("why")) {
    const reason = state === "merged" ? mergedReason(run) : str(run.reason).trim()
    row(reason || "No reason recorded", "", state === "blocked" || state === "closed" ? "error" : "base", "↳")
  }
  header("CI Checks", "checks")
  if (isOpen("checks")) {
    const checks = run.checks ?? []
    if (!checks.length) row("(none)")
    else checks.forEach((c) => row(`${c.name || "check"} ${c.state || "unknown"}`, c.durationSeconds ? elapsed(c.durationSeconds * 1000) : "", c.state === "failure" ? "error" : "muted"))
  }
  header("Timeline", "timeline")
  if (isOpen("timeline")) {
    const timeline = [...(run.timeline ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
    if (!timeline.length) row("(none)")
    else timeline.forEach((e) => row(`${stamp(e.at)} ${e.kind || "event"}${e.state ? ` → ${e.state}` : ""}`))
  }
  header("Technical", "technical")
  if (isOpen("technical")) {
    row(run.id || "-", "", "base", "Run ID")
    row(run.sha || "-", "", "base", "SHA")
    const attempts = `Rebase ${run.rebaseAttempts ?? 0}/${run.maxAttempts || "?"} · Fix ${run.fixAttempts ?? 0}/${run.maxAttempts || "?"}`
    row(attempts, "", "base", "Attempts")
    const all = [...(run.decisions ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
    const decisions = all.slice(-DECISION_LIMIT)
    if (all.length > decisions.length) row(`... ${all.length - decisions.length} earlier decisions`)
    if (!decisions.length) row("(none)")
    else decisions.forEach((d) => row(`${stamp(d.at)} ${d.kind || "decision"} ${d.verdict || d.action || ""}`.trimEnd()))
  }
  return {
    badge: `${v.marker} ${v.label}`,
    badgeColor: v.color,
    pr: run.pr ? `#${run.pr}` : "No PR",
    branch: run.branch || "-",
    title: str(run.title) || str(run.taskID) || "-",
    task: run.taskID || "-",
    phase: phaseSummary(run),
    actions: detailActions(run),
    rows,
  }
}
export const detailLines = (run, metrics, now, open) => detailModel(run, metrics, now, open).rows
export function detailCommand(open) {
  return {
    id: "beacon.run.detail", title: "Beacon run detail", description: "Open the selected Beacon run",
    group: "Beacon", bind: "ctrl+g", palette: true, slash: { name: "beacon-detail" },
    run: () => open(PANEL_NAME),
  }
}
