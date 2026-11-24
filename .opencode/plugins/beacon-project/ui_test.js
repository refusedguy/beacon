// Tests for the Beacon status view model and TUI plugin contract.
// Run: node --test .opencode/plugins/beacon-project/ui_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import beaconStatus, {
  BEACON_STATUS_RPC_ID,
  BeaconStatusRpc,
  CliStatusSource,
  EMPTY_SNAPSHOT,
  PANEL_NAME,
  RECENT_LIMIT,
  REASON_WIDTH,
  TITLE_WIDTH,
  beaconStatusDefinition,
  createCliStatusSource,
  createRowMount,
  createStatusSource,
  defineBeaconStatusPlugin,
  detailCommand,
  detailLines,
  elapsed,
  enrichRuns,
  errorMessage,
  normalizeRun,
  normalizeSnapshot,
  parseSnapshot,
  readTitle,
  renderSnapshotLines,
  runLineItems,
  snapshotLineItems,
  truncate,
} from "./ui.js"

const NOW = Date.parse("2026-11-23T10:10:00Z")

function makeRun(overrides = {}) {
  return {
    ID: "run-1",
    TaskID: "T-1",
    State: "running",
    PRNumber: 42,
    HeadSHA: "abcdef1234567890",
    Branch: "beacon/T-1",
    RebaseAttempts: 2,
    FixAttempts: 1,
    MaxAttempts: 3,
    Reason: "waiting CI",
    CreatedAt: "2026-11-23T10:00:00Z",
    UpdatedAt: "2026-11-23T10:07:00Z",
    Timeline: [{ at: "2026-11-23T10:05:00Z", kind: "checks_pending", state: "waiting_ci" }],
    Decisions: [{ at: "2026-11-23T10:06:00Z", kind: "reconcile.decision", verdict: "merge" }],
    Durations: { waiting_ci_seconds: 60, running_seconds: 5, blocked_seconds: 0 },
    ...overrides,
  }
}

const lines = (snapshot, error = null, now = NOW) => renderSnapshotLines(snapshot, error, now)

test("default export is a Plugin.define definition", () => {
  let received = null
  const result = defineBeaconStatusPlugin({ define: (definition) => { received = definition; return definition } })
  assert.equal(received, result)
  assert.equal(received, beaconStatusDefinition)
  assert.equal(beaconStatus.id, "beacon-project-status")
  assert.equal(beaconStatusDefinition.id, "beacon-project-status")
  assert.equal(typeof beaconStatusDefinition.setup, "function")
  assert.equal(beaconStatus.setup, beaconStatusDefinition.setup)
})

test("compact list renders two lines per run with no rule and no repeated title", () => {
  assert.deepEqual(lines({
    active: [makeRun({ title: "Fix TUI plugin shape so Beacon panel renders in the OpenCode sidebar" })],
    recent: [],
  }), [
    "Beacon",
    "▼ Active 1",
    "● T-1 Fix TUI plugin sh… RUN",
    "#42 3m waiting CI",
    "▼ Recent 0",
    "No recent runs",
  ])
  assert.equal(truncate("abcdefghijklmnopqrst", TITLE_WIDTH).length, TITLE_WIDTH)
})

test("normalizeRun leaves title empty instead of copying the task ID", () => {
  const run = normalizeRun(makeRun())
  assert.equal(run.title, "")
  assert.equal(run.state, "running")
  assert.equal(run.durations.waitingCI, 60)
  assert.equal(run.timeline[0].state, "waiting_ci")
  assert.equal(run.decisions[0].verdict, "merge")
  assert.equal(normalizeRun({ State: "" }).state, "unknown")
})

test("compact list does not repeat the task ID when no title exists", () => {
  const result = lines({ active: [makeRun({ title: "" })], recent: [] })
  assert.equal(result[2], "● T-1 RUN")
  assert.equal(result.filter((line) => line.includes("T-1")).length, 1)
})

test("shortens a long task ID once", () => {
  const result = lines({ active: [makeRun({ TaskID: "T-20261123-613986e5", title: "Short" })], recent: [] })
  assert.equal(result[2], "● 613986e5 Short RUN")
  assert.equal(result.filter((line) => line.includes("613986e5")).length, 1)
})

