// Presentation helpers for daemon and incident alerts, the task queue and
// human timeline copy. This module keeps ui_view.js under the 350-line limit.
import { failedCheckCount, str } from "./ui_model.js"

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
  return {
    marker: "",
    name: `🔥 ${str(incident.baseBranch) || "main"} RED (${failedCheckCount(incident.failedChecks)}) · Fixer`,
    status: "", color: "error", bold: true, banner: true,
    runID: str(incident.fixerRunID) || undefined,
  }
}

export function queueLineItems(queue = []) {
  return (Array.isArray(queue) ? queue : []).map((item) => {
    const waiting = str(item?.status).toLowerCase() === "waiting_deps"
    const deps = (Array.isArray(item?.dependsOn) ? item.dependsOn : []).map(str).filter(Boolean).join(", ")
    return {
      marker: waiting ? "◌" : "○",
      name: str(item?.title) || str(item?.id) || "-",
      status: `${waiting ? `blocked by ${deps || "deps"}` : "ready"} · P${str(item?.priority) || "?"}`,
      color: "muted",
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
