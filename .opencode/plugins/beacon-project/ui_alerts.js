// Presentation helpers for daemon and incident alerts, the queue, compact
// reasons, CI checks and human timeline copy. This module keeps ui_view.js
// under the 350-line limit.
import { failedCheckCount, REASON_WIDTH, str } from "./ui_model.js"

const ALERT_STATES = Object.freeze(["dead", "stale"])
const TIMELINE_TEXT = Object.freeze({
  created: "Run created", admitted: "Run created, task admitted",
  admitted_task: "Run created, task admitted", spawn: "Worker started",
  spawned: "Worker started", session: "Worker session started",
  session_started: "Worker session started", worker_started: "Worker started",
  watch_pr: "PR opened", pr_opened: "PR opened", pr: "PR opened",
  checks_pending: "CI checks pending", ci_pending: "CI checks pending",
  rebase: "Rebased onto main", rebased: "Rebased onto main",
  merge: "Squash merged to main", merged: "Squash merged to main",
  delivery_claim: "Squash merged to main", cancel: "Run closed", closed: "Run closed",
})
const STATE_NAMES = Object.freeze({
  waiting_ci: "waiting CI", needs_rebase: "needs rebase", needs_agent_fix: "needs agent fix",
})

export function timelineText(event = {}) {
  const kind = str(event.kind).toLowerCase()
  const base = TIMELINE_TEXT[kind]
    || str(event.kind || "event").replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase())
  const text = event.pr && /PR opened/.test(base) ? base.replace("PR opened", `PR #${event.pr} opened`) : base
  return kind === "checks_pending" && event.state
    ? `${text} · ${STATE_NAMES[str(event.state).toLowerCase()] || str(event.state)}` : text
}

export function daemonAlertItem(daemon) {
  if (!daemon || typeof daemon !== "object") return null
  const state = str(daemon.state).toLowerCase()
  const error = str(daemon.error)
  if (!ALERT_STATES.includes(state) && !error) return null
  return {
    marker: "",
    name: `⚠ Daemon ${str(daemon.state) || "unknown"}: ${error}`,
    status: "", color: state === "dead" ? "error" : "warning",
    bold: true, banner: true,
  }
}

export function baseIncidentItem(incident) {
  if (!incident || typeof incident !== "object") return null
  const id = str(incident.id)
  const baseBranch = str(incident.baseBranch)
  const fixerRunID = str(incident.fixerRunID)
  const failed = failedCheckCount(incident.failedChecks)
  const hasFailed = typeof failed === "number" ? failed > 0 : Boolean(failed) && failed !== "?"
  // Go sends a stable zero-value incident when nothing is open. It must not
  // fabricate a "main RED" banner from that empty object.
  if (!id && !baseBranch && !fixerRunID && !hasFailed) return null
  return {
    marker: "",
    name: `🔥 ${baseBranch || "main"} RED (${failed}) · Fixer`,
    status: "", color: "error", bold: true, banner: true,
    runID: fixerRunID || undefined,
  }
}

export function queueLineItems(queue = []) {
  return (Array.isArray(queue) ? queue : []).map((item) => {
    const waiting = str(item?.status).toLowerCase() === "waiting_deps"
    const deps = (Array.isArray(item?.dependsOn) ? item.dependsOn : []).map(str).filter(Boolean).join(", ")
    const cardID = str(item?.id)
    return {
      marker: waiting ? "◌" : "○",
      name: str(item?.title) || cardID || "-",
      status: `${waiting ? `blocked by ${deps || "deps"}` : "ready"} · P${str(item?.priority) || "?"}`,
      color: "muted",
      cardID,
      action: cardID ? `card:${cardID}` : undefined,
    }
  })
}

export const readyCount = (queue = []) => (Array.isArray(queue) ? queue : [])
  .filter((item) => str(item?.status).toLowerCase() === "ready").length

export function dispatchItem(ready = 0) {
  return ready > 0
    ? { marker: "▶", name: `Dispatch (${ready} ready)`, status: "", color: "success", bold: true, action: "dispatch" }
    : { marker: "", name: "No tasks ready", status: "", color: "muted" }
}

const NEXT_STEPS = Object.freeze({
  pending: "Queued: waiting for dispatch",
  running: "Running: nudge the worker with guidance",
  waiting_ci: "Waiting CI: approve when checks pass",
  needs_rebase: "Rebase needed: nudge the worker",
  needs_agent_fix: "Fix needed: nudge the worker",
  blocked: "Blocked: retry or nudge worker",
  closed: "Closed: retry to resume",
  merged: "",
})

export const nextStepLine = (run) => run ? (NEXT_STEPS[str(run.state).toLowerCase()] ?? "") : ""

// The host truncates from the middle, so clipped text is cut here from the end.
// No ellipsis ever appears in the middle of a rendered string.
export function truncate(value, width) {
  const text = str(value)
  return text.length <= width ? text : width <= 1 ? text.slice(0, width) : `${text.slice(0, width - 1)}…`
}

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

export function clockDuration(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60 ? `${s % 60}s` : ""}`
  return `${Math.floor(m / 60)}h${m % 60 ? `${m % 60}m` : ""}`
}

const CHECK_STATE = Object.freeze({
  failure: "failed", failed: "failed", error: "failed",
  pending: "pending", in_progress: "pending", queued: "pending",
  skipped: "skipped", neutral: "skipped",
})
export function checkRow(check) {
  const state = str(check?.state).toLowerCase()
  const failed = state === "failure" || state === "failed" || state === "error"
  const short = CHECK_STATE[state] ?? ""
  const name = str(check?.name) || "check"
  return {
    marker: failed ? "✖" : state === "success" ? "✔" : "◐",
    markerSpace: true,
    name: short && state !== "success" ? `${name} ${short}` : name,
    status: check?.durationSeconds ? clockDuration(check.durationSeconds) : "",
    statusColor: "muted",
    color: failed ? "error" : "muted",
    kind: "check",
  }
}

export function checksSummary(checks = []) {
  const list = Array.isArray(checks) ? checks : []
  if (!list.length) return ""
  const passed = list.filter((c) => str(c?.state).toLowerCase() === "success").length
  const failed = list.length - passed
  const total = list.reduce((n, c) => n + (Number(c?.durationSeconds) || 0), 0)
  const duration = total > 0 ? ` · ${clockDuration(total)}` : ""
  return failed ? `${failed} failed · ${passed}/${list.length}` : `${passed}/${list.length}${duration}`
}
