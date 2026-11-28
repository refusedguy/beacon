// Run: node --test .opencode/plugins/beacon-project/ui/ui_bridge_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { createRunChangeBridge } from "./ui_bridge.js"

const OWN_LOCATION = {
  directory: "/project",
  project: { canonical: "/project", id: "project-1" },
  workspaceID: "workspace-1",
}

// The watcher emits this flat shape through the RPC event. Older peers wrap
// the same fields in data, which the bridge also accepts.
function runEvent(overrides = {}) {
  return {
    id: "run-1", sha: "abc", state: "running", at: "t1",
    key: "key-1", occurrence: 1,
    location: OWN_LOCATION,
    ...overrides,
  }
}

function harness({ on, onEvent } = {}) {
  let clock = 1000
  let nextHandle = 0
  let subscriptions = 0
  let unsubscribes = 0
  const timers = []
  const cleared = []
  const handlers = []
  const eventNames = []
  const events = []
  const rpc = {
    events: {
      on(name, handler) {
        eventNames.push(name)
        subscriptions += 1
        const plan = on?.(subscriptions) ?? {}
        if (plan.throws) throw new Error("subscription failed")
        handlers.push(handler)
        if ("unsubscribe" in plan) return plan.unsubscribe
        return () => { unsubscribes += 1 }
      },
    },
  }
  const bridge = createRunChangeBridge(rpc, OWN_LOCATION, (event) => {
    events.push(event)
    onEvent?.(event)
  }, {
    now: () => clock,
    setTimeout: (fn, delay) => {
      const id = ++nextHandle
      timers.push({ id, fn, delay })
      return id
    },
    clearTimeout: (id) => { cleared.push(id) },
  })
  return {
    bridge, timers, cleared, handlers, eventNames, events,
    setClock: (value) => { clock = value },
    emit: (event) => handlers.at(-1)?.(event),
    fireTimers: () => timers.splice(0).forEach((timer) => timer.fn()),
    get subscriptions() { return subscriptions },
    get unsubscribes() { return unsubscribes },
  }
}

test("creation subscribes to runChanged and exposes the initial state", () => {
  const h = harness()
  assert.deepEqual(h.eventNames, ["runChanged"])
  assert.equal(h.subscriptions, 1)
  assert.equal(h.bridge.active, true)
  assert.equal(h.bridge.pendingRetry, false)
  assert.equal(h.bridge.lastEventAt, 0)
})

test("a matching event calls onEvent and updates lastEventAt", () => {
  const h = harness()
  const value = runEvent()
  h.emit(value)
  assert.deepEqual(h.events, [value])
  assert.equal(h.bridge.lastEventAt, 1000)
  h.setClock(2000)
  h.emit(runEvent({ key: "key-2", id: "run-2", occurrence: 2 }))
  assert.deepEqual(h.events.map((event) => event.key), ["key-1", "key-2"])
  assert.equal(h.bridge.lastEventAt, 2000)
})

test("events from another project or workspace are ignored", () => {
  const h = harness()
  h.emit(runEvent({ location: { directory: "/elsewhere", project: { canonical: "/elsewhere" } } }))
  h.emit(runEvent({ location: { ...OWN_LOCATION, workspaceID: "workspace-2" } }))
  assert.deepEqual(h.events, [])
  assert.equal(h.bridge.lastEventAt, 0)
  h.emit(runEvent())
  assert.equal(h.events.length, 1)
})

test("a live event without a location is not dropped by the filter", () => {
  const h = harness()
  const value = runEvent()
  delete value.location
  h.emit(value)
  assert.deepEqual(h.events, [value])
  assert.equal(h.bridge.lastEventAt, 1000)
})

test("events with the same key are delivered once", () => {
  const h = harness()
  h.emit(runEvent())
  h.emit(runEvent({ key: "key-1", id: "run-other", occurrence: 1 }))
  assert.equal(h.events.length, 1)
  const payload = { id: "run-9", sha: "sha-9", state: "running", at: "t9", key: "key-9" }
  h.emit(runEvent(payload))
  h.emit(runEvent(payload))
  h.emit(runEvent({ key: "key-2", id: "run-2", occurrence: 3 }))
  assert.deepEqual(h.events.map((event) => event.key ?? event.id), ["key-1", "key-9", "key-2"])
})

test("an occurrence distinguishes repeated transitions with the same values", () => {
  const h = harness()
  const first = runEvent({ key: "repeat", occurrence: 1 })
  const second = runEvent({ key: "repeat", occurrence: 2 })
  h.emit(first)
  h.emit(second)
  assert.deepEqual(h.events.map((event) => event.occurrence), [1, 2])
})