test("recent caps at five, dedupes by run ID and keeps only failure codes", () => {
  const recent = Array.from({ length: 8 }, (_, i) => makeRun({
    ID: `old-${i}`, TaskID: `T-${i}`, State: "merged", Reason: "verbose prose reason",
    title: `Finished ${i}`,
  }))
  const result = lines({
    active: [makeRun({ ID: "run-1", title: "Active" })],
    recent: [makeRun({ ID: "run-1", title: "Duplicate" }), ...recent],
  })
  assert.equal(result[2], "● T-1 Active RUN")
  assert.equal(result[4], `▼ Recent ${RECENT_LIMIT}`)
  assert.equal(result.filter((line) => line.includes("Duplicate")).length, 0)
  assert.equal(result.filter((line) => line.includes("verbose prose")).length, 0)
  assert.equal(result.length, 5 + RECENT_LIMIT * 2)
})

test("recent shows a failure code when the reason is one", () => {
  const result = lines({ active: [], recent: [makeRun({
    ID: "old", State: "blocked", Reason: "ci_timeout", title: "Failed",
  })] })
  assert.ok(result[4].includes("BLOCK"))
  assert.ok(result[5].includes("ci_timeout"))
})

test("elapsed formats seconds, minutes, hours and days", () => {
  assert.equal(elapsed(500), "0s")
  assert.equal(elapsed(90_000), "1m")
  assert.equal(elapsed(3 * 60_000), "3m")
  assert.equal(elapsed(2 * 60 * 60_000 + 5 * 60_000), "2h")
  assert.equal(elapsed(26 * 60 * 60_000), "1d")
  assert.equal(elapsed(-1), "")
})

test("row items carry stable cells and colors by theme key", () => {
  const [head, detail] = runLineItems(normalizeRun(makeRun({ State: "blocked", title: "Blocked work" })), NOW)
  assert.deepEqual(head, {
    marker: "▲", name: "T-1 Blocked work", status: "BLOCK", color: "error", bold: true, runID: "run-1",
  })
  assert.deepEqual(detail, { marker: "", name: "#42 3m", status: "waiting CI", color: "muted", runID: "run-1" })
})

test("a stale transport error keeps the snapshot and clips the marker", () => {
  const snapshot = { active: [makeRun({ title: "Still here" })], recent: [] }
  const result = lines(snapshot, "rpc.unavailable", NOW)
  assert.ok(result.some((line) => line.includes("[stale] rpc.unavailable")))
  assert.ok(result.some((line) => line.includes("Still here")))
  const items = snapshotLineItems(snapshot, "x".repeat(200), NOW)
  assert.ok(items.find((item) => item.name.startsWith("[stale]")).name.length <= 9 + REASON_WIDTH)
})

test("empty snapshot still renders both sections", () => {
  assert.deepEqual(lines(EMPTY_SNAPSHOT), ["Beacon", "▼ Active 0", "No active runs", "▼ Recent 0", "No recent runs"])
})

test("detail lines show identity, metrics, durations, attempts and history sorted by time", () => {
  const run = normalizeRun(makeRun({
    title: "Fix TUI plugin shape",
    Timeline: [
      { at: "2026-11-23T10:06:00Z", kind: "merge_checks", state: "waiting_ci" },
      { at: "2026-11-23T10:01:00Z", kind: "created", state: "running" },
    ],
  }))
  const metrics = { dispatchCount: 25, rebaseCount: 2, fixCount: 1, mergeCount: 3, ciWaitSeconds: 90 }
  const detail = detailLines(run, metrics, NOW)
  const text = detail.map((item) => item.text)
  assert.equal(text[0], "Run detail")
  assert.equal(text[1], "Metrics dispatch 25 · rebase 2 · fix 1 · merge 3 · ci_wait 1m")
  assert.ok(text[2].includes("ID run-1 · task T-1"))
  assert.ok(text[3].includes("● Fix TUI plugin shape"))
  assert.ok(text.some((line) => line.includes("PR #42")))
  assert.ok(text.some((line) => line.includes("Branch beacon/T-1")))
  assert.ok(text.some((line) => line.includes("SHA abcdef12345…")))
  assert.ok(text.some((line) => line.includes("Created 10:00:00 · Updated 10:07:00 · 3m")))
  assert.ok(text.some((line) => line.includes("wait_ci 1m · running 5s · blocked 0s")))
  assert.ok(text.some((line) => line.includes("Attempts 2/3 rebase · 1/3 fix")))
  assert.ok(text.some((line) => line.includes("Reason waiting CI")))
  const timeline = text.filter((line) => line.startsWith("  ") && line.includes("→"))
  assert.deepEqual(timeline, ["  10:01:00 created → running", "  10:06:00 merge_checks → waiting_ci"])
  assert.ok(text.includes("  10:06:00 reconcile.decision merge"))
  assert.equal(detail[0].bold, true)
})

