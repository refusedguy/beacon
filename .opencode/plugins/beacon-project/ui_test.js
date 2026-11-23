// Tests for the Beacon status source contract and sidebar view model.
// Run: node --test .opencode/plugins/beacon-project/ui_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import beaconStatus, {
  BEACON_STATUS_RPC_ID,
  BeaconStatusRpc,
  CliStatusSource,
  EMPTY_SNAPSHOT,
  PANEL_WIDTH,
  beaconStatusDefinition,
  createStatusSource,
  defineBeaconStatusPlugin,
  enrichRuns,
  errorMessage,
  parseRunList,
  readTitle,
  renderSnapshotLines,
  snapshotLineItems,
} from "./ui.js"

const RULE = "─".repeat(PANEL_WIDTH)

function makeRun(overrides = {}) {
  return {
    ID: "run-1",
    TaskID: "T-1",
    State: "running",
    PRNumber: 42,
    HeadSHA: "abcdef1234567890",
    RebaseAttempts: 2,
    FixAttempts: 1,
    MaxAttempts: 3,
    Reason: "waiting for CI",
    UpdatedAt: new Date(Date.now() - 3 * 60 * 1000).toISOString(),
    ...overrides,
  }
}

test("default export is a Plugin.define definition", () => {
  let received = null
  const result = defineBeaconStatusPlugin({
    define: (definition) => {
      received = definition
      return definition
    },
  })
  assert.equal(received, result)
  assert.equal(received, beaconStatusDefinition)
  assert.equal(beaconStatus.id, "beacon-project-status")
  assert.equal(beaconStatusDefinition.id, "beacon-project-status")
  assert.equal(typeof beaconStatusDefinition.setup, "function")
  assert.equal(beaconStatus.setup, beaconStatusDefinition.setup)
})

test("renders exact lines for a long title", () => {
  const snapshot = {
    active: [makeRun({ title: "Fix TUI plugin shape so Beacon panel renders in the OpenCode sidebar" })],
    recent: [],
  }
  assert.deepEqual(renderSnapshotLines(snapshot), [
    "Beacon",
    RULE,
    "● T-1 Fix TUI plugin shape so Beaco…",
    "  PR #42  waiting for CI",
    RULE,
    "Recent",
    "No recent runs",
  ])
})

test("renders exact lines for a long reason", () => {
  const snapshot = {
    active: [makeRun({
      State: "waiting_ci",
      Reason: "PR checks failed on linux because the generated fixture file was not committed to the branch after rebase",
      title: "Short title",
    })],
    recent: [],
  }
  assert.deepEqual(renderSnapshotLines(snapshot), [
    "Beacon",
    RULE,
    "◐ T-1 Short title",
    "  PR #42  PR checks failed on linux bec…",
    RULE,
    "Recent",
    "No recent runs",
  ])
})

test("renders exact lines for an empty snapshot", () => {
  assert.deepEqual(renderSnapshotLines(EMPTY_SNAPSHOT), [
    "Beacon",
    RULE,
    "No active runs",
    RULE,
    "Recent",
    "No recent runs",
  ])
})

test("renders exact lines for a blocked run with a shortened task ID", () => {
  const snapshot = {
    active: [makeRun({
      State: "blocked",
      TaskID: "T-20261123-613986e5",
      PRNumber: 7,
      Reason: "needs owner decision",
      title: "Fix TUI plugin shape",
    })],
    recent: [],
  }
  assert.deepEqual(renderSnapshotLines(snapshot), [
    "Beacon",
    RULE,
    "▲ 613986e5 Fix TUI plugin shape",
    "  PR #7  needs owner decision",
    RULE,
    "Recent",
    "No recent runs",
  ])
})

test("renders both active and recent sections with a separator", () => {
  const snapshot = {
    active: [makeRun({ title: "Active work" })],
    recent: [makeRun({ ID: "run-0", TaskID: "T-0", State: "merged", PRNumber: 40, Reason: "", title: "Finished work" })],
  }
  assert.deepEqual(renderSnapshotLines(snapshot), [
    "Beacon",
    RULE,
    "● T-1 Active work",
    "  PR #42  waiting for CI",
    RULE,
    "Recent",
    "✔ T-0 Finished work",
    "  PR #40",
  ])
})

test("colors the state marker line and the error line", () => {
  const snapshot = { active: [makeRun({ State: "blocked", title: "Blocked work" })], recent: [] }
  const items = snapshotLineItems(snapshot)
  assert.deepEqual(items[2], { text: "▲ T-1 Blocked work", fg: "#f87171" })
  assert.deepEqual(snapshotLineItems(EMPTY_SNAPSHOT, "rpc.unavailable"), [
    { text: "Beacon status unavailable: rpc.unavailable", fg: "#f87171" },
  ])
})

test("renders an error state without touching run data", () => {
  assert.deepEqual(
    renderSnapshotLines(EMPTY_SNAPSHOT, "rpc.unavailable"),
    ["Beacon status unavailable: rpc.unavailable"],
  )
})

