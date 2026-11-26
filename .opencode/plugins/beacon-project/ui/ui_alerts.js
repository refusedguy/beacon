// Presentation helpers for daemon and incident alerts, the queue, compact
// reasons, CI checks and human timeline copy. This module keeps ui_view.js
// under the 350-line limit.
import { CHECK_BUCKETS, failedCheckCount, REASON_WIDTH, str } from "./ui_model.js"

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

// One index per snapshot queue. Snapshots arrive as fresh arrays, so identity
// is a safe cache key: a tree never outlives the array it was built from. This
// lets the queue header, the rows and the card panel share a single tree
// instead of rebuilding it for every consumer in a render.
const treeCache = new WeakMap()
const depsOf = (item) => (Array.isArray(item?.dependsOn) ? item.dependsOn
  : Array.isArray(item?.depends_on) ? item.depends_on : []).map(str).filter(Boolean)

// TaskTree is the single index of parent-child relations for the queue and
// card panels. An explicit parent field is authoritative. Snapshots that still
// carry only generated IDs fall back to the longest ID prefix, so older
// daemons keep their layout until they publish the parent field.
export function taskTree(items = []) {
  const list = Array.isArray(items) ? items : []
  const cached = treeCache.get(list)
  if (cached) return cached
  // Read each card ID once. Blocker grouping and row building look it up in
  // idOf instead of reading the same field again for every consumer.
  const byID = new Map()
  const idOf = new Map()
  for (const item of list) {
    const id = str(item?.id)
    byID.set(id, item)
    idOf.set(item, id)
  }
  const parent = new Map()
  const ids = [...byID.keys()]
  for (const id of ids) {
    const value = str(byID.get(id)?.parent)
    if (value && value !== id && byID.has(value)) parent.set(id, value)
  }
  for (const id of ids) {
    if (parent.has(id)) continue
    // Walk this ID's own hyphen boundaries instead of scanning every other ID.
    // The first boundary that names a known card is the longest prefix, which is
    // what the old filter and sort selected.
    let found = ""
    for (let at = id.lastIndexOf("-"); at > 0; at = id.lastIndexOf("-", at - 1)) {
      const candidate = id.slice(0, at)
      if (byID.has(candidate)) { found = candidate; break }
    }
    parent.set(id, found)
  }
  const children = new Map()
  const epic = new Set()
  for (const item of list) {
    if (str(item?.kind).toLowerCase() === "epic") epic.add(str(item?.id))
  }
  for (const [child, value] of parent) {
    if (!value) continue
    if (!children.has(value)) children.set(value, [])
    children.get(value).push(child)
    epic.add(value)
  }
  const index = { list, byID, idOf, ids, parent, children, epic }
  treeCache.set(list, index)
  return index
}

export const spacerItem = () => ({
  marker: "", name: "", status: "", color: "muted", kind: "spacer",
})
const countLabel = (n, one, many) => `${n} ${n === 1 ? one : many}`

// The snapshot carries only unresolved deps. Children of an epic already have
// their own rows, so they never become blocker headers.
const queueBlockers = (item, parent) => depsOf(item).filter((dep) => dep && !parent.get(dep))

function taskRows(item, { id, epic, parent, blockers, children }) {
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
export function queueGroups(queue = [], hidden = null, tree = null) {
  const list = Array.isArray(queue) ? queue : []
  const index = tree ?? taskTree(list)
  const shown = hidden ? list.filter((item) => !hidden.has(str(item?.id))) : list
  const { parent, children, epic } = index
  // Membership lives here so the queue renders share one derivation of parent,
  // child and epic relations. Callers read these helpers instead of rebuilding
  // the maps and wrapping them again.
  const parentOf = (id) => parent.get(id) || ""
  const childrenOf = (id) => children.get(id) ?? []
  const isEpic = (id) => epic.has(id)
  // A dep that names a child card is an internal edge, not a blocker header.
  // The parent map already holds that answer, so no extra Set is needed.
  const ownIndex = index.list === list
  const ids = ownIndex ? index.ids : list.map((item) => str(item?.id))
  const idOf = ownIndex ? index.idOf : null
  const blockers = new Map(list.map((item, i) => [ids[i], queueBlockers(item, parent)]))
  const groupOf = (item) => {
    const id = idOf?.get(item) ?? str(item?.id)
    const own = blockers.get(id) ?? []
    return own[0] || (blockers.get(parent.get(id) || "") ?? [])[0] || ""
  }
  const groups = new Map()
  const order = []
  for (const item of shown) {
    const key = groupOf(item)
    if (!groups.has(key)) { groups.set(key, []); order.push(key) }
    groups.get(key).push(item)
  }
  return {
    groups, order, parents: parent, blockers, tree: index, idOf,
    parentOf, childrenOf, isEpic,
  }
}

export const blockedGroupKeys = (queue = [], tree = null) => queueGroups(queue, null, tree).order.filter(Boolean)

// An expanded epic or blocked group shows at most this many member rows. The
// rest collapse into a "+N more" line so Recent stays inside the sidebar.
export const QUEUE_CHILD_LIMIT = 3

// A null blockedOpen keeps the flat legacy rows for pure callers that never
// drive the toggle. With a map, only groups marked true expand.
export function queueLineItems(queue = [], hidden = null, blockedOpen = null, tree = null) {
  const { groups, order, blockers, parentOf, childrenOf, isEpic, idOf } = queueGroups(queue, hidden, tree)
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
      const id = idOf?.get(item) ?? str(item?.id)
      rows.push(...taskRows(item, {
        id, epic: isEpic(id),
        parent: parentOf(id), blockers: blockers.get(id) ?? [],
        children: childrenOf(id).length,
      }))
    }
    if (capped) rows.push({
      marker: "", name: `+${members.length - QUEUE_CHILD_LIMIT} more`, status: "",
      color: "muted", kind: "queue-more",
    })
  }
  return rows
}

