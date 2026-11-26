// Run: node --test .opencode/plugins/beacon-project/ui_model_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { NOW, lines, makeRun } from "./ui_test_helpers.js"
import * as ui from "./ui.js"
const {
  BeaconStatusRpc, CliStatusSource, EMPTY_SNAPSHOT, RECENT_LIMIT,
  cleanErrorMessage, createCliStatusSource, createStatusSource,
  defineBeaconStatus, errorMessage, normalizeRun, normalizeSnapshot,
  parseSnapshot, renderSnapshotLines, default: beaconStatus,
  beaconStatusDefinition,
} = ui

test("default export is a Plugin.define definition", () => {
  let received = null
  const result = defineBeaconStatus((definition) => { received = definition; return definition })
  assert.equal(received, result)
  assert.equal(received, beaconStatusDefinition)
  assert.equal(beaconStatus.id, "beacon-project-status")
  assert.equal(beaconStatusDefinition.id, "beacon-project-status")
  assert.equal(typeof beaconStatusDefinition.setup, "function")
  assert.equal(beaconStatus.setup, beaconStatusDefinition.setup)
})

test("normalizeRun keeps a missing title empty and reads CI checks", () => {
  const run = normalizeRun(makeRun({
    title: "",
    Checks: [{ name: "ci", state: "success", duration_seconds: 28 }],
  }))
  assert.equal(run.title, "")
  assert.equal(run.state, "running")
  assert.equal(run.durations.waitingCI, 60)
  assert.deepEqual(run.checks, [{ name: "ci", state: "success", durationSeconds: 28 }])
  assert.equal(normalizeRun({ State: "" }).state, "unknown")
})

test("sidebar renders a flat header, state bar legend and two lines per run", () => {
  const result = lines({
    active: [makeRun({ State: "blocked", Reason: "workspace: conflict: refusing to remove dirty worktree at /tmp/wt" })],
    recent: [],
  }, null, NOW)
  assert.equal(result[0], "Beacon 1 blocked")
  assert.equal(result[1], "P0 R0 C0 E0 F0 B1 M0 X0")
  assert.equal(result[2], "▼ Active 1")
  assert.equal(result[3], "▲ T-1 BLK")
  assert.equal(result[4], "#42 3m dirty worktree")
  assert.equal(result[5], "Queue no tasks ready")
  assert.equal(result.filter((line) => /queue|ready/i.test(line)).length, 1, "an empty queue renders one line")
  assert.equal(result[6], "▼ Recent 0")
  assert.equal(result[7], "No recent runs")
})

test("a long full title is not cut to a fixed width", () => {
  const title = "Fix TUI plugin shape so Beacon panel renders in the OpenCode sidebar"
  const result = lines({ active: [makeRun({ title })], recent: [] }, null, NOW)
  assert.ok(result.some((line) => line.includes(title)), "the whole title is present")
})

test("a task with no title shows only its short ID once", () => {
  const result = lines({ active: [makeRun({ title: "" })], recent: [] }, null, NOW)
  assert.equal(result[3], "● T-1 RUN")
  assert.equal(result.filter((line) => line.includes("T-1")).length, 1)
})

test("recent dedupes by task, hides active tasks and caps at five", () => {
  const recent = Array.from({ length: 8 }, (_, i) => makeRun({
    ID: `old-${i}`, TaskID: `T-${i}`, State: "closed", Reason: "verbose prose reason", title: `Finished ${i}`,
  }))
  const result = lines({
    active: [makeRun({ ID: "active-1", TaskID: "T-ACTIVE", title: "Active" })],
    recent: [
      makeRun({ ID: "old-active", TaskID: "T-ACTIVE", State: "closed", Reason: "stale attempt" }),
      ...recent,
    ],
  }, null, NOW)
  assert.equal(result.filter((line) => line.includes("T-ACTIVE")).length, 0, "the old attempt is hidden")
  assert.equal(result.filter((line) => line.includes("Finished")).length, RECENT_LIMIT)
  assert.equal(result.filter((line) => line.includes("stale attempt")).length, 0)
  assert.equal(result.filter((line) => line.includes("verbose prose")).length, RECENT_LIMIT)
})

test("a merged run drops the reason and shows the real merge total", () => {
  const result = lines({
    active: [],
    recent: [makeRun({ ID: "r2", State: "merged", Reason: "automatic rebase" })],
    metrics: { merge_count: 37 },
  }, null, NOW)
  assert.ok(result.some((line) => line.includes("M37")), "the legend uses metrics.mergeCount")
  assert.equal(result.filter((line) => line.includes("automatic rebase")).length, 0)
  assert.ok(result.some((line) => line.includes("✔ T-1 OK")), "recent rows use the calm OK badge")
  assert.ok(result.some((line) => line.includes("#42")))
})

