// Live subscriptions miss events while the client is disconnected. A failed
// subscription is dropped and retried with exponential backoff; the poll in
// setup keeps the panel fresh until the subscription recovers.
import { locationInfo } from "./location.js"
import {
  RESUBSCRIBE_MAX_MS, RESUBSCRIBE_MIN_MS, RUN_CHANGED_EVENT,
  eventKey, eventMatchesLocation,
} from "./ui_model.js"

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
          if (!eventMatchesLocation(event?.location, ownLocation)) return
          const key = eventKey(event?.data)
          if (key && keys.has(key)) return
          if (key) { keys.add(key); if (keys.size > 128) keys = new Set([...keys].slice(-64)) }
          lastEventAt = now()
          onEvent(event)
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
