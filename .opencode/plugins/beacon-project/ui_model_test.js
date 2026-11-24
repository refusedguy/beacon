// Run: node --test .opencode/plugins/beacon-project/ui_model_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { NOW, lines, makeRun, solidModule, themeStub, signalStub } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  BEACON_STATUS_RPC_ID,
  BeaconStatusRpc,
  CliStatusSource,
  DECISION_LIMIT,
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
  default: beaconStatus,
} = ui

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