test("readTitle uses frontmatter and falls back to the task ID", () => {
  const files = {
    "/p/docs/tasks/T-1.md": "---\nid: T-1\ntitle: \"Human readable title\"\nstatus: done\n---\n",
    "/p/docs/tasks/T-2.md": "---\nid: T-2\nstatus: done\n---\n",
  }
  const deps = { existsSync: (p) => Object.hasOwn(files, p), readFileSync: (p) => files[p] }
  assert.equal(ui.readTitle("/p", "T-1", deps), "Human readable title")
  assert.equal(ui.readTitle("/p", "T-2", deps), "T-2")
  assert.equal(ui.readTitle("/p", "T-3", deps), "T-3")
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
  assert.ok(Number.isFinite(options.maxBuffer), "maxBuffer is finite")
  assert.ok(options.maxBuffer >= 4 * 1024 * 1024)
  assert.equal(options.cwd, "/p")
  assert.equal(options.encoding, "utf8")
})

test("CLI control sends the action, run ID, optional SHA and --json", async () => {
  const calls = []
  const execFile = async (program, args) => {
    calls.push([program, ...args])
    return { stdout: '{"ok":true,"action":"approve","run_id":"run-1"}' }
  }
  const source = new CliStatusSource({ root: "/p", deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false } })
  const result = await source.control({ action: "approve", runID: "run-1", sha: "abc123" })
  assert.deepEqual(calls, [["/bin/beacon", "control", "--action", "approve", "--run-id", "run-1", "--json", "--sha", "abc123"]])
  assert.equal(result.ok, true)
  await source.control({ action: "retry", runID: "run-2" })
  assert.equal(calls[1].includes("--sha"), false)
})

test("CLI control forwards --message for a nudge", async () => {
  const calls = []
  const execFile = async (program, args) => {
    calls.push([program, ...args])
    return { stdout: '{"ok":true,"action":"nudge"}' }
  }
  const source = new CliStatusSource({ root: "/p", deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false } })
  const result = await source.control({ action: "nudge", runID: "run-1", message: "check the base branch" })
  assert.deepEqual(calls, [["/bin/beacon", "control", "--action", "nudge", "--run-id", "run-1", "--json", "--message", "check the base branch"]])
  assert.equal(result.ok, true)
  await source.control({ action: "retry", runID: "run-2" })
  assert.equal(calls[1].includes("--message"), false)
})

test("normalizeSnapshot keeps a non-null queue and reads the new run and incident fields", () => {
  const old = normalizeSnapshot({ active: [makeRun()], recent: [] })
  assert.deepEqual(old.queue, [])
  assert.equal(old.daemon, undefined)
  assert.equal(old.baseIncident, undefined)

  const snap = normalizeSnapshot({
    active: [makeRun({
      SessionID: "ses-1", IsFixer: true, BlockedIncidentID: "inc-1",
      PRURL: "https://github.com/o/r/pull/42",
    })],
    recent: [],
    queue: [
      { id: "T-2", title: "Ready task", priority: 9, status: "ready" },
      { id: "T-3", title: "Blocked task", priority: 2, status: "waiting_deps", depends_on: ["T-2"] },
    ],
    base_incident: {
      id: "inc-1", base_branch: "main", failed_checks: ["ci", "lint"],
      fixer_run_id: "run-1", attempts: 2,
    },
    daemon: { state: "dead", pid: 0, heartbeat_age_seconds: 91, error: "heartbeat lost" },
  })
  assert.equal(snap.active[0].sessionID, "ses-1")
  assert.equal(snap.active[0].isFixer, true)
  assert.equal(snap.active[0].blockedIncidentID, "inc-1")
  assert.equal(snap.active[0].prURL, "https://github.com/o/r/pull/42")
  assert.equal(snap.queue.length, 2)
  assert.deepEqual(snap.queue[1].dependsOn, ["T-2"])
  assert.equal(snap.baseIncident.failedChecks, 2)
  assert.equal(snap.baseIncident.fixerRunID, "run-1")
  assert.equal(snap.daemon.state, "dead")
  assert.equal(snap.daemon.heartbeatAgeSeconds, 91)
  assert.equal(snap.daemon.error, "heartbeat lost")
  assert.equal(normalizeSnapshot(null).queue.length, 0)
  assert.equal(normalizeSnapshot({ base_incident: { failed_checks: '["ci","lint"]' } }).baseIncident.failedChecks, 2)
})

