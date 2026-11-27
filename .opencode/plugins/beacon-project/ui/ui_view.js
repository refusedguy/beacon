// Pure view helpers for the Beacon panels. No TUI imports live here so these
// functions stay testable with plain Node and shared by every slot.
import {
  DECISION_LIMIT, EMPTY_SNAPSHOT, PANEL_NAME, REASON_WIDTH, RECENT_LIMIT,
  normalizeSnapshot, str,
} from "./ui_model.js"
import {
  baseIncidentItem, blockedGroupKeys, checkRow, checksSummary, compactReason, daemonAlertItem,
  dispatchItem, isDispatching, nextStepLine, queueRowsWithMeta, readyCount, shortTaskID, spacerItem, taskTree, timelineText, truncate,
} from "./ui_alerts.js"
import { clockTime, elapsed, timeOf } from "./time_format.js"
export { elapsed }

export const STATE_BAR_WIDTH = 30
// A solid block stays visible in every host font. Every filled segment reads
// this constant, so the bar needs no other change when the host font differs.
export const STATE_BAR_FILLED = "█"
export const STATE_BAR_EMPTY = "─"
export const STATE_BAR_KEYS = Object.freeze([
  "pending", "running", "waiting_ci", "needs_rebase", "needs_agent_fix", "blocked", "merged", "closed",
])
// Finished work gets the accent tone so RUN and MERGED never share a color.
const MERGED_COLOR = "action"
const STATE_BAR_COLOR = Object.freeze({
  pending: "muted", running: "success", waiting_ci: "info", needs_rebase: "warning",
  needs_agent_fix: "warning", blocked: "error", merged: MERGED_COLOR, closed: "muted",
})
const STATE_BAR_LETTER = Object.freeze({
  pending: "P", running: "R", waiting_ci: "C", needs_rebase: "E",
  needs_agent_fix: "F", blocked: "B", merged: "M", closed: "X",
})

const STATE_VIEW = Object.freeze({
  pending: { marker: "○", color: "muted", label: "PEND" },
  running: { marker: "●", color: "success", label: "RUN" },
  waiting_ci: { marker: "◐", color: "info", label: "CI" },
  needs_rebase: { marker: "◆", color: "warning", label: "REBASE" },
  needs_agent_fix: { marker: "◆", color: "warning", label: "FIX" },
  blocked: { marker: "▲", color: "error", label: "BLK" },
  merged: { marker: "✔", color: MERGED_COLOR, label: "MERGED" },
  closed: { marker: "✖", color: "muted", label: "CLOSED" },
})
const stateView = (state) => STATE_VIEW[str(state).toLowerCase()] ?? { marker: "·", color: "muted", label: "?" }

// Recent rows use three calm verdicts instead of the full active state names.
function recentBadge(run) {
  const state = str(run?.state).toLowerCase()
  const reason = str(run?.reason)
  if (/already merged|duplicate/i.test(reason)) return "DUP"
  if (state === "merged") return "OK"
  if (state === "closed" || state === "blocked" || state === "needs_agent_fix") return "ERR"
  return "OK"
}

export const SIDEBAR_OPEN = Object.freeze({ active: true, queue: false, recent: true })
export const DETAIL_OPEN = Object.freeze({ why: true, checks: true, timeline: true, technical: false })
export const ACTION_NAMES = Object.freeze(["retry", "approve", "cancel", "pr", "help", "copy"])

// The host truncates from the middle, so sidebar names are clipped here from
// the end with an explicit ellipsis. Detail text keeps its full width.
export const SIDEBAR_TITLE_WIDTH = 29
export const truncateSidebarTitle = (value) => truncate(value, SIDEBAR_TITLE_WIDTH)
const ageOf = (run, now) => { const t = Math.max(timeOf(run?.updatedAt), timeOf(run?.createdAt)); return t ? elapsed(now - t) : "" }
const tookOf = (run) => { const a = timeOf(run?.createdAt), b = timeOf(run?.updatedAt); return a && b ? elapsed(b - a) : "" }
const numberOrZero = (v) => { const n = Number(v); return Number.isFinite(n) && n >= 0 ? n : 0 }
export const shortSHA = (value) => { const sha = str(value); return sha ? sha.slice(0, 7) : "-" }
const lastChecksPending = (timeline) => [...timeline].reverse().find((e) => str(e?.kind).toLowerCase() === "checks_pending")

