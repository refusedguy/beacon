// Timing harness for the plugin's queue grouping hot path.
//
//   node docs/perf/plugin_grouping_bench.mjs
//
// It imports the real queueGroups and queueLineItems from the project plugin,
// so the numbers measure production code. The fixture is seeded and fixed and
// the report prints median, range, work counts, error counts, the named limiter
// and the grouping share of a full sidebar render.

import { queueGroups, queueLineItems } from "../../.opencode/plugins/beacon-project/ui/ui_alerts.js"

const SEED = 20261125
const EPICS = 24
const CHILDREN_PER_EPIC = 4
const BLOCKERS = 8
const ITERATIONS = 2000
const WARMUP = 200

function rng(seed) {
  let state = seed >>> 0
  return () => {
    state = (Math.imul(state, 1664525) + 1013904223) >>> 0
    return state / 0x100000000
  }
}

function id(n) {
  return `T-20261125-${String(n).padStart(4, "0")}`
}

function fixture() {
  const next = rng(SEED)
  const queue = []
  for (let i = 0; i < BLOCKERS; i++) {
    queue.push({ id: id(i), title: `Blocker ${i}`, priority: 95, status: "blocked", dependsOn: [] })
  }
  for (let e = 0; e < EPICS; e++) {
    const epicID = id(e + 100)
    queue.push({
      id: epicID,
      title: `Epic ${e}`,
      priority: 90 - Math.floor(next() * 20),
      status: "available",
      kind: "epic",
      dependsOn: [id(e % BLOCKERS)],
    })
    for (let c = 0; c < CHILDREN_PER_EPIC; c++) {
      queue.push({
        id: `${epicID}-${String(c + 1).padStart(2, "0")}`,
        title: `Child ${c} of epic ${e}`,
        priority: 80 - Math.floor(next() * 5),
        status: "waiting_deps",
        dependsOn: [],
      })
    }
  }
  return queue
}

// Both functions are measured in the same iteration so a load spike affects
// them together and the grouping share stays below the full render.
function measureBoth(groupFn, lineFn, iterations) {
  const groupDurations = new Float64Array(iterations)
  const lineDurations = new Float64Array(iterations)
  let groupErrors = 0
  let lineErrors = 0
  for (let i = 0; i < iterations; i++) {
    const start = performance.now()
    try {
      groupFn()
    } catch {
      groupErrors++
    }
    const middle = performance.now()
    try {
      lineFn()
    } catch {
      lineErrors++
    }
    const end = performance.now()
    groupDurations[i] = middle - start
    lineDurations[i] = end - middle
  }
  return { groupDurations, lineDurations, groupErrors, lineErrors }
}

function median(sorted) {
  return sorted[Math.floor(sorted.length / 2)]
}

function row(name, durations, errors, work, share, limiter) {
  const sorted = Float64Array.from(durations).sort()
  return {
    name,
    median: median(sorted),
    range: sorted[sorted.length - 1] - sorted[0],
    work,
    errors,
    share,
    limiter,
  }
}

const queue = fixture()
const grouped = queueGroups(queue)
const groupWork = queue.length + grouped.groups.size + grouped.parents.size + grouped.blockers.size

for (let i = 0; i < WARMUP; i++) {
  queueGroups(queue)
  queueLineItems(queue, null, null)
}

const measured = measureBoth(
  () => queueGroups(queue),
  () => queueLineItems(queue, null, null),
  ITERATIONS,
)
const groupMedian = median(Float64Array.from(measured.groupDurations).sort())
const lineMedian = median(Float64Array.from(measured.lineDurations).sort())
const rows = [
  row("queueGroups", measured.groupDurations, measured.groupErrors, groupWork,
    (100 * groupMedian) / lineMedian, "Map/Set allocation"),
  row("queueLineItems", measured.lineDurations, measured.lineErrors,
    queueLineItems(queue, null, null).length, 100, "row building + Map allocation"),
]

console.log(
  `plugin grouping: seed=${SEED} epics=${EPICS} children=${CHILDREN_PER_EPIC} blockers=${BLOCKERS} iterations=${ITERATIONS}`,
)
console.log("hot path         median(ms)  range(ms)   work   errs  share  limiter")
for (const r of rows) {
  console.log(
    `${r.name.padEnd(16)} ${r.median.toFixed(4).padStart(10)} ${r.range.toFixed(4).padStart(10)} ${String(r.work).padStart(6)} ${String(r.errors).padStart(6)} ${r.share.toFixed(1).padStart(5)}%  ${r.limiter}`,
  )
}
console.log(`end-to-end median: ${lineMedian.toFixed(4)}ms (queueLineItems, which calls queueGroups)`)