test("detail with no run is explicit", () => {
  assert.deepEqual(detailLines(null), [{ text: "No run selected", color: "muted" }])
})

test("detail command binds ctrl+g, exposes a slash command and opens the panel", () => {
  const opened = []
  const cmd = detailCommand((name) => opened.push(name))
  assert.equal(PANEL_NAME, "beacon.detail")
  assert.equal(cmd.id, "beacon.run.detail")
  assert.equal(cmd.bind, "ctrl+g")
  assert.deepEqual(cmd.slash, { name: "beacon-detail" })
  assert.equal(cmd.palette, true)
  cmd.run()
  assert.deepEqual(opened, [PANEL_NAME])
})

test("readTitle uses frontmatter and falls back to the task ID", () => {
  const files = {
    "/p/docs/tasks/T-1.md": "---\nid: T-1\ntitle: \"Human readable title\"\nstatus: done\n---\n",
    "/p/docs/tasks/T-2.md": "---\nid: T-2\nstatus: done\n---\n",
  }
  const deps = { existsSync: (p) => Object.hasOwn(files, p), readFileSync: (p) => files[p] }
  assert.equal(readTitle("/p", "T-1", deps), "Human readable title")
  assert.equal(readTitle("/p", "T-2", deps), "T-2")
  assert.equal(readTitle("/p", "T-3", deps), "T-3")
  const enriched = enrichRuns("/p", [makeRun({ TaskID: "T-1" }), makeRun({ TaskID: "T-3" })], deps)
  assert.deepEqual(enriched.map((r) => r.title), ["Human readable title", "T-3"])
})

test("parseSnapshot tolerates null, empty and malformed output", () => {
  assert.deepEqual(parseSnapshot("null"), {})
  assert.deepEqual(parseSnapshot(""), {})
  assert.deepEqual(parseSnapshot("not json"), {})
  assert.deepEqual(parseSnapshot(JSON.stringify({ active: [], recent: [] })), { active: [], recent: [] })
})

test("CLI source has a working default execFile when no deps are injected", () => {
  const source = createCliStatusSource({ root: "/p" })
  assert.equal(typeof source.deps.execFile, "function")
})

test("CLI source makes one batched call and keeps numeric metrics", async () => {
  const calls = []
  const execFile = async (program, args) => {
    calls.push([program, ...args])
    return { stdout: JSON.stringify({
      generated_at: "2026-11-23T10:00:00Z",
      active: [makeRun({ TaskID: "T-9" })],
      recent: null,
      metrics: { dispatch_count: 4, skips: { scope_overlap: 2 }, ci_wait_seconds: 90 },
    }) }
  }
  const source = createCliStatusSource({
    root: "/p",
    deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false },
  })
  const snapshot = await source.snapshot()
  assert.equal(snapshot.active.length, 1)
  assert.equal(snapshot.active[0].title, "T-9")
  assert.deepEqual(snapshot.recent, [])
  assert.equal(snapshot.metrics.dispatchCount, 4)
  assert.equal(snapshot.metrics.skips.scope_overlap, 2)
  assert.equal(snapshot.metrics.ciWaitSeconds, 90)
  assert.equal(snapshot.generatedAt, "2026-11-23T10:00:00Z")
  assert.deepEqual(calls, [["/bin/beacon", "status", "--snapshot"]])
})

