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

export function shortTaskID(value) {
  const id = str(value)
  // Cards are T-YYYYMMDD-<slug>[-NN]. Drop the date prefix and keep the
  // readable slug with its child suffix instead of cutting the ID in half.
  return id ? id.replace(/^T-\d{8}-/, "") : "-"
}

// A generated child carries its parent's ID as a prefix. Longest match wins
// when a nested ID happens to contain a shorter epic ID.
const queueParent = (list, id) => list
  .map((item) => str(item?.id))
  .filter((parent) => parent && parent !== id && id.startsWith(`${parent}-`))
  .sort((a, b) => b.length - a.length)[0] || ""

export const spacerItem = () => ({
  marker: "", name: "", status: "", color: "muted", kind: "spacer",
})
const countLabel = (n, one, many) => `${n} ${n === 1 ? one : many}`

// The snapshot carries only unresolved deps. Children of an epic already have
// their own rows, so they never become blocker headers.
const queueBlockers = (item, childIDs) => (Array.isArray(item?.dependsOn) ? item.dependsOn
  : Array.isArray(item?.depends_on) ? item.depends_on : [])
  .map(str).filter((dep) => dep && !childIDs.has(dep))

function taskRows(item, { epic, parent, blockers, children }) {
  const id = str(item?.id)
  const blocked = str(item?.status).toLowerCase() === "waiting_deps"
  const status = [
    `P${str(item?.priority) || "?"}`,
    blockers.length ? countLabel(blockers.length, "dep", "deps") : "",
  ].filter(Boolean).join(" · ")
  const rows = [{
    marker: epic ? "◆" : parent ? "└" : blocked ? "◌" : "○",
    markerSpace: parent ? 2 : true,
    name: str(item?.title) || id || "-",
    status, statusColor: "muted",
    color: "base", nameColor: "base", nameBold: epic, cardID: id, kind: "queue-title",
    action: id ? `card:${id}` : undefined,
  }]
  if (epic) rows.push({
    marker: "", name: `epic · ${countLabel(children, "child", "children")}`,
    status: "", color: "muted", statusColor: "muted", cardID: id, kind: "queue-status",
    action: id ? `card:${id}` : undefined,
  })
  return rows
}

// One group per unresolved blocker. A child with no dep of its own follows its
// epic's blocker, so an expanded epic keeps a single header. The rendered
// headers and the global Queue toggle share this grouping.
export function queueGroups(queue = [], hidden = null) {
  const list = Array.isArray(queue) ? queue : []
  const shown = hidden ? list.filter((item) => !hidden.has(str(item?.id))) : list
  const parents = new Map(list.map((item) => [str(item?.id), queueParent(list, str(item?.id))]))
  const parentsWithChildren = new Set(parents.values())
  const childIDs = new Set([...parents].filter(([, parent]) => parent).map(([id]) => id))
  const childCount = new Map()
  for (const parent of parents.values()) childCount.set(parent, (childCount.get(parent) ?? 0) + 1)
  const blockers = new Map(list.map((item) => [str(item?.id), queueBlockers(item, childIDs)]))
  const groupOf = (item) => {
    const id = str(item?.id)
    const own = blockers.get(id) ?? []
    return own[0] || (blockers.get(parents.get(id) || "") ?? [])[0] || ""
  }
  const groups = new Map()
  const order = []
  for (const item of shown) {
    const key = groupOf(item)
    if (!groups.has(key)) { groups.set(key, []); order.push(key) }
    groups.get(key).push(item)
  }
  return { groups, order, parents, parentsWithChildren, childCount, blockers }
}

export const blockedGroupKeys = (queue = []) => queueGroups(queue).order.filter(Boolean)

// An expanded epic or blocked group shows at most this many member rows. The
// rest collapse into a "+N more" line so Recent stays inside the sidebar.
export const QUEUE_CHILD_LIMIT = 3

// A null blockedOpen keeps the flat legacy rows for pure callers that never
// drive the toggle. With a map, only groups marked true expand.
export function queueLineItems(queue = [], hidden = null, blockedOpen = null) {
  const { groups, order, parents, parentsWithChildren, childCount, blockers } = queueGroups(queue, hidden)
  const rows = []
  for (const key of order) {
    if (rows.length) rows.push(spacerItem())
    const members = groups.get(key)
    if (key) {
      const expanded = blockedOpen == null || blockedOpen[key] === true
      rows.push({
        marker: expanded ? "▼" : "▶",
        name: `blocked by ${shortTaskID(key)} · ${countLabel(members.length, "task", "tasks")}`,
        status: "", color: "muted", kind: "queue-blocker", depID: key,
        blockedKey: key, action: `blocked:${key}`,
      })
      if (!expanded) continue
    }
    const capped = Boolean(key) && blockedOpen != null && members.length > QUEUE_CHILD_LIMIT
    for (const item of capped ? members.slice(0, QUEUE_CHILD_LIMIT) : members) {
      const id = str(item?.id)
      rows.push(...taskRows(item, {
        epic: str(item?.kind).toLowerCase() === "epic" || parentsWithChildren.has(id),
        parent: parents.get(id) || "", blockers: blockers.get(id) ?? [],
        children: childCount.get(id) ?? 0,
      }))
    }
    if (capped) rows.push({
      marker: "", name: `+${members.length - QUEUE_CHILD_LIMIT} more`, status: "",
      color: "muted", kind: "queue-more",
    })
  }
  return rows
}

