// Live subscriptions miss events while the client is disconnected. A failed
// subscription is dropped and retried with exponential backoff; the poll in
// setup keeps the panel fresh until the subscription recovers.
import { locationInfo } from "../state/location.js"
import {
  RESUBSCRIBE_MAX_MS, RESUBSCRIBE_MIN_MS, RUN_CHANGED_EVENT,
  eventKey, eventMatchesLocation, str,
} from "./ui_model.js"

// Notifications carry no run identity. Each one must reach the panel, so the
// dedupe window does not record them.
const NOTIFICATION_TYPES = new Set(["ping", "resync", "gap", "reconnected"])

export function createRunChangeBridge(rpc, ownLocation, onEvent, deps = {}) {
  const now = deps.now ?? (() => Date.now())
  const setTimer = deps.setTimeout ?? globalThis.setTimeout.bind(globalThis)
  const clearTimer = deps.clearTimeout ?? globalThis.clearTimeout.bind(globalThis)
  let off = null
  let retry = null
  let attempts = 0
  let lastEventAt = 0
  let stopped = false
  let keys = new Set()
  const teardown = () => {
    const current = off
    off = null
    try { current?.() } catch {}
  }
  const scheduleRetry = () => {
    if (stopped || retry) return
    const delay = Math.min(RESUBSCRIBE_MIN_MS * 2 ** attempts, RESUBSCRIBE_MAX_MS)
    attempts += 1
    retry = setTimer(() => { retry = null; subscribe() }, delay)
  }
  const subscribe = () => {
    if (stopped) return
    try {
      const unsubscribe = rpc.events.on(RUN_CHANGED_EVENT, (event) => {
        if (stopped) return
        try {
          // The watcher emits a flat payload. Older peers wrap it in data.
          const payload = event?.data ?? event
          // A live event without a location is not evidence that it belongs to
          // another project. Only filter when the sender states a location.
          if (event?.location != null && !eventMatchesLocation(event.location, ownLocation)) return
          const key = NOTIFICATION_TYPES.has(str(event?.type)) ? "" : eventKey(payload)
          if (key && keys.has(key)) return
          lastEventAt = now()
          // Acknowledge only after the callback accepts the event. A callback
          // that throws retries the subscription and must see the event again.
          onEvent(event)
          if (key) { keys.add(key); if (keys.size > 128) keys = new Set([...keys].slice(-64)) }
        } catch {
          teardown()
          scheduleRetry()
        }
      })
      if (typeof unsubscribe !== "function") throw new Error("events.on did not return an unsubscribe function")
      off = unsubscribe
      attempts = 0
    } catch {
      teardown()
      scheduleRetry()
    }
  }
  subscribe()
  return {
    get active() { return typeof off === "function" },
    get lastEventAt() { return lastEventAt },
    get pendingRetry() { return retry != null },
    stop() {
      stopped = true
      if (retry) clearTimer(retry)
      retry = null
      teardown()
      keys.clear()
    },
  }
}