// A rebase or fix transition starts a new attempt. The cumulative buckets keep
// counting from the first start, so the live phase uses the cycle buckets that
// measure only the current round.
export function phaseDurations(run, now = Date.now()) {
  const d = run.durations ?? {}
  const cumulative = {
    waitingCI: numberOrZero(d.waitingCI), running: numberOrZero(d.running), blocked: numberOrZero(d.blocked),
  }
  const attempt = run.attempt ?? {}
  if (attempt.kind !== "rebase" && attempt.kind !== "fix") return { ...cumulative, scoped: false }
  const waitingCI = attempt.kind === "rebase" ? numberOrZero(d.waitingCIRebase) : numberOrZero(d.waitingCIFix)
  const running = attempt.startedAt && str(run?.state).toLowerCase() === "running"
    ? Math.max(0, now - timeOf(attempt.startedAt)) / 1000 : cumulative.running
  return { waitingCI, running, blocked: cumulative.blocked, scoped: true, cumulative }
}

// Checks can outlive the head they measured. After a rebase the run carries a
// new head before the next observation replaces the old green checks.
export function checksForRun(run) {
  const all = run.checks ?? []
  const head = str(run?.sha)
  if (!head) return { checks: all, stale: [] }
  const observed = str(lastChecksPending(run.timeline ?? [])?.sha)
  const checks = [], stale = []
  for (const check of all) {
    const checkHead = str(check?.headSHA)
    const current = checkHead ? checkHead === head : !observed || observed === head
    ;(current ? checks : stale).push(check)
  }
  return { checks, stale }
}

export function phaseLabel(run, now = Date.now()) {
  const d = phaseDurations(run, now)
  const state = str(run.state).toLowerCase()
  const phase = state === "waiting_ci" ? ["CI wait", d.waitingCI]
    : state === "blocked" ? ["block", d.blocked]
      : state === "running" ? ["run", d.running] : null
  if (phase && Number(phase[1]) > 0) return `${phase[0]} ${elapsed(Number(phase[1]) * 1000)}`
  return ageOf(run, now)
}
export const mergedReason = (run) => str(run?.reason).trim() || (run?.pr ? `merged PR #${run.pr}` : "merged to main")

const BADGE_LABEL = Object.freeze({
  pending: "PEND", running: "RUN", waiting_ci: "CI", needs_rebase: "REBASE",
  needs_agent_fix: "FIX", blocked: "BLOCKED", merged: "MERGED", closed: "CLOSED",
})
function heroBadge(run, now = Date.now()) {
  const v = stateView(run.state)
  const label = BADGE_LABEL[str(run.state).toLowerCase()] || v.label
  const duration = phaseLabel(run, now)
  return `${v.marker} ${label}${duration ? ` · ${duration}` : ""}`
}

