// Run with the plugin test loader:
// node --import ./test_support/register_plugin_doubles.js --test run_watcher_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { createRunChangeWatcher, snapshotFingerprint } from "./run_watcher.js"
import { normalizeSnapshot } from "./ui/ui.js"

const run = (id, state = "running", sha = "abc123") => ({
  id, task_id: `T-${id}`, state, head_sha: sha, updated_at: "2026-01-01T00:00:00Z",
})

test("snapshotFingerprint compares raw snapshots by value", () => {
  assert.equal(snapshotFingerprint({ active: [] }), snapshotFingerprint({ active: [] }))
  assert.notEqual(snapshotFingerprint({ active: [] }), snapshotFingerprint({ active: [run("r")] }))
  assert.equal(snapshotFingerprint(undefined), snapshotFingerprint(null))
})

test("the identity normalizer leaves a CliStatusSource snapshot untouched", async () => {
  // CliStatusSource.snapshot() returns an already-normalized object. The
  // operator watcher passes an identity normalizer so it is not rebuilt again.
  const normalized = normalizeSnapshot({ active: [run("run-1")], recent: [] })
  const seen = []
  const watcher = createRunChangeWatcher({ snapshot: async () => normalized }, {
    emit: () => {},
    deps: { normalizeSnapshot: (value) => { seen.push(value); return value } },
  })
  await watcher.tick()
  assert.equal(seen.length, 1)
  assert.equal(seen[0], normalized, "the watcher reuses the source object")
  await watcher.tick()
  assert.equal(seen.length, 1, "an unchanged normalized snapshot is not normalized twice")
})

test("1000 idle ticks normalize once and emit no extra events", async () => {
  const raw = { active: [run("run-1")], recent: [] }
  let snapshots = 0
  let normalizations = 0
  const source = { snapshot: async () => { snapshots++; return raw } }
  const emitted = []
  const watcher = createRunChangeWatcher(source, {
    emit: (change) => { emitted.push(change) },
    deps: { normalizeSnapshot: (value) => { normalizations++; return normalizeSnapshot(value) } },
  })
  await watcher.tick()
  assert.equal(normalizations, 1, "the first tick establishes the baseline")
  assert.deepEqual(emitted, [])
  for (let i = 0; i < 1000; i++) await watcher.tick()
  assert.equal(normalizations, 1, "unchanged ticks skip normalization")
  assert.equal(snapshots, 1001, "each tick still fetches a fresh snapshot")
  assert.deepEqual(emitted, [])
})

test("a changed snapshot normalizes again and emits only the changed run", async () => {
  let raw = { active: [run("run-1")], recent: [] }
  let normalizations = 0
  const emitted = []
  const watcher = createRunChangeWatcher({ snapshot: async () => raw }, {
    emit: (change) => { emitted.push(change) },
    deps: { normalizeSnapshot: (value) => { normalizations++; return normalizeSnapshot(value) } },
  })
  await watcher.tick()
  assert.equal(normalizations, 1)
  assert.deepEqual(emitted, [])
  raw = { active: [run("run-1", "done", "def456")], recent: [] }
  await watcher.tick()
  assert.equal(normalizations, 2)
  assert.equal(emitted.length, 1)
  assert.equal(emitted[0].id, "run-1")
  assert.equal(emitted[0].state, "done")
  assert.equal(emitted[0].sha, "def456")
  await watcher.tick()
  assert.equal(normalizations, 2, "the new snapshot is fingerprinted once")
  assert.equal(emitted.length, 1)
})

test("skipping unchanged ticks emits the same events as normalizing every tick", async () => {
  const raws = [
    { active: [run("a")], recent: [] },
    { active: [run("a")], recent: [] },
    { active: [run("a", "done")], recent: [] },
    { active: [run("a", "done")], recent: [] },
    { active: [run("a", "done"), run("b")], recent: [] },
  ]
  let n = 0
  const drive = (deps) => {
    const emitted = []
    let i = 0
    const watcher = createRunChangeWatcher({ snapshot: async () => raws[i++] }, {
      emit: (change) => { emitted.push(change) },
      deps,
    })
    return { emitted, run: async () => { for (let tick = 0; tick < raws.length; tick++) await watcher.tick() } }
  }
  const skipped = drive({})
  const always = drive({ fingerprint: () => String(n++) })
  await skipped.run()
  await always.run()
  assert.deepEqual(skipped.emitted, always.emitted)
  assert.equal(skipped.emitted.length, 2)
})

test("the watcher still honours a custom interval and stops", () => {
  const timers = []
  const watcher = createRunChangeWatcher({ snapshot: async () => ({ active: [], recent: [] }) }, {
    emit: () => {},
    deps: {
      intervalMs: 2500,
      setInterval: (fn, ms) => { timers.push(ms); return 1 },
      clearInterval: () => {},
    },
  })
  watcher.start()
  assert.deepEqual(timers, [2500])
  assert.equal(watcher.running, true)
  watcher.stop()
  assert.equal(watcher.running, false)
})