test("CLI source raises maxBuffer so multi-megabyte snapshots fit", async () => {
  let options
  const execFile = async (program, args, opts) => {
    options = opts
    return { stdout: JSON.stringify({ active: [], recent: [] }) }
  }
  const source = new CliStatusSource({ root: "/p", deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false } })
  await source.snapshot()
  assert.ok(options, "execFile receives an options object")
  assert.ok(Number.isFinite(options.maxBuffer), "maxBuffer is set to a finite number")
  assert.ok(options.maxBuffer >= 4 * 1024 * 1024,
    `maxBuffer ${options.maxBuffer} must hold a multi-megabyte snapshot`)
  assert.equal(options.cwd, "/p")
  assert.equal(options.encoding, "utf8")
})

test("status source uses the RPC when it works and falls back when it throws", async () => {
  let calls = 0
  const ctx = { client: { rpc: () => ({ snapshot: async () => {
    calls += 1
    return { active: [makeRun({ title: "From RPC" })], recent: [] }
  } }), }, location: { project: { canonical: "/p" } } }
  const good = await createStatusSource(ctx, { deps: { command: () => { throw new Error("CLI must not run") } } }).snapshot()
  assert.equal(calls, 1)
  assert.equal(good.active[0].title, "From RPC")

  const cliCalls = []
  const bad = {
    client: { rpc: () => { throw new Error("no rpc") } },
    location: { project: { canonical: "/p" } },
  }
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async (program, args) => {
      cliCalls.push([program, ...args])
      return { stdout: JSON.stringify({ active: [makeRun({ TaskID: "T-7", title: "From CLI" })], recent: [] }) }
    },
    existsSync: () => false,
  }
  const fallback = await createStatusSource(bad, { deps }).snapshot()
  assert.equal(fallback.active[0].title, "From CLI")
  assert.deepEqual(cliCalls, [["/bin/beacon", "status", "--snapshot"]])
})

test("RPC definition validates the snapshot output", () => {
  const method = BeaconStatusRpc.methods.snapshot
  assert.equal(BeaconStatusRpc.id, BEACON_STATUS_RPC_ID)
  assert.ok(method.input["~standard"].validate({}).value !== undefined)
  assert.ok(method.output["~standard"].validate({ active: [], recent: [] }).value !== undefined)
  assert.ok("issues" in method.output["~standard"].validate(null))
  assert.ok("issues" in method.output["~standard"].validate([]))
  assert.ok("issues" in method.output["~standard"].validate({ active: [], recent: [], metrics: { merge_count: "three" } }))
})

test("adding runs does not add CLI calls", async () => {
  const calls = []
  const runs = Array.from({ length: 25 }, (_, i) => makeRun({ ID: `run-${i}`, TaskID: `T-${i}` }))
  const execFile = async (program, args) => { calls.push([program, ...args]); return { stdout: JSON.stringify({ active: runs, recent: [] }) } }
  const source = new CliStatusSource({ root: "/p", deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false } })
  const snapshot = await source.snapshot()
  assert.equal(snapshot.active.length, 25)
  assert.equal(calls.length, 1)
  assert.equal(normalizeSnapshot({ active: runs, recent: [] }).active.length, 25)
})

test("errorMessage handles Error, plain object and string", () => {
  assert.equal(errorMessage(new Error("boom")), "boom")
  assert.equal(errorMessage({ message: "rpc failed" }), "rpc failed")
  assert.equal(errorMessage("down"), "down")
})

