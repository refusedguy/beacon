// Tests for the Beacon status source contract and sidebar view model.
// Run: node --test .opencode/plugins/beacon-project/ui_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  BEACON_STATUS_RPC_ID,
  BeaconStatusRpc,
  CliStatusSource,
  EMPTY_SNAPSHOT,
  enrichRuns,
  errorMessage,
  parseRunList,
  readTitle,
  renderSnapshotLines,
} from "./ui.js"

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

test("renders active and recent runs with state, ID, title, PR, SHA, attempts, reason and age", () => {
  const snapshot = {
    active: [makeRun({ title: "Render Beacon status in OpenCode sidebar panel" })],
    recent: [
      makeRun({
        ID: "run-0",
        TaskID: "T-0",
        State: "merged",
        PRNumber: 40,
        HeadSHA: "deadbeeffeedface",
        RebaseAttempts: 0,
        FixAttempts: 0,
        MaxAttempts: 3,
        Reason: "",
        UpdatedAt: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
        title: "Expose recent finished runs",
      }),
    ],
  }
  const lines = renderSnapshotLines(snapshot)
  const text = lines.join("\n")
  assert.match(text, /Beacon/)
  assert.match(text, /running  T-1  Render Beacon status in OpenCode sidebar panel/)
  assert.match(text, /PR #42/)
  assert.match(text, /abcdef1/)
  assert.match(text, /attempts max 3 rebase 2 fix 1/)
  assert.match(text, /reason: waiting for CI/)
  assert.match(text, /age 3m/)
  assert.match(text, /Recent/)
  assert.match(text, /merged  T-0  Expose recent finished runs/)
  assert.match(text, /age 2h/)
  assert.ok(lines.filter((line) => line.startsWith("─")).length >= 1, "separator present")
})

test("renders empty active and recent sections", () => {
  const text = renderSnapshotLines(EMPTY_SNAPSHOT).join("\n")
  assert.match(text, /No active runs/)
  assert.match(text, /No recent runs/)
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

test("falls back to the task ID when the source entry has no title", () => {
  const lines = renderSnapshotLines({ active: [makeRun({ title: "" })], recent: [] })
  assert.match(lines.join("\n"), /running  T-1  T-1/)
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