test("reads task titles from frontmatter and falls back to the task ID", () => {
  const files = {
    "/p/docs/tasks/T-1.md": "---\nid: T-1\ntitle: \"Human readable title\"\nstatus: done\n---\n",
    "/p/docs/tasks/T-2.md": "---\nid: T-2\nstatus: done\n---\n",
  }
  const deps = {
    existsSync: (path) => Object.hasOwn(files, path),
    readFileSync: (path) => files[path],
  }

  assert.equal(readTitle("/p", "T-1", deps), "Human readable title")
  assert.equal(readTitle("/p", "T-2", deps), "T-2", "frontmatter without title falls back")
  assert.equal(readTitle("/p", "T-3", deps), "T-3", "missing card falls back")

  const enriched = enrichRuns(
    "/p",
    [makeRun({ TaskID: "T-1" }), makeRun({ TaskID: "T-2" }), makeRun({ TaskID: "T-3" })],
    deps,
  )
  assert.deepEqual(enriched.map((run) => run.title), ["Human readable title", "T-2", "T-3"])
})

test("does not repeat the task ID when no title is available", () => {
  const lines = renderSnapshotLines({ active: [makeRun({ title: "" })], recent: [] })
  assert.equal(lines[2], "● T-1")
  assert.equal(lines.filter((line) => line.includes("T-1")).length, 1)
})

test("parses null, empty and malformed CLI output as an empty list", () => {
  assert.deepEqual(parseRunList("null"), [])
  assert.deepEqual(parseRunList(""), [])
  assert.deepEqual(parseRunList("not json"), [])
  assert.equal(parseRunList(JSON.stringify([makeRun()])).length, 1)
})

test("CLI source spawns status and status --recent and tolerates null", async () => {
  const calls = []
  const execFile = async (program, args) => {
    calls.push([program, ...args])
    const recent = args.includes("--recent")
    return { stdout: recent ? "null" : JSON.stringify([makeRun({ TaskID: "T-9" })]) }
  }
  const source = new CliStatusSource({
    root: "/p",
    deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false },
  })

  const snapshot = await source.snapshot()
  assert.equal(snapshot.active.length, 1)
  assert.equal(snapshot.active[0].title, "T-9", "missing card falls back to the task ID")
  assert.deepEqual(snapshot.recent, [])
  assert.deepEqual(calls, [
    ["/bin/beacon", "status"],
    ["/bin/beacon", "status", "--recent"],
  ])
})

test("status source uses the RPC when it works", async () => {
  let calls = 0
  const ctx = {
    client: {
      rpc: () => ({
        snapshot: async () => {
          calls += 1
          return { active: [makeRun({ title: "From RPC" })], recent: [] }
        },
      }),
    },
    location: { project: { canonical: "/p" } },
  }
  const deps = {
    command: () => {
      throw new Error("CLI must not spawn when the RPC works")
    },
  }
  const snapshot = await createStatusSource(ctx, { deps }).snapshot()
  assert.equal(calls, 1)
  assert.equal(snapshot.active[0].title, "From RPC")
})

test("status source falls back to the CLI when the RPC throws", async () => {
  const calls = []
  const ctx = {
    client: {
      rpc: () => {
        throw new Error("rpc not registered")
      },
    },
    location: { project: { canonical: "/p" } },
  }
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async (program, args) => {
      calls.push([program, ...args])
      return { stdout: JSON.stringify([makeRun({ TaskID: "T-7", title: "From CLI" })]) }
    },
    existsSync: () => false,
  }
  const snapshot = await createStatusSource(ctx, { deps }).snapshot()
  assert.equal(snapshot.active[0].title, "From CLI")
  assert.deepEqual(calls, [
    ["/bin/beacon", "status"],
    ["/bin/beacon", "status", "--recent"],
  ])
})

test("status source uses the CLI when the client has no rpc method", async () => {
  const ctx = { client: {}, location: { project: { canonical: "/p" } } }
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async () => ({ stdout: "null" }),
    existsSync: () => false,
  }
  const snapshot = await createStatusSource(ctx, { deps }).snapshot()
  assert.deepEqual(snapshot, { active: [], recent: [] })
})

test("RPC definition exposes a validated snapshot method", () => {
  const method = BeaconStatusRpc.methods.snapshot
  assert.equal(BeaconStatusRpc.id, BEACON_STATUS_RPC_ID)
  assert.equal(method.input["~standard"].validate({}).value !== undefined, true)
  assert.equal(method.output["~standard"].validate({ active: [], recent: [] }).value !== undefined, true)
  assert.equal("issues" in method.output["~standard"].validate(null), true)
})

test("errorMessage handles Error, plain object and string", () => {
  assert.equal(errorMessage(new Error("boom")), "boom")
  assert.equal(errorMessage({ message: "rpc failed" }), "rpc failed")
  assert.equal(errorMessage("down"), "down")
})
