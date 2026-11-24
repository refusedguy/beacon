// Presentation helpers for the Beacon status panel. Kept apart from the data
// model so both files stay small and easy to test.
import {
  DECISION_LIMIT, EMPTY_SNAPSHOT, PANEL_NAME, RECENT_LIMIT, REASON_WIDTH, TITLE_WIDTH,
  normalizeSnapshot, str,
} from "./ui_model.js"

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
const section = (label, count) => ({ marker: "▼", name: label, status: String(count), color: "action", bold: true })
export function snapshotLineItems(snapshot = EMPTY_SNAPSHOT, error = null, now = Date.now()) {
  const view = normalizeSnapshot(snapshot)
  const seen = new Set()
  const active = view.active.filter((r) => keep(r, seen))
  const recent = view.recent.filter((r) => keep(r, seen)).slice(0, RECENT_LIMIT)
  const items = [{ marker: "", name: "Beacon", status: "", color: "action", bold: true }]
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
  const m = view.metrics ?? {}
  const blocked = m.blockedCount ?? view.active.filter((r) => r.state === "blocked").length
  const stamps = [...view.active, ...view.recent].map((r) => timeOf(r.updatedAt)).filter(Boolean)
  const at = timeOf(view.generatedAt) || Math.max(0, ...stamps)
  const date = at ? new Date(at) : null
  const clock = date && Number.isFinite(date.getTime()) ? date.toISOString().slice(11, 19) : "--:--:--"
  const name = `Beacon active ${view.active.length} · blocked ${blocked} · rebase ${m.rebaseCount ?? 0} · fix ${m.fixCount ?? 0} · updated ${clock}`
  return { marker: "", name, status: "", color: "muted" }
}
export function detailLines(run, metrics, now = Date.now()) {
  if (!run) return [{ text: "No run selected", color: "muted" }]
  const v = stateView(run.state)
  const line = (text, color = "base", bold = false) => ({ text, color, bold })
  const stamp = (value) => { const t = str(value); return t.length >= 19 ? t.slice(11, 19) : t || "?" }
  const d = run.durations ?? {}
  const m = metrics ?? {}
  const out = [
    line("Run detail", "base", true),
    line(`Metrics dispatch ${m.dispatchCount ?? 0} · rebase ${m.rebaseCount ?? 0} · fix ${m.fixCount ?? 0} · merge ${m.mergeCount ?? 0} · ci_wait ${elapsed((m.ciWaitSeconds ?? 0) * 1000)}`, "muted"),
    line(`ID ${run.id || "-"} · task ${run.taskID || "-"}`, "base", true),
    line(`${v.marker} ${run.title || run.taskID || "-"}`, v.color, true),
    line(`State ${run.state} · PR ${run.pr ? `#${run.pr}` : "-"} · ${v.label}`),
    line(`Branch ${run.branch || "-"} · SHA ${run.sha ? truncate(run.sha, 12) : "-"}`, "muted"),
    line(`Created ${stamp(run.createdAt)} · Updated ${stamp(run.updatedAt)} · ${ageOf(run, now)}`, "muted"),
    line(`Durations wait_ci ${elapsed(d.waitingCI * 1000)} · running ${elapsed(d.running * 1000)} · blocked ${elapsed(d.blocked * 1000)}`, "muted"),
    line(`Attempts ${run.rebaseAttempts}/${run.maxAttempts || "?"} rebase · ${run.fixAttempts}/${run.maxAttempts || "?"} fix`, "muted"),
    line(`Reason ${run.reason || "-"}`, run.state === "blocked" ? "error" : "base"),
    line("Timeline", "base", true),
  ]
  const timeline = [...(run.timeline ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
  timeline.length ? timeline.forEach((e) => out.push(line(`  ${stamp(e.at)} ${e.kind || "event"}${e.state ? ` → ${e.state}` : ""}`, "muted"))) : out.push(line("  (none)", "muted"))
  out.push(line("Decisions", "base", true))
  const allDecisions = [...(run.decisions ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
  const decisions = allDecisions.slice(-DECISION_LIMIT)
  const earlier = allDecisions.length - decisions.length
  if (earlier > 0) out.push(line(`  ... ${earlier} earlier decisions`, "muted"))
  decisions.length ? decisions.forEach((d) => out.push(line(`  ${stamp(d.at)} ${d.kind || "decision"} ${d.verdict || d.action || ""}`.trimEnd(), "muted"))) : out.push(line("  (none)", "muted"))
  return out
}
export function detailCommand(open) {
  return {
    id: "beacon.run.detail", title: "Beacon run detail", description: "Open the selected Beacon run",
    group: "Beacon", bind: "ctrl+g", palette: true, slash: { name: "beacon-detail" },
    run: () => open(PANEL_NAME),
  }
}


