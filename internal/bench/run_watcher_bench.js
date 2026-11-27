// Counts fingerprint characters over 1001 ticks; before is fixed, production per-tree.
import assert from "node:assert/strict"
import { performance } from "node:perf_hooks"
import { createRunChangeWatcher, snapshotFingerprint } from "../../.opencode/plugins/beacon-project/run_watcher.js"
import { joinKey, normalizeSnapshot } from "../../.opencode/plugins/beacon-project/ui/ui_model.js"
const TICKS = +(process.env.BENCH_TICKS ?? 1001), ROUNDS = +(process.env.BENCH_ROUNDS ?? 5)
const run = (id, state) => ({ id, task_id: `T-${id}`, state, head_sha: "abcdef0123456789", updated_at: "2026-11-27T10:00:00Z", detail: "x".repeat(240) })
const shared = { queue: [], metrics: { dispatch_count: 25 }, daemon: { PID: 42 }, base_incident: { ID: "INC-1" } }
const snapAt = (t) => ({ ...shared, active: [run("run-1", t < 400 ? "running" : "waiting_ci"), run("run-2", "running")], recent: t > 700 ? [run("run-4", "merged")] : [] })
const full = (raw) => JSON.stringify(raw ?? null)
const identity = (raw) => JSON.stringify([...(raw?.active ?? []), ...(raw?.recent ?? [])].map((r) => joinKey(r.id, r.head_sha, r.state, r.updated_at)))
async function drive(fn) {
  let chars = 0, snapshots = 0, norm = 0, emissions = 0, errs = 0
  const emitted = [], original = JSON.stringify
  JSON.stringify = (...a) => { const out = original(...a); if (typeof out === "string") chars += out.length; return out }
  try {
    let t = 0
    const w = createRunChangeWatcher({ snapshot: async () => { snapshots++; return snapAt(t++) } }, {
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
const summary = (s) => {
  const ss = samples[s], ms = ss.map((x) => x.ms), cs = ss.map((x) => x.chars).sort((x, y) => x - y)
  return { ms: median(ms), min: Math.min(...ms), max: Math.max(...ms), chars: median(cs), cmin: cs[0], cmax: cs[cs.length - 1], snapshots: ss[0].snapshots, norm: ss[0].norm, emissions: ss[0].emissions, errs: ss.reduce((n, x) => n + x.errs, 0) }
}
const p = summary("production")
console.log(`run-watcher-fp: median ${p.ms.toFixed(2)} ms, range ${p.min.toFixed(2)} to ${p.max.toFixed(2)} ms`)
console.log(`run-watcher-fp-chars: ${p.chars} characters, range ${p.cmin} to ${p.cmax}`)
console.log(`run-watcher-fp-work: snapshots=${p.snapshots} normalizations=${p.norm} emissions=${p.emissions}`)
console.log(`run-watcher-fp-errs: ${p.errs} errors`)