// The helpers below take an already-normalized view. The public entry points
// normalize once at the boundary and pass the view down, so a composed call
// no longer normalizes the same snapshot five or more times.
function activeFromView(view) {
  const seen = new Set()
  return view.active.filter((run) => {
    if (!run.id || seen.has(run.id)) return false
    seen.add(run.id)
    return true
  })
}
function recentFromViewWith(view, active) {
  const activeTasks = new Set(active.map((r) => str(r.taskID)).filter(Boolean))
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
function recentFromView(view) { return recentFromViewWith(view, activeFromView(view)) }
function stateBarFromView(view, active = activeFromView(view), recent = recentFromView(view)) {
  // A prepared literal with the same keys as STATE_BAR_KEYS avoids building an
  // entries array and a combined run array on every refresh. Walk the two
  // views in place instead of spreading them into a third array.
  const counts = {
    pending: 0, running: 0, waiting_ci: 0, needs_rebase: 0,
    needs_agent_fix: 0, blocked: 0, merged: 0, closed: 0,
  }
  for (const run of active) {
    const state = str(run.state).toLowerCase()
    if (Object.hasOwn(counts, state)) counts[state] += 1
  }
  for (const run of recent) {
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
  const segments = STATE_BAR_KEYS.map((key, i) => ({ text: STATE_BAR_FILLED.repeat(cells[i]), color: STATE_BAR_COLOR[key] }))
  segments.push({ text: STATE_BAR_EMPTY.repeat(STATE_BAR_WIDTH - filled), color: "muted" })
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
// One normalized view and state bar per snapshot object. The sidebar pass
// builds them and the footer pass takes the same bar, so a refresh normalizes
// and counts the runs once instead of once per slot. The queue array held here
// is also the stable key that lets the taskTree WeakMap hit across the passes
// of one refresh and across repeated renders of the same snapshot.
const viewCache = new WeakMap()
let lastRaw = null
let lastEntry = null
function buildView(snapshot) {
  const view = normalizeSnapshot(snapshot)
  const active = activeFromView(view)
  const recent = recentFromViewWith(view, active)
  return { view, active, recent, bar: stateBarFromView(view, active, recent) }
}
function viewFor(snapshot) {
  if (!snapshot || typeof snapshot !== "object") return buildView(snapshot)
  const cached = viewCache.get(snapshot)
  if (cached) return cached
  // A fresh wrapper around the same payload is common on every poll. Reuse the
  // prior view when every field the sidebar reads is still the same object.
  if (lastRaw && lastEntry && lastRaw.queue === snapshot.queue
    && lastRaw.active === snapshot.active && lastRaw.recent === snapshot.recent
    && lastRaw.metrics === snapshot.metrics && lastRaw.daemon === snapshot.daemon
    && lastRaw.baseIncident === snapshot.baseIncident && lastRaw.generatedAt === snapshot.generatedAt) {
    viewCache.set(snapshot, lastEntry)
    return lastEntry
  }
  const entry = buildView(snapshot)
  viewCache.set(snapshot, entry)
  lastRaw = snapshot
  lastEntry = entry
  return entry
}
export function stateBarItem(snapshot = EMPTY_SNAPSHOT) {
  return viewFor(snapshot).bar
}

// Two pooled slots per run: identity, then reference and live cause.
export function runLineItems(run, now = Date.now(), recent = false) {
  const v = stateView(run.state)
  const state = str(run.state).toLowerCase()
  const title = str(run.title) || shortTaskID(run.taskID)
  const ref = run.pr ? `#${run.pr}` : shortTaskID(run.taskID)
  const duration = recent ? tookOf(run) : phaseLabel(run, now)
  const reason = state === "merged" ? "" : compactReason(run.reason)
  const errorReason = state === "blocked" || state === "closed" || state === "needs_agent_fix"
  const secondLine = { marker: ref, markerSpace: true, name: duration, status: reason,
    color: "muted", statusColor: errorReason ? "error" : "muted", runID: run.id }
  if (recent) {
    const badge = recentBadge(run)
    const tone = badge === "DUP" ? "muted" : badge === "ERR" ? "error"
      : state === "merged" ? MERGED_COLOR : "success"
    return [
      { marker: badge === "OK" ? "✔" : "✖", markerSpace: true, name: title, status: badge,
        color: tone, markerColor: tone, nameColor: "muted", nameBold: false, statusColor: tone,
        recent: true, runID: run.id },
      { ...secondLine, markerSpace: 2, recent: true, markerColor: "muted" },
    ]
  }
  return [
    { marker: v.marker, markerSpace: true, name: title, status: v.label, color: v.color, bold: true, runID: run.id },
    secondLine,
  ]
}
const sectionItem = (label, count, key, open) => ({
  marker: open ? "▼" : "▶", name: label, status: String(count),
  color: "action", bold: true, statusColor: "muted", sectionKey: key,
})

// Expanded epic IDs in storage belong to an earlier session. Discard them at
// startup so every epic starts collapsed; a click in this session adds an ID.
export function discardStoredEpicOpen(epic) {
  for (const id of Object.keys(epic.open ?? {})) delete epic.open[id]
  return epic
}

const BLOCKED_PREFIX = "blocked:"
const blockedStateKey = (depID) => `${BLOCKED_PREFIX}${depID}`

// Blocked groups live in the same persisted map as epics, under a prefixed key
// so a blocker that is also a card cannot collide with an epic toggle. The
// startup discard above clears both, so every group begins collapsed.
export function blockedOpenFrom(epicOpen) {
  const out = Object.create(null)
  for (const [key, value] of Object.entries(epicOpen ?? {})) {
    if (key.startsWith(BLOCKED_PREFIX) && value === true) {
      out[key.slice(BLOCKED_PREFIX.length)] = true
    }
  }
  return out
}

export function blockedToggleItem(queue = [], blockedOpen = null, tree = null, keys = null) {
  // A caller that already grouped the queue passes the keys from that pass.
  // A standalone caller falls back to the shared index.
  const blocked = keys ?? blockedGroupKeys(queue, tree)
  if (!blocked.length) return null
  const expanded = blocked.every((key) => blockedOpen?.[key] === true)
  return {
    marker: expanded ? "▼" : "▶",
    name: expanded ? "Collapse all blocked" : "Expand all blocked",
    status: "", color: "action", nameColor: "action",
    kind: "queue-blocked-toggle", action: "blocked-toggle",
  }
}

export function toggleBlockedGroup(epic, depID) {
  if (depID) epic.toggle(blockedStateKey(depID))
}

export function toggleAllBlockedGroups(epic, queue = []) {
  const keys = blockedGroupKeys(queue)
  const expanded = keys.length > 0 && keys.every((key) => epic.open[blockedStateKey(key)] === true)
  const target = !expanded
  for (const key of keys) {
    const storageKey = blockedStateKey(key)
    if ((epic.open[storageKey] === true) !== target) epic.toggle(storageKey)
  }
  return keys
}

export function sidebarModel(snapshot = EMPTY_SNAPSHOT, error = null, now = Date.now(), open = SIDEBAR_OPEN, epicOpen = null, blockedOpen = null, dispatching = isDispatching()) {
  const { view, active, recent, bar } = viewFor(snapshot)
  const items = [
    { marker: "", name: "Beacon", status: bar.summary, color: "action", bold: true },
    { marker: "", name: bar.legend, status: "", color: "muted" },
  ]
  if (error) items.push({ marker: "", name: `[stale] ${truncate(error, REASON_WIDTH)}`, status: "", color: "warning" })
  const listItems = []
  for (const alert of [daemonAlertItem(view.daemon), baseIncidentItem(view.baseIncident)]) {
    if (alert) listItems.push(alert)
  }
  const ready = readyCount(view.queue)
  const queueEmpty = view.queue.length === 0
  // The queue header and every queue row read the same index. Build it once
  // from the normalized snapshot instead of once per consumer.
  const tree = open.queue === true && !queueEmpty ? taskTree(view.queue) : null
  listItems.push(sectionItem("Active", active.length, "active", open.active !== false))
  if (open.active !== false) {
    if (!active.length) listItems.push({ marker: "", name: "No active runs", status: "", color: "muted" })
    active.forEach((run) => listItems.push(...runLineItems(run, now)))
    if (dispatching || ready > 0) listItems.push(dispatchItem(ready, dispatching))
  }
  if (queueEmpty) {
    // A counted, expandable "Queue 0" next to a "No tasks ready" row says the
    // same thing twice. One flat header carries the whole empty-queue fact.
    listItems.push({
      marker: "", name: "Queue", status: "no tasks ready",
      color: "action", bold: true, statusColor: "muted",
    })
  } else {
    listItems.push(sectionItem("Queue", view.queue.length, "queue", open.queue === true))
    if (open.queue === true) {
      // One grouping serves the rows and the blocked toggle. Passing its keys
      // into the toggle removes the second queueGroups scan of the queue.
      const { rows, blockedKeys } = queueRowsWithMeta(view.queue, epicOpen, blockedOpen, tree)
      const toggle = blockedOpen != null ? blockedToggleItem(view.queue, blockedOpen, tree, blockedKeys) : null
      if (toggle) listItems.push(toggle)
      listItems.push(...rows)
      listItems.push(spacerItem())
    }
  }
  listItems.push(sectionItem("Recent", recent.length, "recent", open.recent !== false))
  if (open.recent !== false) {
    if (!recent.length) listItems.push({ marker: "", name: "No recent runs", status: "", color: "muted" })
    recent.forEach((run) => listItems.push(...runLineItems(run, now, true)))
  }
  return { ...bar, items: [...items, ...listItems], listItems, active, recent, view, queue: view.queue, ready }
}
export const snapshotLineItems = (snapshot, error, now) => sidebarModel(snapshot, error, now).items
export const renderSnapshotLines = (snapshot, error, now) => snapshotLineItems(snapshot, error, now)
  .map((i) => [i.marker, i.name, i.status].filter((x) => x !== "" && x != null).join(" "))
const launchOf = (run) => str(run?.launchSource ?? run?.launch_source).toLowerCase()
export const launchSourceText = (metrics = {}) => {
  const raw = metrics?.launch_sources ?? metrics?.launchSources
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return ""
  return Object.entries(raw).filter(([, n]) => Number.isFinite(Number(n)) && Number(n) > 0)
    .map(([k, n]) => `${str(k).toLowerCase()}:${Number(n)}`).join(" ")
}
export const metricsLine = (snapshot = EMPTY_SNAPSHOT) => {
  const item = stateBarItem(snapshot)
  const launch = launchSourceText(snapshot?.metrics)
  return { marker: "", name: `Beacon ${item.summary}`, status: [item.legend, launch].filter(Boolean).join(" · "), color: "muted" }
}

const stamp = (value) => clockTime(value) || "?"
export function detailActions(run) {
  if (!run) return []
  const state = str(run.state).toLowerCase()
  const active = ["pending", "running", "waiting_ci", "needs_rebase", "needs_agent_fix"].includes(state)
  const actions = []
  if (state === "blocked" || state === "closed") actions.push("retry")
  if (state === "waiting_ci" || (run.pr && active)) actions.push("approve")
  if (active) actions.push("cancel")
  if (run.prURL) actions.push("pr")
  if (active) actions.push("help")
  actions.push("copy")
  return actions
}
function phaseSummary(run, now) {
  const d = phaseDurations(run, now)
  return `Run ${shortTaskID(run.id)} · Coding ${elapsed(d.running * 1000)} · CI wait ${elapsed(d.waitingCI * 1000)} · Blocked ${elapsed(d.blocked * 1000)}`
}
export function detailModel(run, metrics = {}, now = Date.now(), open = DETAIL_OPEN) {
  if (!run) return {
    badge: "", pr: "", branch: "", title: "No run selected", task: "", phase: "",
    nextStep: "", fixer: "", actions: [], rows: [{ text: "No run selected", color: "muted" }],
  }
  const v = stateView(run.state)
  const state = str(run.state).toLowerCase()
  const rows = []
  const problem = state === "blocked" || state === "closed" || state === "needs_agent_fix"
  const isOpen = (key) => key === "why" && problem ? true
    : key === "technical" ? open.technical === true : open[key] !== false
  const header = (label, key, status = "") => rows.push({
    marker: isOpen(key) ? "▼" : "▶", name: label, status,
    color: "action", bold: true, statusColor: "muted", sectionKey: key, kind: "section",
  })
  const row = (item) => rows.push(item)
  const key = (label) => label.padEnd(10, " ")
  header("Why / Blocker", "why")
  if (isOpen("why")) {
    const reason = state === "merged" ? mergedReason(run) : str(run.reason).trim()
    row({ marker: "", name: reason || "No reason recorded", status: "", kind: "reason",
      color: problem ? "error" : "base" })
  }
  const { checks, stale } = checksForRun(run)
  header("CI Checks", "checks", stale.length ? `pending for ${shortSHA(run.sha)}` : checksSummary(checks, run.checks?.ciWallSeconds))
  if (isOpen("checks")) {
    if (stale.length) row({ marker: "", name: `Checks pending for head ${shortSHA(run.sha)}`, status: "", color: "warning", kind: "check" })
    if (!checks.length && !stale.length) row({ marker: "", name: "(none)", status: "", color: "muted", kind: "check" })
    else checks.forEach((c) => row(checkRow(c)))
  }
  header("Timeline", "timeline")
  if (isOpen("timeline")) {
    const timeline = [...(run.timeline ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
    if (!timeline.length) row({ marker: "", name: "(none)", status: "", color: "muted", kind: "timeline" })
    else timeline.forEach((e) => row({
      marker: key(stamp(e.at)), name: timelineText(e), status: "", color: "muted",
      markerColor: "muted", nameColor: "base", kind: "timeline",
    }))
  }
  header("Technical", "technical")
  if (isOpen("technical")) {
    row({ marker: key("Run ID"), name: run.id || "-", status: "", color: "base", kind: "tech" })
    row({ marker: key("Session"), name: run.sessionID || "-", status: "", color: "base", kind: "tech" })
    const launch = launchOf(run)
    if (launch) row({ marker: key("Launch"), name: [launch, str(run.agentIdentity ?? run.agent_identity)].filter(Boolean).join(" · "), status: "", color: "base", kind: "tech" })
    row({ marker: key("Head SHA"), name: run.sha || "-", status: "", color: "base", kind: "tech" })
    const attempt = run.attempt ?? {}
    const head = shortSHA(attempt.headSHA || run.sha)
    const attemptName = `#${attempt.number || 1}${attempt.kind && attempt.kind !== "initial" ? ` ${attempt.kind}` : ""}`
      + `${attempt.outcome ? ` · ${attempt.outcome}` : ""} · head ${head}`
    row({ marker: key("Attempts"), name: attemptName, status: "", color: "base", kind: "tech" })
    row({ marker: key("Rebase"), name: attempt.rebaseStatus || "none", status: "", color: "base", kind: "tech" })
    const counters = `Rebase ${run.rebaseAttempts ?? 0}/${run.maxAttempts || "?"} · Fix ${run.fixAttempts ?? 0}/${run.maxAttempts || "?"}`
    row({ marker: key("Counters"), name: counters, status: "", color: "muted", kind: "tech" })
    const totals = phaseDurations(run, now)
    if (totals.scoped) row({ marker: key("Total time"), name: `Coding ${elapsed(totals.cumulative.running * 1000)} · CI wait ${elapsed(totals.cumulative.waitingCI * 1000)} · Blocked ${elapsed(totals.cumulative.blocked * 1000)}`, status: "", color: "muted", kind: "tech" })
    const all = [...(run.decisions ?? [])].sort((a, b) => str(a.at).localeCompare(str(b.at)))
    const decisions = all.slice(-DECISION_LIMIT)
    if (all.length > decisions.length) row({ marker: "", name: `... ${all.length - decisions.length} earlier decisions`, status: "", color: "muted", kind: "decision" })
    if (!decisions.length) row({ marker: "", name: "(none)", status: "", color: "muted", kind: "decision" })
    else decisions.forEach((d) => row({
      marker: key(stamp(d.at)), name: `${d.kind || "decision"} ${d.verdict || d.action || ""}`.trimEnd(),
      status: "", color: "muted", kind: "decision",
    }))
  }
  return {
    badge: heroBadge(run, now),
    badgeColor: v.color,
    pr: run.pr ? `#${run.pr}` : "No PR",
    branch: run.branch || "-",
    title: str(run.title) || str(run.taskID) || "-",
    task: run.taskID || "-",
    phase: phaseSummary(run, now),
    nextStep: nextStepLine(run),
    fixer: run.isFixer ? "FIXER" : "",
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