export function queueRows(queue = [], epicOpen = null, blockedOpen = null) {
  const list = Array.isArray(queue) ? queue : []
  if (!epicOpen) return queueLineItems(list, null, blockedOpen)
  const ids = list.map((item) => str(item?.id))
  const parentOf = (id) => ids.filter((p) => p && p !== id && id.startsWith(`${p}-`))
    .sort((a, b) => b.length - a.length)[0] || ""
  const childrenOf = (id) => ids.filter((child) => parentOf(child) === id)
  const counts = new Map()
  for (const id of ids) {
    const parent = parentOf(id)
    if (parent) counts.set(parent, (counts.get(parent) ?? 0) + 1)
  }
  const isEpic = (id) => counts.has(id)
    || list.some((i) => str(i?.id) === id && str(i?.kind).toLowerCase() === "epic")
  // Walk up to the direct child under each ancestor epic. This hides the whole
  // subtree of a collapsed epic and of a child beyond the cap.
  const childUnder = (id, ancestor) => {
    let current = id
    let parent = parentOf(current)
    while (parent && parent !== ancestor) {
      current = parent
      parent = parentOf(current)
    }
    return parent === ancestor ? current : ""
  }
  const hidden = new Set()
  for (const id of ids) {
    let parent = parentOf(id)
    while (parent) {
      if (isEpic(parent)) {
        if (epicOpen[parent] !== true) { hidden.add(id); break }
        if (childrenOf(parent).indexOf(childUnder(id, parent)) >= QUEUE_CHILD_LIMIT) { hidden.add(id); break }
      }
      parent = parentOf(parent)
    }
  }
  const rows = queueLineItems(list, hidden, blockedOpen)
  const summaries = []
  for (const id of ids) {
    if (!isEpic(id) || epicOpen[id] !== true) continue
    const children = childrenOf(id)
    if (children.length <= QUEUE_CHILD_LIMIT) continue
    const last = children[QUEUE_CHILD_LIMIT - 1]
    let at = -1
    for (let i = rows.length - 1; i >= 0; i -= 1) {
      if (rows[i].cardID === last) { at = i + 1; break }
    }
    if (at > 0) summaries.push({ at, item: {
      marker: "", name: `+${children.length - QUEUE_CHILD_LIMIT} more`, status: "",
      color: "muted", kind: "queue-more",
    } })
  }
  for (const { at, item } of summaries.sort((a, b) => b.at - a.at)) rows.splice(at, 0, item)
  return rows.map((row) => {
    if (row.kind !== "queue-title" || !isEpic(row.cardID)) return row
    const chevron = epicOpen[row.cardID] === true ? "▼" : "▶"
    return { ...row, marker: chevron, epicKey: row.cardID, action: `epic:${row.cardID}` }
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
  const chars = Array.from(str(value))
  if (chars.length <= width) return chars.join("")
  if (width <= 0) return ""
  if (width === 1) return "…"
  return `${chars.slice(0, width - 1).join("")}…`
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

const CHECK_BUCKETS = Object.freeze(Object.assign(Object.create(null), {
  failure: "failed", failed: "failed", error: "failed",
  skipped: "skipped", neutral: "skipped",
  pending: "pending", in_progress: "pending", queued: "pending",
}))
export function checksSummary(checks = [], ciWallSeconds) {
  const list = Array.isArray(checks) ? checks : []
  if (!list.length) return ""
  const buckets = { success: 0, failed: 0, skipped: 0, pending: 0 }
  for (const check of list) {
    const state = str(check?.state).toLowerCase()
    const bucket = state === "success" ? "success" : CHECK_BUCKETS[state] ?? "pending"
    buckets[bucket] += 1
  }
  // An unrecognised state is a wait, not a failure. Go sends only the four
  // canonical states, but older snapshots and third-party sources may differ.
  const parts = []
  if (buckets.failed) parts.push(`${buckets.failed} failed`)
  parts.push(`${buckets.success}/${list.length}`)
  if (buckets.skipped) parts.push(`${buckets.skipped} skipped`)
  if (buckets.pending) parts.push(`${buckets.pending} pending`)
  // GitHub runs required checks in parallel, so their durations overlap. The
  // Go side measures the real span; the longest job is only a fallback.
  const longest = list.reduce((max, check) => Math.max(max, Number(check?.durationSeconds) || 0), 0)
  const wall = Number(ciWallSeconds !== undefined ? ciWallSeconds : list.ciWallSeconds)
  if (Number.isFinite(wall) && wall > 0) parts.push(clockDuration(wall))
  else if (longest > 0) parts.push(`longest ${clockDuration(longest)}`)
  return parts.join(" · ")
}