export function queueRows(queue = [], epicOpen = null, blockedOpen = null, tree = null) {
  const list = Array.isArray(queue) ? queue : []
  // One tree per snapshot. The caller passes the index it already built for the
  // queue header; a standalone caller falls back to the cached index for the
  // same array. queueLineItems reads that index through its tree argument.
  const index = tree ?? taskTree(list)
  const { parentOf, childrenOf, isEpic } = queueGroups(list, null, index)
  if (!epicOpen) return queueLineItems(list, null, blockedOpen, index)
  const ids = index.list === list ? index.ids : list.map((item) => str(item?.id))
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
  const rows = queueLineItems(list, hidden, blockedOpen, index)
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

// One in-flight flag shared by the action runner and the sidebar view. The
// click sets it before the RPC settles, so the row can show Dispatching on the
// same frame as the click instead of waiting for a snapshot.
let dispatchInFlight = false
export const isDispatching = () => dispatchInFlight
export function setDispatching(value) {
  const next = value === true
  if (dispatchInFlight === next) return false
  dispatchInFlight = next
  return true
}

export function dispatchItem(ready = 0, dispatching = false) {
  if (dispatching) return {
    marker: "◐", name: "Dispatching", status: "", color: "warning",
    nameColor: "warning", markerColor: "warning", bold: true, action: "dispatch",
  }
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

// Truncation is pure, so cache by the pair that determines the answer. Titles,
// reasons and section names repeat on every poll; the cache keeps the
// code-point walk to the first render of each string. Each width gets a bounded
// FIFO map so a long session cannot grow it without limit.
const TRUNCATE_CACHE_LIMIT = 2048
const truncateCache = new Map()
const clip = (text, width) => {
  // A string shorter than the width in UTF-16 units also fits in code points,
  // so the common short title skips Array.from entirely.
  if (text.length <= width) return text
  const chars = Array.from(text)
  if (chars.length <= width) return text
  if (width <= 0) return ""
  if (width === 1) return "…"
  return `${chars.slice(0, width - 1).join("")}…`
}

// The host truncates from the middle, so clipped text is cut here from the end.
// No ellipsis ever appears in the middle of a rendered string.
export function truncate(value, width) {
  const text = str(value)
  let byText = truncateCache.get(width)
  if (byText) {
    const hit = byText.get(text)
    if (hit !== undefined) return hit
  } else {
    byText = new Map()
    truncateCache.set(width, byText)
  }
  const out = clip(text, width)
  if (byText.size >= TRUNCATE_CACHE_LIMIT) byText.delete(byText.keys().next().value)
  byText.set(text, out)
  return out
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
const reasonCache = new Map()
const REASON_CACHE_LIMIT = 2048
export function compactReason(reason) {
  const key = str(reason)
  const hit = reasonCache.get(key)
  if (hit !== undefined) return hit
  let text = key.replace(/\s+/g, " ").trim()
  if (text) {
    for (const [pattern, replacement] of REASON_RULES) {
      if (pattern.test(text)) { text = text.replace(pattern, replacement); break }
    }
    text = truncate(text.replace(/^:\s*/, "").replace(/\s+at\s+\/.*$/i, "").trim(), REASON_WIDTH)
  }
  if (reasonCache.size >= REASON_CACHE_LIMIT) reasonCache.delete(reasonCache.keys().next().value)
  reasonCache.set(key, text)
  return text
}

function clockDuration(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60 ? `${s % 60}s` : ""}`
  return `${Math.floor(m / 60)}h${m % 60 ? `${m % 60}m` : ""}`
}

export function checkRow(check) {
  const state = str(check?.state).toLowerCase()
  const failed = state === "failure" || state === "failed" || state === "error"
  const short = CHECK_BUCKETS[state] ?? ""
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
