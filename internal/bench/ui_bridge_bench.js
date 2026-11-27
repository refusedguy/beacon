// Measures event acceptance and dedupe in the client half of plugin event
// handling. The production arm drives the shipped createRunChangeBridge with an
// injected clock and timers; the two mirror arms copy its control flow so the
// cost of the eventKey join and the 128-entry Set trim can be read directly.
// No production file is instrumented and no timing number is trusted unless
// every arm accepts the same events and emits the same id:state:sha lines.
//
// Run: BENCH_TICKS=1001 BENCH_ROUNDS=5 node internal/bench/ui_bridge_bench.js
//
// CI uses the command above for the A/B/B/A/B/A perf job. A smoke pass uses
// smaller BENCH_TICKS and BENCH_ROUNDS values. The fixture, work, errs and
// limiter do not depend on the mode.
import assert from "node:assert/strict"
import { performance } from "node:perf_hooks"
import { createRunChangeBridge } from "../../.opencode/plugins/beacon-project/ui/ui_bridge.js"
import {
  eventKey, eventMatchesLocation, RESUBSCRIBE_MAX_MS, RESUBSCRIBE_MIN_MS,
  RUN_CHANGED_EVENT,
} from "../../.opencode/plugins/beacon-project/ui/ui_model.js"

function envInt(name, fallback, min) {
  const raw = process.env[name]
  if (raw === undefined || raw === "") return fallback
  const value = Number(raw)
  if (!Number.isSafeInteger(value) || value < min) {
    throw new Error(`${name} must be an integer >= ${min}; got ${JSON.stringify(raw)}`)
  }
  return value
}

const TICKS = envInt("BENCH_TICKS", 1001, 4)
const ROUNDS = envInt("BENCH_ROUNDS", 5, 1)
const DEDUPE_WINDOW = 128

const OWN_LOCATION = Object.freeze({
  directory: "/project",
  project: Object.freeze({ canonical: "/project", id: "project-1" }),
  workspaceID: "workspace-1",
})
const OTHER_LOCATION = Object.freeze({
  directory: "/elsewhere",
  project: Object.freeze({ canonical: "/elsewhere", id: "project-2" }),
  workspaceID: "workspace-2",
})
const STATES = ["running", "waiting_ci", "needs_rebase", "merged"]

// Four event shapes repeat: in-location unique, immediate duplicate,
// other-location and malformed. Every arm drops the last three at the same
// boundary, so the accepted set is identical before any key function runs.
const events = Array.from({ length: TICKS }, (_, i) => {
  const state = STATES[i % STATES.length]
  switch (i % 4) {
    case 0:
      return {
        location: OWN_LOCATION,
        data: { key: `key-${i}`, id: `run-${i}`, sha: `sha-${i}`, state, at: `t-${i}` },
      }
    case 1: {
      const prior = i - 1
      return {
        location: OWN_LOCATION,
        data: {
          key: `key-${prior}`, id: `run-${prior}`, sha: `sha-${prior}`,
          state: STATES[prior % STATES.length], at: `t-${prior}`,
        },
      }
    }
    case 2:
      return {
        location: OTHER_LOCATION,
        data: { key: `key-${i}`, id: `run-${i}`, sha: `sha-${i}`, state, at: `t-${i}` },
      }
    default:
      return { location: null, data: null }
  }
})

