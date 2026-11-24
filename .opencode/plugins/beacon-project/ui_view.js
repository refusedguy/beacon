// Presentation helpers for the Beacon status panel. Kept apart from the data
// model so both files stay small and easy to test.
import {
  DECISION_LIMIT, EMPTY_SNAPSHOT, PANEL_NAME, RECENT_LIMIT, REASON_WIDTH,
  RULE, TITLE_WIDTH, VALUE_WIDTH, normalizeSnapshot, str,
} from "./ui_model.js"

// One-row state bar in the panel header. A single text node carries the whole
// bar, so the fill is a fixed-width run of block cells and a muted remainder.
export const STATE_BAR_WIDTH = 12
export const STATE_BAR_KEYS = Object.freeze(["running", "waiting_ci", "blocked", "merged"])
const STATE_BAR_LETTER = Object.freeze({ running: "R", waiting_ci: "C", blocked: "B", merged: "M" })

const STATE_VIEW = Object.freeze({
  pending: { marker: "○", color: "muted", label: "PEND" },
  running: { marker: "●", color: "success", label: "RUN" },
  waiting_ci: { marker: "◐", color: "info", label: "CI" },
  needs_rebase: { marker: "◆", color: "warning", label: "REBASE" },
  needs_agent_fix: { marker: "◆", color: "warning", label: "FIX" },
  blocked: { marker: "▲", color: "error", label: "BLOCK" },
  merged: { marker: "✔", color: "success", label: "MERGED" },
  closed: { marker: "✖", color: "muted", label: "CLOSED" },
})
const stateView = (state) => STATE_VIEW[str(state).toLowerCase()] ?? { marker: "·", color: "muted", label: "?" }
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
const ageOf = (run, now) => { const t = Math.max(timeOf(run.updatedAt), timeOf(run.createdAt)); return t ? elapsed(now - t) : "" }
const tookOf = (run) => { const a = timeOf(run.createdAt), b = timeOf(run.updatedAt); return a && b ? elapsed(b - a) : "" }
const FAILURE_CODE = /^[A-Z][A-Z0-9_]*$|^[a-z]+(?:_[a-z0-9]+)+$/
const failureReason = (reason) => (FAILURE_CODE.test(str(reason)) ? str(reason) : "")
const keep = (run, seen) => {
  const key = run.id || run.taskID || run.title
  if (!key || seen.has(key)) return false
  seen.add(key)
  return true
}
export function runLineItems(run, now = Date.now(), recent = false) {
  const v = stateView(run.state)
  const title = run.title && run.title !== run.taskID ? truncate(run.title, TITLE_WIDTH) : ""
  const name = [shortTaskID(run.taskID), title].filter(Boolean).join(" ")
  const meta = [run.pr ? `#${run.pr}` : "", recent ? tookOf(run) : ageOf(run, now)].filter(Boolean).join(" ")
  const reason = recent ? failureReason(run.reason) : truncate(run.reason, REASON_WIDTH)
  return [
    { marker: v.marker, name, status: v.label, color: v.color, bold: true, runID: run.id },
    { marker: "", name: meta, status: reason, color: "muted", runID: run.id },
  ]
}
const section = (label, count) => ({ marker: "▼", name: label, status: count == null ? "" : String(count), color: "action", bold: true })
export function metricCounts(view = EMPTY_SNAPSHOT) {
  const m = view.metrics ?? {}
  return {
    active: view.active.length,
    blocked: m.blockedCount ?? view.active.filter((r) => r.state === "blocked").length,
    rebase: m.rebaseCount ?? 0,
    fix: m.fixCount ?? 0,
  }
}
// Counts come from active and recent runs, which already carry every state the
// panel shows. Up to STATE_BAR_WIDTH cells are handed out proportionally with
// the largest-remainder method; the unused tail is drawn with shade cells.
export function stateBarItem(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const counts = { running: 0, waiting_ci: 0, blocked: 0, merged: 0 }
  for (const run of [...view.active, ...view.recent]) {
    const state = str(run.state).toLowerCase()
    if (Object.hasOwn(counts, state)) counts[state] += 1
  }
  const total = STATE_BAR_KEYS.reduce((sum, key) => sum + counts[key], 0)
  const filled = Math.min(total, STATE_BAR_WIDTH)
  const share = total ? filled / total : 0
  const exact = STATE_BAR_KEYS.map((key) => counts[key] * share)
  const cells = exact.map(Math.floor)
  let spare = filled - cells.reduce((sum, n) => sum + n, 0)
  const byFraction = exact.map((value, i) => ({ i, fraction: value - cells[i] }))
    .sort((a, b) => b.fraction - a.fraction)
  for (let i = 0; spare > 0; i += 1, spare -= 1) cells[byFraction[i].i] += 1
  const bar = STATE_BAR_KEYS.map((key, i) => "█".repeat(cells[i])).join("")
    + "░".repeat(STATE_BAR_WIDTH - filled)
  const legend = STATE_BAR_KEYS.map((key) => `${STATE_BAR_LETTER[key]}${counts[key]}`).join(" ")
  return {
    content: `states ${bar} ${legend}`,
    fg: counts.blocked > 0 ? "error" : "base",
    counts, total, filled, bar, legend,
  }
}

