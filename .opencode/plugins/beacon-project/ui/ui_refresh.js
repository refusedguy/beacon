// Refresh and live bridge for the Beacon status panel. Each read carries a
// generation number, so a stale read cannot overwrite a newer snapshot.
import { EVENT_IDLE_MS, BeaconStatusRpc, cleanErrorMessage, str, tuiLocation } from "./ui_model.js"
import { compactReason } from "./ui_alerts.js"
import { createRunChangeBridge } from "./ui_bridge.js"
import { pollIntervalMs, watcherEnabled } from "../state/options.js"

// One notification per run that reaches a terminal or blocked state. The
// caller owns the state map so each setup keeps its own first-render baseline.
export function collectTransitionToasts(previous, snapshot) {
  const runs = new Map()
  for (const run of [...(snapshot?.active ?? []), ...(snapshot?.recent ?? [])]) {
    if (run?.id && !runs.has(run.id)) runs.set(run.id, run)
  }
  const toasts = []
  for (const [id, run] of runs) {
    const state = str(run.state).toLowerCase()
    const before = previous.get(id)
    previous.set(id, state)
    if (before === undefined || before === state) continue
    if (state === "merged") {
      toasts.push({ message: `✔ Merged #${run.pr || "?"}: ${str(run.title) || str(run.taskID)}`, variant: "success" })
    } else if (state === "blocked" || state === "closed") {
      toasts.push({ message: `▲ ${str(run.taskID) || id}: ${compactReason(run.reason)}`, variant: state === "blocked" ? "error" : "warning" })
    }
  }
  return toasts
}

export function createRefresh(env, applyAll) {
  const { ctx, source, options, timers, flags, signals, lastSeenState } = env

  // The footer and sidebar read these fields from the snapshot. They travel
  // with the data so a refresh always renders the state that produced it.
  const withStatus = (snapshot, live) => snapshot && typeof snapshot === "object"
    ? { ...snapshot, live, root: source.root?.(), verified: source.verified?.() === true }
    : snapshot

  const notify = (snapshot) => {
    for (const toast of collectTransitionToasts(lastSeenState, snapshot)) {
      ctx.ui?.toast?.show?.(toast)
    }
  }

  const refresh = async () => {
    if (flags.stopped) return
    const mine = ++flags.generation
    if (flags.inFlight) { flags.pending = true; return }
    flags.inFlight = true
    let rendered = false
    try {
      const next = await source.snapshot()
      if (flags.stopped) return
      if (mine === flags.generation) {
        // An empty cwd snapshot is not a baseline. A snapshot taken during an
        // outage may miss transitions, so toasts wait for a confirmed poll.
        const empty = !next || (!next.active?.length && !next.recent?.length && !next.queue?.length)
        if (next != null && !(empty && !source.verified())) {
          if (!flags.stale) notify(next)
          signals.setSnapshot(withStatus(next, "live"))
          rendered = true
        }
        flags.failures = 0
        flags.stale = false
        flags.retryPending = false
        signals.setError(null)
      }
    } catch (cause) {
      if (flags.stopped) return
      if (mine === flags.generation) {
        flags.failures += 1
        // The first miss only asks for a retry. The second one admits the
        // snapshot is stale and shows a cause together with a way back.
        if (flags.failures >= 2) { flags.stale = true; signals.setError(cleanErrorMessage(cause)) }
        flags.retryPending = !flags.stale
        signals.setSnapshot(withStatus(signals.m(), flags.stale ? "stale" : "retry pending"))
        rendered = true
      }
    } finally { flags.inFlight = false }
    if (rendered && !flags.stopped) applyAll()
    if (flags.pending && !flags.stopped) { flags.pending = false; void refresh() }
  }

  let rpc = null
  try { rpc = typeof ctx?.client?.rpc === "function" ? ctx.client.rpc(BeaconStatusRpc) : null } catch (error) {
    console.debug(`beacon status panel: RPC client unavailable: ${error?.message ?? error}`)
  }
  const bridge = rpc?.events && watcherEnabled(options)
    ? createRunChangeBridge(rpc, tuiLocation(ctx), () => void refresh(), timers) : null

  void refresh()
  const pollMs = pollIntervalMs(options.statusPollMs)
  if (pollMs > 0) {
    flags.pollTimer = timers.setInterval(() => {
      if (flags.stopped) return
      if (bridge?.active && timers.now() - bridge.lastEventAt < EVENT_IDLE_MS) return
      void refresh()
    }, pollMs)
  }
  return { refresh, bridge }
}
