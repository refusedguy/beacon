// Run: node --test .opencode/plugins/beacon-project/ui_actions_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { makeRun, solidModule, themeStub } from "./ui_test_helpers.js"
import { beaconStatusDefinition, createActionRunner, normalizeRun } from "./ui.js"

const run = () => normalizeRun(makeRun({ ID: "run-1", State: "blocked" }))

function harness(overrides = {}) {
  const toasts = []
  const controls = []
  const dispatches = []
  let refreshes = 0
  const source = {
    control: async (input) => { controls.push(input); return { ok: true, ...(overrides.controlResult ?? {}) } },
    dispatch: async (input) => { dispatches.push(input); return { started: 1 } },
  }
  const ctx = {
    ui: {
      toast: { show: (options) => toasts.push(options) },
      panel: { open: () => {}, close: () => {} },
      ...(overrides.ui ?? {}),
    },
    ...(overrides.renderer ? { renderer: overrides.renderer } : {}),
  }
  const invoke = createActionRunner(ctx, source, { refresh: async () => { refreshes++ } })
  return { invoke, toasts, controls, dispatches, get refreshes() { return refreshes }, ctx, source }
}

function findText(node, content) {
  if (node.props?.content === content) return node
  for (const child of node.children ?? []) {
    const hit = findText(child, content)
    if (hit) return hit
  }
  return null
}
function signals() {
  return { createSignal: (v) => { let x = v; return [() => x, (n) => { x = typeof n === "function" ? n(x) : n }] } }
}

test("Retry and Approve call control, toast the result and refresh", async () => {
  const h = harness()
  await h.invoke("retry", run())
  assert.deepEqual(h.controls, [{ action: "retry", runID: "run-1", sha: "abcdef1234567890" }])
  assert.equal(h.toasts[0].variant, "success")
  assert.match(h.toasts[0].message, /retry accepted/)
  assert.equal(h.refreshes, 1)
  await h.invoke("approve", run())
  assert.equal(h.controls[1].action, "approve")
  assert.equal(h.refreshes, 2)
})

test("a refused control result reports an error and still refreshes", async () => {
  const h = harness({ controlResult: { ok: false, reason: "run is not active" } })
  await h.invoke("retry", run())
  assert.equal(h.toasts[0].variant, "error")
  assert.match(h.toasts[0].message, /run is not active/)
  assert.equal(h.refreshes, 1)
})

test("Cancel asks for confirmation and only cancels on agreement", async () => {
  let answer = false
  const h = harness({ ui: { dialog: { confirm: async () => answer } } })
  await h.invoke("cancel", run())
  assert.equal(h.controls.length, 0, "a declined dialog changes nothing")
  assert.equal(h.toasts[0].message, "Run kept, nothing cancelled")
  assert.equal(h.refreshes, 1)
  answer = true
  await h.invoke("cancel", run())
  assert.equal(h.controls[0].action, "cancel")
  assert.equal(h.toasts[1].variant, "success")
  assert.equal(h.refreshes, 2)
})

test("no dialog API means no unconfirmed cancel", async () => {
  const h = harness()
  await h.invoke("cancel", run())
  assert.equal(h.controls.length, 0)
  assert.match(h.toasts[0].message, /unavailable/)
  assert.equal(h.toasts[0].variant, "warning")
  assert.equal(h.refreshes, 1)
})

test("Copy ID uses OSC52 and reports honestly when unavailable", async () => {
  const copied = []
  const ok = harness({ renderer: { copyToClipboardOSC52: (text) => { copied.push(text); return true } } })
  await ok.invoke("copy", run())
  assert.deepEqual(copied, ["run-1 · beacon/T-1"])
  assert.equal(ok.toasts[0].variant, "success")

  const refused = harness({ renderer: { copyToClipboardOSC52: () => false } })
  await refused.invoke("copy", run())
  assert.equal(refused.toasts[0].variant, "warning")

  const missing = harness()
  await missing.invoke("copy", run())
  assert.equal(missing.toasts[0].message, "Clipboard unavailable in this TUI")
})

test("Dispatch starts one task, toasts the result and refreshes", async () => {
  const h = harness()
  await h.invoke("dispatch")
  assert.deepEqual(h.dispatches, [{ n: 1 }])
  assert.equal(h.toasts[0].message, "Dispatched 1 task")
  assert.equal(h.refreshes, 1)
})

test("panel buttons and the Dispatch row call the RPC through the real setup", async () => {
  const created = []
  const renderers = Object.create(null)
  const toasts = []
  const controls = []
  const dispatches = []
  let snapshots = 0
  const rpc = {
    snapshot: async () => { snapshots++; return { active: [makeRun({ State: "blocked" })], recent: [] } },
    control: async (input) => { controls.push(input); return { ok: true } },
    dispatch: async (input) => { dispatches.push(input); return { started: 1 } },
    events: { on: () => () => {} },
  }
  const ctx = {
    theme: themeStub(),
    client: { rpc: () => rpc },
    storage: { store: () => [{ id: "run-1" }, async () => {}] },
    renderer: { copyToClipboardOSC52: () => true },
    ui: {
      toast: { show: (options) => toasts.push(options) },
      slot: ({ append, render }) => { renderers[append] = render; return () => { delete renderers[append] } },
      panel: { open: () => {}, close: () => {} },
    },
    keymap: { layer: () => () => {} },
  }
  const modules = {
    "@opentui/solid": solidModule(created),
    "solid-js": signals(),
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const dispose = await beaconStatusDefinition.setup(ctx, {
    options: { statusPollMs: 0, watcherEnabled: false },
    import: (specifier) => modules[specifier],
  })
  await new Promise((resolve) => setImmediate(resolve))
  const detail = renderers["session.panel"]({ name: "beacon.detail" })
  const sidebar = renderers["sidebar.content"]()
  const retry = findText(detail, "↻ Retry")
  const copy = findText(detail, "⧉ Copy ID")
  const approve = findText(detail, "✔ Approve")
  const dispatch = findItem(sidebar, (item) => item.action === "dispatch")
  assert.ok(retry && copy && dispatch, "the controls are rendered")
  assert.equal(retry.props.visible, true, "Retry is available for a blocked run")
  assert.equal(approve.props.visible, false, "Approve is hidden for a blocked run")
  const before = snapshots
  retry.props.onMouseUp()
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(controls, [{ action: "retry", runID: "run-1", sha: "abcdef1234567890" }])
  assert.ok(snapshots > before, "the action refreshes the snapshot")

  copy.props.onMouseUp()
  assert.equal(toasts.some((t) => t.message.startsWith("Copied ")), true)
  dispatch.props.onMouseDown()
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(dispatches, [{ n: 1 }])
  dispose()
})

function findItem(container, predicate) {
  return container.children.find((child) => child.item && predicate(child.item))
}
