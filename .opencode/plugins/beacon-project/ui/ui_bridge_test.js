// Run: node --test .opencode/plugins/beacon-project/ui_bridge_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { createRunChangeBridge } from "./ui_bridge.js"

const OWN_LOCATION = {
  directory: "/project",
  project: { canonical: "/project", id: "project-1" },
  workspaceID: "workspace-1",
}

function runEvent(overrides = {}) {
  return {
    location: OWN_LOCATION,
    data: { key: "key-1", id: "run-1", sha: "abc", state: "running", at: "t1" },
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
  h.emit(runEvent({ data: { key: "key-2", id: "run-2" } }))
  assert.deepEqual(h.events.map((event) => event.data.key), ["key-1", "key-2"])
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

test("events with the same key are delivered once", () => {
  const h = harness()
  h.emit(runEvent())
  h.emit(runEvent({ data: { key: "key-1", id: "run-other" } }))
  assert.equal(h.events.length, 1)
  const payload = { id: "run-9", sha: "sha-9", state: "running", at: "t9" }
  h.emit(runEvent({ data: payload }))
  h.emit(runEvent({ data: { ...payload } }))
  h.emit(runEvent({ data: { key: "key-2", id: "run-2" } }))
  assert.deepEqual(h.events.map((event) => event.data.key ?? event.data.id), ["key-1", "run-9", "key-2"])
})

test("dedupe keeps a bounded set of keys", () => {
  const h = harness()
  for (let i = 0; i < 129; i++) h.emit(runEvent({ data: { key: `key-${i}` } }))
  assert.equal(h.events.length, 129)
  h.emit(runEvent({ data: { key: "key-0" } }))
  assert.equal(h.events.length, 130, "the oldest key fell out of the dedupe window")
  h.emit(runEvent({ data: { key: "key-128" } }))
  assert.equal(h.events.length, 130, "a recent key is still deduped")
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

test("a throwing callback tears down and resubscribes with a fresh backoff", () => {
  const h = harness({
    onEvent: (event) => {
      if (String(event.data?.key).startsWith("boom")) throw new Error("refresh failed")
    },
  })
  h.emit(runEvent({ data: { key: "boom" } }))
  assert.equal(h.bridge.active, false)
  assert.equal(h.unsubscribes, 1)
  assert.deepEqual(h.timers.map((timer) => timer.delay), [1000])
  h.fireTimers()
  assert.equal(h.bridge.active, true)
  assert.equal(h.bridge.pendingRetry, false)
  assert.equal(h.subscriptions, 2)
  h.emit(runEvent({ data: { key: "boom-2" } }))
  assert.deepEqual(h.timers.map((timer) => timer.delay), [1000], "a successful retry resets the delay")
})

test("stop unsubscribes and ignores events delivered to a stale handler", () => {
  const h = harness()
  const stale = h.handlers[0]
  h.bridge.stop()
  assert.equal(h.unsubscribes, 1)
  assert.equal(h.bridge.active, false)
  assert.equal(h.bridge.pendingRetry, false)
  stale(runEvent({ data: { key: "after-stop" } }))
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