test("CLI dispatch runs beacon dispatch 1 through the same resolver", async () => {
  const calls = []
  const execFile = async (program, args) => {
    calls.push([program, ...args])
    return { stdout: '{"started":1,"task_ids":["T-1"]}' }
  }
  const source = new CliStatusSource({ root: "/p", deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false } })
  const result = await source.dispatch({ n: 1 })
  assert.deepEqual(calls, [["/bin/beacon", "dispatch", "1"]])
  assert.equal(result.started, 1)
})

test("status source reads snapshot from CLI and keeps RPC for control and dispatch", async () => {
  const calls = []
  const rpc = {
    snapshot: async () => { throw new Error("stale RPC") },
    control: async (input) => { calls.push(["control", input]); return { ok: true } },
    dispatch: async (input) => { calls.push(["dispatch", input]); return { started: 1 } },
  }
  const ctx = { client: { rpc: () => rpc }, location: { project: { canonical: "/p" } } }
  const cliCalls = []
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async (program, args) => {
      cliCalls.push([program, ...args])
      if (args[0] === "status") {
        return { stdout: JSON.stringify({ active: [makeRun({ TaskID: "T-7", title: "From CLI" })], recent: [] }) }
      }
      return { stdout: args[0] === "dispatch" ? '{"started":1}' : '{"ok":true}' }
    },
    existsSync: () => false,
  }
  const good = createStatusSource(ctx, { deps })
  assert.equal((await good.snapshot()).active[0].title, "From CLI")
  assert.deepEqual(await good.control({ action: "retry", runID: "run-1" }), { ok: true })
  assert.deepEqual(await good.dispatch({ n: 1 }), { started: 1 })
  assert.deepEqual(calls, [["control", { action: "retry", runID: "run-1" }], ["dispatch", { n: 1 }]])
  assert.deepEqual(cliCalls, [["/bin/beacon", "status", "--snapshot"]])

  const throwing = { control: async () => { throw new Error("no rpc") }, dispatch: async () => { throw new Error("no rpc") } }
  const bad = createStatusSource({ client: { rpc: () => throwing }, location: { project: { canonical: "/p" } } }, { deps })
  assert.deepEqual(await bad.control({ action: "retry", runID: "run-2" }), { ok: true })
  assert.deepEqual(await bad.dispatch({ n: 2 }), { started: 1 })
  assert.deepEqual(cliCalls.slice(1), [
    ["/bin/beacon", "control", "--action", "retry", "--run-id", "run-2", "--json"],
    ["/bin/beacon", "dispatch", "2"],
  ])
})

test("an unverified root keeps every queue card instead of filtering them all", async () => {
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async () => ({ stdout: JSON.stringify({ active: [], recent: [], queue: [{ id: "T-A" }, { id: "T-B" }] }) }),
    existsSync: () => false,
  }
  const source = createStatusSource({}, { deps })
  const snap = await source.snapshot()
  assert.equal(source.verified(), false)
  assert.deepEqual(snap.queue.map((q) => q.id), ["T-A", "T-B"])
})

test("a late location verifies the root and drops only missing or done cards", async () => {
  const root = "/work/project"
  const files = {
    [`${root}/docs/tasks/T-A.md`]: "---\ntitle: Alpha\nstatus: todo\n---\n",
    [`${root}/docs/tasks/T-B.md`]: "---\ntitle: Beta\nstatus: done\n---\n",
  }
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async () => ({ stdout: JSON.stringify({
      active: [], recent: [], queue: [{ id: "T-A" }, { id: "T-B" }, { id: "T-GONE" }],
    }) }),
    existsSync: (p) => Object.hasOwn(files, p), readFileSync: (p) => files[p],
  }
  const ctx = {}
  const source = createStatusSource(ctx, { deps })
  assert.equal((await source.snapshot()).queue.length, 3, "no verified root keeps all cards")
  ctx.location = { project: { canonical: root } }
  const snap = await source.snapshot()
  assert.equal(source.verified(), true)
  assert.equal(source.root(), root)
  assert.deepEqual(snap.queue.map((q) => q.id), ["T-A"])
})