export function clockOf(view = EMPTY_SNAPSHOT) {
  const stamps = [...view.active, ...view.recent].map((r) => timeOf(r.updatedAt)).filter(Boolean)
  const at = timeOf(view.generatedAt) || Math.max(0, ...stamps)
  if (!at) return "--:--:--"
  const date = new Date(at)
  return Number.isFinite(date.getTime()) ? date.toISOString().slice(11, 19) : "--:--:--"
}
export function snapshotLineItems(snapshot = EMPTY_SNAPSHOT, error = null, now = Date.now()) {
  const view = normalizeSnapshot(snapshot)
  const seen = new Set()
  const active = view.active.filter((r) => keep(r, seen))
  const recent = view.recent.filter((r) => keep(r, seen)).slice(0, RECENT_LIMIT)
  const c = metricCounts(view)
  const items = [{ marker: "", name: "Beacon", status: `A${c.active} B${c.blocked} R${c.rebase} F${c.fix} ${clockOf(view)}`, color: "action", bold: true }]
  if (error) items.push({ marker: "", name: `[stale] ${truncate(error, REASON_WIDTH)}`, status: "", color: "warning" })
  items.push(section("Active", active.length))
  if (!active.length) items.push({ marker: "", name: "No active runs", status: "", color: "muted" })
  else active.forEach((r) => items.push(...runLineItems(r, now)))
  items.push(section("Recent", recent.length))
  if (!recent.length) items.push({ marker: "", name: "No recent runs", status: "", color: "muted" })
  else recent.forEach((r) => items.push(...runLineItems(r, now, true)))
  return items
}
export function renderSnapshotLines(snapshot, error, now) {
  return snapshotLineItems(snapshot, error, now).map((i) => [i.marker, i.name, i.status].filter((x) => x !== "" && x != null).join(" "))
}
export function metricsLine(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const c = metricCounts(view)
  return { marker: "", name: `Beacon active ${c.active} · blocked ${c.blocked} · rebase ${c.rebase} · fix ${c.fix} · updated ${clockOf(view)}`, status: "", color: "muted" }
}
export function detailLines(run, metrics, now = Date.now()) {
  if (!run) return [{ text: "No run selected", color: "muted" }]
  const v = stateView(run.state)
  const m = metrics ?? {}
  const d = run.durations ?? {}
  const field = (name, value, color = "muted", statusColor = "base") => ({
    marker: "", name, status: truncate(value, VALUE_WIDTH), color, statusColor,
  })
  const note = (text, color = "muted") => ({ marker: "", name: text, status: "", color })
  const out = []
  out.push(section("Identity"), field("State", `${v.marker} ${run.state} · ${v.label}`, v.color),
    field("Run", run.id || "-"), field("Task", run.taskID || "-"),
    field("Title", run.title || "-", "base"), field("PR", run.pr ? `#${run.pr}` : "-"),
    field("Branch", run.branch || "-"), field("SHA", run.sha ? truncate(run.sha, 12) : "-"))
  out.push({ marker: "", name: RULE, status: "", color: "muted" })
  const stamp = (value) => { const t = str(value); return t.length >= 19 ? t.slice(11, 19) : t || "?" }
  out.push(section("Timing"), field("Created", stamp(run.createdAt)), field("Updated", stamp(run.updatedAt)),
    field("Age", ageOf(run, now)), field("Wait CI", elapsed(d.waitingCI * 1000)),
    field("Running", elapsed(d.running * 1000)), field("Blocked", elapsed(d.blocked * 1000)))
  out.push({ marker: "", name: RULE, status: "", color: "muted" })
  out.push(section("Attempts"), field("Rebase", `${run.rebaseAttempts}/${run.maxAttempts || "?"}`),
    field("Fix", `${run.fixAttempts}/${run.maxAttempts || "?"}`), field("Dispatch", m.dispatchCount ?? 0),
    field("Rebases", m.rebaseCount ?? 0), field("Merges", m.mergeCount ?? 0),
    field("CI wait", elapsed((m.ciWaitSeconds ?? 0) * 1000)))
  out.push({ marker: "", name: RULE, status: "", color: "muted" })
  out.push(section("Reason"), note(run.reason || "-", run.state === "blocked" ? "error" : "base"))
  out.push({ marker: "", name: RULE, status: "", color: "muted" })
  out.push(section("Timeline"))
  const timeline = [...(run.timeline ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
  timeline.length ? timeline.forEach((e) => out.push(note(`${stamp(e.at)} ${e.kind || "event"}${e.state ? ` → ${e.state}` : ""}`)))
    : out.push(note("(none)"))
  out.push({ marker: "", name: RULE, status: "", color: "muted" })
  out.push(section("Decisions"))
  const allDecisions = [...(run.decisions ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
  const decisions = allDecisions.slice(-DECISION_LIMIT)
  const earlier = allDecisions.length - decisions.length
  if (earlier > 0) out.push(note(`... ${earlier} earlier decisions`))
  decisions.length ? decisions.forEach((d) => out.push(note(`${stamp(d.at)} ${d.kind || "decision"} ${d.verdict || d.action || ""}`.trimEnd())))
    : out.push(note("(none)"))
  return out
}
export function detailCommand(open) {
  return {
    id: "beacon.run.detail", title: "Beacon run detail", description: "Open the selected Beacon run",
    group: "Beacon", bind: "ctrl+g", palette: true, slash: { name: "beacon-detail" },
    run: () => open(PANEL_NAME),
  }
}
