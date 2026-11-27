// Counts fingerprint characters over 1001 ticks on a realistic snapshot:
// 20 active runs, each carrying 40 timeline events, 40 decisions and 8 checks,
// plus 30 queue rows. The arms differ only in how they fingerprint that payload.
import assert from "node:assert/strict"
import { performance } from "node:perf_hooks"
import { createRunChangeWatcher, snapshotFingerprint } from "../../.opencode/plugins/beacon-project/run_watcher.js"
import { joinKey, normalizeSnapshot } from "../../.opencode/plugins/beacon-project/ui/ui_model.js"
const TICKS = +(process.env.BENCH_TICKS ?? 1001), ROUNDS = +(process.env.BENCH_ROUNDS ?? 5)
// The moving arm has its own canonical key and its own expected normalization
// count. It stays opt-in because the PR advisory reads the base registry, which
// does not list the key; printing the row in every pass would make the whole
// comparison inconclusive instead of just the stable deciding row.
const MOVING = process.env.BENCH_MOVING_ARM === "1", MOVING_KEY = "run-watcher-fp-moving"
const ACTIVE = 20, TIMELINE = 40, DECISIONS = 40, CHECKS = 8, QUEUE = 30
// CACHE names the policy the measurement ran under; an unset variable is
// reported as "unset" so every canonical row carries the field.
const CACHE = process.env.BEACON_BENCH_CACHE || "unset"
const stamp = (i) => `2026-11-27T10:${String(Math.floor(i / 60)).padStart(2, "0")}:${String(i % 60).padStart(2, "0")}Z`
const timeline = (id) => Array.from({ length: TIMELINE }, (_, i) => ({
  at: stamp(i), kind: i % 3 === 0 ? "rebase" : "checks_passed", state: "waiting_ci",
  sha: `sha-${id}-${i}`, task_id: `T-${id}`,
}))
const decisions = (id) => Array.from({ length: DECISIONS }, (_, i) => ({
  at: stamp(i), kind: "transition.rebase", verdict: "approve", action: "rebased",
  task_id: `T-${id}`, fields: { attempt: i + 1, new_sha: `sha-${id}-${i}` },
}))
const checks = (id) => Array.from({ length: CHECKS }, (_, i) => ({
  name: `check-${i}`, state: "success", duration_seconds: i + 1, head_sha: `sha-${id}-${i}`,
}))
// Build each run's static payload once. Every tick still returns a fresh run
// object so the watcher sees the same states and timestamps, but CI does not
// spend its measurement budget rebuilding 1760 nested objects per tick.
const fixtures = new Map()
const run = (id, state) => {
  let base = fixtures.get(id)
  if (!base) {
    base = {
      id, task_id: `T-${id}`, head_sha: "abcdef0123456789",
      updated_at: "2026-11-27T10:00:00Z",
      durations: { waiting_ci_seconds: 12, running_seconds: 3 },
      timeline: timeline(id), decisions: decisions(id), checks: checks(id),
    }
    fixtures.set(id, base)
  }
  return { ...base, state }
}
const shared = {
  queue: Array.from({ length: QUEUE }, (_, i) => ({
    id: `T-${i}`, title: `Card ${i}`, kind: "task", priority: i, status: "ready", depends_on: [],
  })),
  metrics: { dispatch_count: 25 }, daemon: { PID: 42 }, base_incident: { ID: "INC-1" },
  generated_at: "2026-11-27T11:00:00Z",
}
const snapAt = (t) => ({
  ...shared,
  active: Array.from({ length: ACTIVE }, (_, i) => run(`run-${i}`, i === 0 && t >= 400 ? "waiting_ci" : "running")),
  recent: t > 700 ? [run("run-100", "merged")] : [],
})
const movingAt = (t) => ({ ...snapAt(t), generated_at: stamp(t) })
const full = (raw) => JSON.stringify(raw ?? null)
const identity = (raw) => JSON.stringify([...(raw?.active ?? []), ...(raw?.recent ?? [])].map((r) => joinKey(r.id, r.head_sha, r.state, r.updated_at)))
async function drive(fn, snapshot = snapAt) {
  let chars = 0, snapshots = 0, norm = 0, emissions = 0, errs = 0
  const emitted = [], original = JSON.stringify
  JSON.stringify = (...a) => { const out = original(...a); if (typeof out === "string") chars += out.length; return out }
  try {
    let t = 0
    const w = createRunChangeWatcher({ snapshot: async () => { snapshots++; return snapshot(t++) } }, {
      emit: (c) => { emissions++; emitted.push(c) },
      deps: { fingerprint: fn, normalizeSnapshot: (v) => { norm++; return normalizeSnapshot(v) } },
    })
    for (let i = 0; i < TICKS; i += 1) await w.tick()
  } catch { errs += 1 } finally { JSON.stringify = original }
  return { chars, snapshots, norm, emissions, errs, rendered: emitted.map((c) => JSON.stringify(c)) }
}
const sides = { before: full, after: identity, production: snapshotFingerprint }
const samples = { before: [], after: [], production: [] }
for (let round = 0; round < ROUNDS; round += 1) {
  for (const s of (round % 2 ? ["production", "after", "before"] : ["before", "after", "production"])) {
    const start = performance.now(), r = await drive(sides[s])
    samples[s].push({ ...r, ms: performance.now() - start })
  }
}
assert.deepEqual(samples.before[0].rendered, samples.after[0].rendered)
assert.deepEqual(samples.after[0].rendered, samples.production[0].rendered)
for (const k of ["snapshots", "norm", "emissions", "errs"]) for (const s of ["after", "production"]) assert.equal(samples.before[0][k], samples[s][0][k])
const median = (xs) => { const a = [...xs].sort((x, y) => x - y), m = Math.floor(a.length / 2); return a.length % 2 ? a[m] : (a[m - 1] + a[m]) / 2 }
const summary = (s, collection = samples) => {
  const ss = collection[s], ms = ss.map((x) => x.ms), cs = ss.map((x) => x.chars).sort((x, y) => x - y)
  return { ms: median(ms), min: Math.min(...ms), max: Math.max(...ms), chars: median(cs), cmin: cs[0], cmax: cs[cs.length - 1], snapshots: ss[0].snapshots, norm: ss[0].norm, emissions: ss[0].emissions, errs: ss.reduce((n, x) => n + x.errs, 0) }
}
const p = summary("production")
console.log(`run-watcher-fp: median ${p.ms.toFixed(2)} ms, range ${p.min.toFixed(2)} to ${p.max.toFixed(2)} ms`)
console.log(`run-watcher-fp-chars: ${p.chars} characters, range ${p.cmin} to ${p.cmax}`)
console.log(`run-watcher-fp-work: snapshots=${p.snapshots} normalizations=${p.norm} emissions=${p.emissions}`)
console.log(`run-watcher-fp-errs: ${p.errs} errors`)
if (MOVING) {
  const moving = { before: [], after: [], production: [] }
  for (let round = 0; round < ROUNDS; round += 1) {
    for (const s of (round % 2 ? ["production", "after", "before"] : ["before", "after", "production"])) {
      const start = performance.now(), r = await drive(sides[s], movingAt)
      moving[s].push({ ...r, ms: performance.now() - start })
    }
  }
  assert.deepEqual(moving.before[0].rendered, moving.after[0].rendered)
  assert.deepEqual(moving.after[0].rendered, moving.production[0].rendered)
  // A moving generated_at changes the key on every tick, so every tick
  // normalizes. This count belongs to the moving arm alone; it is not part of
  // the stable arm's equal-work assertion above.
  for (const s of ["before", "after", "production"]) assert.equal(moving[s][0].norm, TICKS)
  for (const k of ["snapshots", "emissions", "errs"]) for (const s of ["after", "production"]) assert.equal(moving.before[0][k], moving[s][0][k])
  const m = summary("production", moving)
  console.log(`${MOVING_KEY}: median ${m.ms.toFixed(2)} ms, normalizations=${m.norm}, errors=${m.errs}`)
  console.log(`beacon-perf: ${JSON.stringify({
    key: MOVING_KEY,
    median_ms: m.ms,
    min_ms: m.min,
    max_ms: m.max,
    work: m.snapshots + m.norm + m.emissions,
    errs: m.errs,
    limiter: "generated_at timestamp churn",
    cache: CACHE,
  })}`)
}
// The canonical row uses one fixed field set. The character and counter
// details stay on the human lines above for the existing advisory report.
console.log(`beacon-perf: ${JSON.stringify({
  key: "run-watcher-fp",
  median_ms: p.ms,
  min_ms: p.min,
  max_ms: p.max,
  work: p.snapshots + p.norm + p.emissions,
  errs: p.errs,
  limiter: "JSON.stringify character volume",
  cache: CACHE,
})}`)