test("a stale RPC empty queue cannot hide CLI ready cards in the panel", async () => {
  const root = "/work/project"
  const files = {
    [`${root}/docs/tasks/T-1.md`]: "---\nid: T-1\ntitle: Alpha\nstatus: todo\n---\n",
    [`${root}/docs/tasks/T-2.md`]: "---\nid: T-2\ntitle: Beta\nstatus: todo\n---\n",
  }
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async () => ({ stdout: JSON.stringify({
      active: [], recent: [],
      queue: [
        { id: "T-1", title: "Alpha", status: "ready", priority: 2 },
        { id: "T-2", title: "Beta", status: "ready", priority: 1 },
      ],
    }) }),
    existsSync: (p) => Object.hasOwn(files, p), readFileSync: (p) => files[p],
  }
  let rpcSnapshots = 0
  const rpc = { snapshot: async () => { rpcSnapshots++; return { active: [], recent: [], queue: [] } } }
  const source = createStatusSource({ client: { rpc: () => rpc }, location: { project: { canonical: root } } }, { deps })
  const snap = await source.snapshot()
  assert.equal(rpcSnapshots, 0, "the stale RPC snapshot is not consulted")
  assert.deepEqual(snap.queue.map((q) => q.id), ["T-1", "T-2"])
  const rendered = renderSnapshotLines(snap)
  assert.ok(rendered.includes("▶ Queue 2"), "the queue header counts the ready cards")
  assert.ok(rendered.includes("▶ Dispatch (2 ready)"), "the Dispatch plate shows the ready count")
  assert.equal(rendered.includes("Queue no tasks ready"), false)
})

test("RPC definition validates snapshot, control and dispatch shapes", () => {
  const { snapshot, control, dispatch } = BeaconStatusRpc.methods
  assert.equal(BeaconStatusRpc.id, "beacon.project.status")
  assert.ok(snapshot.input["~standard"].validate({}).value !== undefined)
  assert.ok(snapshot.output["~standard"].validate({ active: [], recent: [] }).value !== undefined)
  assert.ok("issues" in snapshot.output["~standard"].validate(null))
  assert.ok("issues" in snapshot.output["~standard"].validate({ active: [], recent: [], metrics: { merge_count: "three" } }))
  assert.ok(control.input["~standard"].validate({ action: "retry", runID: "run-1" }).value !== undefined)
  assert.ok("issues" in control.input["~standard"].validate({ action: "nope", runID: "run-1" }))
  assert.ok("issues" in control.input["~standard"].validate({ action: "retry" }))
  assert.ok("issues" in control.input["~standard"].validate({ action: "retry", runID: "run-1", sha: 4 }))
  assert.ok(control.input["~standard"].validate({ action: "nudge", runID: "run-1", message: "go" }).value !== undefined)
  assert.ok("issues" in control.input["~standard"].validate({ action: "nudge", runID: "run-1" }))
  assert.ok("issues" in control.input["~standard"].validate({ action: "retry", runID: "run-1", message: 4 }))
  assert.ok(control.output["~standard"].validate({ ok: true }).value !== undefined)
  assert.ok(dispatch.input["~standard"].validate({ n: 1 }).value !== undefined)
  assert.ok("issues" in dispatch.input["~standard"].validate({ n: 0 }))
  assert.ok(dispatch.output["~standard"].validate({ started: 1 }).value !== undefined)
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

test("cleanErrorMessage turns a Cobra help dump into one short label", () => {
  const cause = new Error("\u001b[31mError: unknown flag: --snapshot\u001b[0m\nUsage:\n  beacon status [flags]\n  --help  show help\n  at /src/main.go:42:1")
  const clean = cleanErrorMessage(cause)
  assert.equal(clean, "beacon status temporarily unavailable")
  assert.equal(clean.includes("--help"), false)
  assert.equal(/[\r\n]/.test(clean), false)
  assert.equal(cleanErrorMessage('{"error":"boom"}'), "beacon status temporarily unavailable")
})

test("cleanErrorMessage keeps readable text, strips escapes and caps length", () => {
  assert.equal(cleanErrorMessage("daemon   unavailable\n again"), "daemon unavailable again")
  assert.equal(cleanErrorMessage("\u001b[31mred\u001b[0m  error"), "red error")
  assert.equal(cleanErrorMessage("x".repeat(300)).length, 120)
})

test("empty snapshot renders one collapsed queue line", () => {
  assert.deepEqual(renderSnapshotLines(EMPTY_SNAPSHOT), [
    "Beacon 0 active", "P0 R0 C0 E0 F0 B0 M0 X0", "▼ Active 0", "No active runs",
    "Queue no tasks ready", "▼ Recent 0", "No recent runs",
  ])
})