// A faithful copy of the bridge control flow with the dedupe key injected.
// before uses a full stringify key; after uses the production join key.
function createMirrorBridge(rpc, ownLocation, onEvent, deps, keyOf) {
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
          const key = keyOf(event?.data)
          if (key && keys.has(key)) return
          if (key) {
            keys.add(key)
            if (keys.size > DEDUPE_WINDOW) keys = new Set([...keys].slice(-64))
          }
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

const stringifyKey = (data) => JSON.stringify(data ?? null)
const arms = {
  before: (rpc, ownLocation, onEvent, deps) =>
    createMirrorBridge(rpc, ownLocation, onEvent, deps, stringifyKey),
  after: (rpc, ownLocation, onEvent, deps) =>
    createMirrorBridge(rpc, ownLocation, onEvent, deps, eventKey),
  production: createRunChangeBridge,
}

// The timed drive injects one subscription failure so the retry path runs.
// No other event causes a retry: location and dedupe drops stay silent.
function drive(make, failSubscribe) {
  let clock = 1000
  let nextTimer = 0
  let subscriptions = 0
  let accepted = 0
  let retried = 0
  let errs = 0
  const timers = []
  const handlers = []
  const emitted = []
  const rpc = {
    events: {
      on(name, handler) {
        subscriptions += 1
        if (failSubscribe && subscriptions === 1) {
          errs += 1
          throw new Error("subscription failed")
        }
        handlers.push(handler)
        return () => {}
      },
    },
  }
  const deps = {
    now: () => clock,
    setTimeout: (fn, delay) => {
      const id = ++nextTimer
      retried += 1
      timers.push({ id, fn, delay })
      return id
    },
    clearTimeout: () => {},
  }
  const bridge = make(rpc, OWN_LOCATION, (event) => {
    accepted += 1
    emitted.push(`${event.data.id}:${event.data.state}:${event.data.sha}`)
  }, deps)
  // Fire the pending retry so the subscription is live before any event lands.
  for (const timer of timers.splice(0)) timer.fn()
  for (const event of events) handlers.at(-1)?.(event)
  assert.equal(bridge.active, true, "the recovered bridge must be subscribed")
  assert.equal(bridge.pendingRetry, false, "no retry may remain after recovery")
  return { accepted, dropped: events.length - accepted, retried, errs, emitted, subscriptions }
}

// Untimed control. The same fixture without a subscription failure must retry
// zero times and accept exactly what the timed drive accepted.
function control(make) {
  const result = drive(make, false)
  assert.equal(result.retried, 0, "a clean subscription never retries")
  assert.equal(result.errs, 0, "a clean subscription raises no error")
  return result
}

// A throwing handler tears the bridge down and schedules a fresh retry.
function handlerThrowProbe(make) {
  const handlers = []
  const timers = []
  const rpc = {
    events: {
      on(name, handler) {
        handlers.push(handler)
        return () => {}
      },
    },
  }
  const deps = {
    now: () => 1000,
    setTimeout: (fn) => { timers.push(fn); return timers.length },
    clearTimeout: () => {},
  }
  const bridge = make(rpc, OWN_LOCATION, (event) => {
    if (event.data?.id === "boom") throw new Error("handler failed")
  }, deps)
  handlers.at(-1)({
    location: OWN_LOCATION,
    data: { key: "boom", id: "boom", sha: "sha-boom", state: "running", at: "t-boom" },
  })
  assert.equal(bridge.active, false, "a throwing handler tears down the subscription")
  assert.equal(timers.length, 1, "a throwing handler schedules one retry")
  timers.shift()()
  assert.equal(bridge.active, true, "the retry restores the subscription")
  assert.equal(bridge.pendingRetry, false)
  bridge.stop()
  return 1
}

// A timer that fires after stop must not resubscribe the bridge.
function lateTimerProbe(make) {
  let subscriptions = 0
  const timers = []
  const rpc = {
    events: {
      on() {
        subscriptions += 1
        throw new Error("subscription failed")
      },
    },
  }
  const deps = {
    now: () => 1000,
    setTimeout: (fn) => { timers.push(fn); return timers.length },
    clearTimeout: () => {},
  }
  const bridge = make(rpc, OWN_LOCATION, () => {}, deps)
  assert.equal(timers.length, 1, "the failed subscription scheduled one retry")
  const timer = timers[0]
  bridge.stop()
  timer()
  assert.equal(subscriptions, 1, "a stale timer does not resubscribe after stop")
  assert.equal(bridge.active, false)
  assert.equal(bridge.pendingRetry, false)
  return 1
}

const samples = { before: [], after: [], production: [] }
for (let round = 0; round < ROUNDS; round += 1) {
  const order = round % 2 ? ["production", "after", "before"] : ["before", "after", "production"]
  for (const name of order) {
    const start = performance.now()
    const result = drive(arms[name], true)
    samples[name].push({ ...result, ms: performance.now() - start })
  }
}

// Every arm accepts the same events and emits the same lines in every round.
const first = samples.before[0]
for (let round = 0; round < ROUNDS; round += 1) {
  for (const name of ["after", "production"]) {
    const sample = samples[name][round]
    assert.deepEqual(sample.emitted, first.emitted, `${name} emitted different run lines`)
    for (const key of ["accepted", "dropped", "retried", "errs", "subscriptions"]) {
      assert.equal(sample[key], first[key], `${name} disagreed on ${key}`)
    }
  }
}
assert.equal(first.retried, 1, "only the injected subscription failure retries")
assert.equal(first.accepted + first.dropped, TICKS, "every event was accepted or dropped")

for (const name of ["before", "after", "production"]) {
  const clean = control(arms[name])
  assert.equal(clean.accepted, first.accepted, `${name} clean arm accepted a different set`)
  assert.deepEqual(clean.emitted, first.emitted, `${name} clean arm emitted different run lines`)
  assert.equal(clean.dropped, first.dropped)
}

const errs = samples.production.reduce((total, sample) => total + sample.errs, 0)
  + handlerThrowProbe(arms.production)
  + lateTimerProbe(arms.production)
const median = (xs) => {
  const sorted = [...xs].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2
}
const production = samples.production.map((sample) => sample.ms)
const p = {
  ms: median(production),
  min: Math.min(...production),
  max: Math.max(...production),
}

console.log(`run-bridge: median ${p.ms.toFixed(2)} ms, range ${p.min.toFixed(2)} to ${p.max.toFixed(2)} ms`)
console.log(`run-bridge-work: accepted=${first.accepted} dropped=${first.dropped} retried=${first.retried}`)
console.log(`run-bridge-errs: ${errs} errors`)
console.log("limiter: eventKey join + 128-entry Set trim")