test("wrapped payloads from older peers are still deduped", () => {
  const h = harness()
  h.emit({ location: OWN_LOCATION, data: { key: "wrapped", occurrence: 1 } })
  h.emit({ location: OWN_LOCATION, data: { key: "wrapped", occurrence: 1 } })
  assert.equal(h.events.length, 1)
})

test("dedupe keeps a bounded set of keys", () => {
  const h = harness()
  for (let i = 0; i < 129; i++) h.emit(runEvent({ key: `key-${i}`, occurrence: i + 1 }))
  assert.equal(h.events.length, 129)
  h.emit(runEvent({ key: "key-0", occurrence: 1 }))
  assert.equal(h.events.length, 130, "the oldest key fell out of the dedupe window")
  h.emit(runEvent({ key: "key-128", occurrence: 129 }))
  assert.equal(h.events.length, 130, "a recent key is still deduped")
})

test("resync and ping notifications always reach the panel", () => {
  const h = harness()
  for (const type of ["resync", "gap", "ping", "resync", "gap", "ping"]) {
    h.emit({ type, location: OWN_LOCATION })
  }
  assert.equal(h.events.length, 6)
  assert.deepEqual(h.events.map((event) => event.type), ["resync", "gap", "ping", "resync", "gap", "ping"])
})

test("failed subscribes retry with exponential backoff capped at 30s", () => {
  const h = harness({ on: (n) => (n < 7 ? { throws: true } : {}) })
  assert.equal(h.bridge.active, false)
  assert.equal(h.bridge.pendingRetry, true)
  const delays = []
  for (let i = 0; i < 6; i++) {
    assert.equal(h.timers.length, 1, "one retry is pending at a time")
    delays.push(h.timers[0].delay)
    h.fireTimers()
  }
  assert.deepEqual(delays, [1000, 2000, 4000, 8000, 16000, 30000])
  assert.equal(h.subscriptions, 7)
  assert.equal(h.bridge.active, true)
  assert.equal(h.bridge.pendingRetry, false)
  assert.deepEqual(h.eventNames, Array(7).fill("runChanged"))
})

test("a subscribe without an unsubscribe function is retried", () => {
  const h = harness({ on: (n) => (n === 1 ? { unsubscribe: null } : {}) })
  assert.equal(h.bridge.active, false)
  assert.equal(h.bridge.pendingRetry, true)
  assert.deepEqual(h.timers.map((timer) => timer.delay), [1000])
  h.fireTimers()
  assert.equal(h.bridge.active, true)
  assert.equal(h.subscriptions, 2)
})

test("a throwing callback tears down, resubscribes and does not burn the key", () => {
  const h = harness({
    onEvent: (event) => {
      if (String(event.key).startsWith("boom")) throw new Error("refresh failed")
    },
  })
  h.emit(runEvent({ key: "boom" }))
  assert.equal(h.bridge.active, false)
  assert.equal(h.unsubscribes, 1)
  assert.deepEqual(h.timers.map((timer) => timer.delay), [1000])
  h.fireTimers()
  assert.equal(h.bridge.active, true)
  assert.equal(h.bridge.pendingRetry, false)
  assert.equal(h.subscriptions, 2)
  // The first callback threw before the key was acknowledged, so the same
  // event is delivered again after the retry instead of being silently lost.
  h.emit(runEvent({ key: "boom" }))
  assert.equal(h.events.length, 2)
  assert.equal(h.bridge.active, false)
  h.fireTimers()
  assert.equal(h.bridge.active, true)
})

test("stop unsubscribes and ignores events delivered to a stale handler", () => {
  const h = harness()
  const stale = h.handlers[0]
  h.bridge.stop()
  assert.equal(h.unsubscribes, 1)
  assert.equal(h.bridge.active, false)
  assert.equal(h.bridge.pendingRetry, false)
  stale(runEvent({ key: "after-stop" }))
  assert.deepEqual(h.events, [])
  assert.equal(h.bridge.lastEventAt, 0)
})

test("stop cancels a pending retry and blocks a late timer callback", () => {
  const h = harness({ on: () => ({ throws: true }) })
  assert.equal(h.bridge.pendingRetry, true)
  const timer = h.timers[0]
  h.bridge.stop()
  assert.deepEqual(h.cleared, [timer.id])
  assert.equal(h.bridge.pendingRetry, false)
  assert.equal(h.bridge.active, false)
  timer.fn()
  assert.equal(h.subscriptions, 1, "the stopped bridge does not resubscribe")
  assert.equal(h.bridge.pendingRetry, false)
})