test("row mount reuses nodes across refreshes and keeps the click handler", () => {
  let nextNode = 0
  const created = []
  const ui = {
    createElement: (tag) => {
      const node = { id: ++nextNode, tag, props: {}, children: [] }
      created.push(node)
      return node
    },
    insert: (parent, child) => parent.children.push(child),
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const picked = []
  const color = (key) => `theme.${key}`
  const mount = createRowMount(ui, { color, BOLD: 1, onRow: (item) => picked.push(item), hoverBg: "theme.hover" })
  const container = ui.createElement("box")
  let items = snapshotLineItems({ active: [makeRun({ title: "First" })], recent: [] }, null, NOW)
  const apply = mount(container, () => items)
  const afterFirst = created.length
  const boxes = container.children
  const handler = boxes[2].props.onMouseDown

  items = snapshotLineItems({ active: [makeRun({ State: "blocked", title: "Second" })], recent: [] }, null, NOW)
  apply()

  assert.equal(created.length, afterFirst, "refresh must not create nodes")
  assert.equal(container.children[2], boxes[2], "row node identity is stable")
  assert.equal(boxes[2].props.onMouseDown, handler, "click handler survives")
  assert.equal(boxes[2].children[1].props.content, "T-1 Second")
  assert.equal(boxes[2].children[1].props.fg, "theme.base")
  assert.equal(boxes[2].children[0].props.content, "▲")
  assert.equal(boxes[2].children[0].props.fg, "theme.error")
  handler()
  assert.equal(picked.length, 1)
  assert.equal(picked[0].runID, "run-1")

  const beforeHide = container.children.length
  items = snapshotLineItems(EMPTY_SNAPSHOT, null, NOW)
  apply()
  assert.equal(created.length, afterFirst, "hiding rows must not create nodes")
  assert.equal(container.children.length, beforeHide, "rows are hidden, not removed")
  assert.equal(container.children[beforeHide - 1].props.visible, false)
})

test("setup defers every createElement to a slot render callback", async () => {
  const created = []
  const ui = {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push(node)
      return node
    },
    insert: (parent, child) => { parent.children.push(child) },
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const createSignal = (initial) => {
    let value = initial
    return [() => value, (next) => { value = typeof next === "function" ? next(value) : next }]
  }
  const modules = {
    "@opentui/solid": ui,
    "solid-js": { createSignal },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const renderers = Object.create(null)
  const ctx = {
    theme: {
      text: {
        base: "base", muted: "muted", action: { primary: { base: "action" } },
        feedback: {
          error: { base: "error" }, warning: { base: "warning" },
          success: { base: "success" }, info: { base: "info" },
        },
      },
      background: { raised: { high: "hover" } },
    },
    client: { rpc: () => ({ snapshot: async () => ({ active: [], recent: [] }) }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => {
        renderers[append] = render
        return () => { delete renderers[append] }
      },
      panel: { open: () => {} },
    },
    keymap: { layer: () => {} },
  }

  const dispose = await beaconStatusDefinition.setup(ctx, { import: (specifier) => modules[specifier] })
  assert.equal(created.length, 0, "setup must not call createElement before any render callback")
  for (const name of ["sidebar.content", "home.footer.status", "session.panel", "app"]) {
    assert.equal(typeof renderers[name], "function", `registers ${name}`)
  }

  const sidebar = renderers["sidebar.content"]()
  assert.ok(sidebar, "sidebar render returns a node")
  assert.ok(created.length > 0, "first render callback creates the nodes")

  const before = created.length
  assert.equal(renderers["sidebar.content"](), sidebar, "sidebar node is reused")
  assert.equal(renderers["home.footer.status"]() !== sidebar, true, "footer has its own node")
  assert.equal(created.length, before, "repeat renders create no new nodes")

  const detail = renderers["session.panel"]({ name: PANEL_NAME })
  assert.ok(detail, "panel render returns the detail node")
  assert.equal(renderers["session.panel"]({ name: "other" }), null, "other panels render nothing")
  assert.equal(created.length, before, "opening the detail panel creates no new nodes")

  dispose()
  assert.equal(Object.keys(renderers).length, 0, "dispose removes every slot")
})

test("a row count change during refresh creates no nodes outside a render callback", async () => {
  let current = { active: [], recent: [] }
  const created = []
  let renderActive = false
  const ui = {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push({ node, renderActive })
      return node
    },
    insert: (parent, child) => parent.children.push(child),
    setProp: (node, name, value) => { node.props[name] = value },
  }
  const createSignal = (initial) => {
    let value = initial
    return [() => value, (next) => { value = typeof next === "function" ? next(value) : next }]
  }
  const modules = {
    "@opentui/solid": ui,
    "solid-js": { createSignal },
    "@opentui/core": { TextAttributes: { BOLD: 1 } },
  }
  const renderers = Object.create(null)
  let tick = null
  const ctx = {
    theme: {
      text: {
        base: "base", muted: "muted", action: { primary: { base: "action" } },
        feedback: {
          error: { base: "error" }, warning: { base: "warning" },
          success: { base: "success" }, info: { base: "info" },
        },
      },
      background: { raised: { high: "hover" } },
    },
    client: { rpc: () => ({
      snapshot: async () => current,
      events: { on: () => () => {} },
    }) },
    storage: { store: () => [{ id: "" }, async () => {}] },
    ui: {
      slot: ({ append, render }) => {
        renderers[append] = render
        return () => { delete renderers[append] }
      },
      panel: { open: () => {} },
    },
    keymap: { layer: () => {} },
  }
  const deps = {
    import: (specifier) => modules[specifier],
    setInterval: (fn) => { tick = fn; return 1 },
    clearInterval: () => {},
    setTimeout: () => 1,
    clearTimeout: () => {},
  }

  const dispose = await beaconStatusDefinition.setup(ctx, deps)
  assert.equal(created.length, 0, "setup creates nothing before a render callback")
  assert.equal(typeof tick, "function", "the refresh timer is installed")

  renderActive = true
  const sidebar = renderers["sidebar.content"]()
  renderActive = false
  assert.ok(sidebar, "the first render callback returns the sidebar node")
  const afterEnsure = created.length
  assert.ok(afterEnsure > 0, "the first render callback allocates the row pool")
  assert.ok(created.every((call) => call.renderActive), "every node was created inside a render callback")

  const beforeVisible = sidebar.children.filter((child) => child.props.visible === true).length
  current = {
    active: [
      makeRun({ ID: "run-a", TaskID: "T-A", title: "First" }),
      makeRun({ ID: "run-b", TaskID: "T-B", title: "Second" }),
      makeRun({ ID: "run-c", TaskID: "T-C", title: "Third" }),
    ],
    recent: [],
  }
  renderActive = false
  tick()
  await new Promise((resolve) => setImmediate(resolve))

  assert.equal(created.length, afterEnsure, "refresh with more rows must not create nodes")
  assert.ok(created.every((call) => call.renderActive), "no node was created outside a render callback")
  const afterVisible = sidebar.children.filter((child) => child.props.visible === true).length
  assert.ok(afterVisible > beforeVisible, "the panel shows the new rows")

  dispose()
})

test("setup without injected deps keeps the TUI imports literal", async () => {
  const src = readFileSync(new URL("./ui.js", import.meta.url), "utf8")
  for (const specifier of ["@opentui/solid", "solid-js", "@opentui/core"]) {
    assert.ok(src.includes(`import("${specifier}")`), `literal import("${specifier}") is present`)
  }
  assert.ok(src.includes('import("@opencode/plugin/tui")'), "the TUI plugin import stays literal")

  // With no injected deps, setup must reach the runtime imports rather than
  // the guard. The TUI peers are absent under node --test, so a module
  // resolution failure is the only acceptable rejection.
  await assert.rejects(beaconStatusDefinition.setup({}), (error) => {
    assert.equal(/unexpected import/.test(error.message), false)
    assert.match(error.message, /Cannot find package|ERR_MODULE_NOT_FOUND|Cannot find module/)
    return true
  })
})

test("ui.js uses theme tokens and native text styling only", () => {
  const src = readFileSync(new URL("./ui.js", import.meta.url), "utf8")
  assert.equal(/#[0-9a-fA-F]{3,8}\b/.test(src), false, "no hardcoded hex colors")
  assert.equal(/ascii_font/.test(src), false)
  assert.equal(/setProp\([^,]+,\s*"(font|size|width|height)"/.test(src), false)
  for (const token of [
    "theme.text.base", "theme.text.muted", "theme.text.action.primary.base",
    "theme.text.feedback", "theme.background.raised.high",
  ]) assert.ok(src.includes(token), `uses ${token}`)
  assert.ok(src.includes('"wrapMode", "none"'))
  assert.ok(src.includes('"selectable", false'))
  assert.ok(src.includes('"truncate", true'))
  assert.ok(src.includes('"minWidth", 0'))
  assert.ok(src.includes('"onMouseDown"'))
  assert.ok(src.includes('append: "session.panel"'))
})
