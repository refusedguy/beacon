# Allocation case study: sidebar normalization

The source study said `sidebarModel` normalizes one snapshot five or more times but gave no count or bytes. This note measures the count and the time, then records the change that removes the redundant calls.

## Verdict

A single `sidebarModel` call normalized its snapshot 8 times before the change and 1 time after. Median time for 2000 renders fell from 920.21 ms to 169.66 ms, about 5.4x. The remaining time is one `normalizeSnapshot` call at the public entry point. That call is 83.5% to 84.4% of the after median across runs. The rendering step is about 3% of the before median and cannot be removed by this change.

## Method

Run `node internal/bench/ui_bench.js` from the repository root. The harness mirrors the `ui_view.js` call graph so it can count calls without adding instrumentation to production files, the same convention as the Go mirrors in `internal/bench/models.go`. A counter wraps the production `normalizeSnapshot`. A third side calls the production `renderSnapshotLines` to confirm the fix landed in `ui_view.js`.

The fixture carries 3 active runs, 2 recent runs and 5 queue cards in raw wire shape. Normalizing it converts every run, timeline event, decision, check and queue card. The active flags render all sections. Each side runs 5 alternating rounds of 2000 renders, so warmup and load drift favor neither side. `performance.now()` times each round and a per-round sink counts the items produced, which proves the work ran inside the timed region.

The reported capture ran on 8 cores at load average 2.98 4.32 4.43. A repeat under load average 3.87 4.31 4.42 gave a before median of 1586.75 ms and an after median of 247.99 ms. The gap is stable; absolute times move with load.

## Measured

| Side | Normalize calls per render | Median for 2000 renders | Range |
| --- | --- | --- | --- |
| Before | 8 | 920.21 ms | 888.53 to 1191.66 ms |
| After | 1 | 169.66 ms | 132.31 to 179.12 ms |
| Render only | 0 | 26.53 ms | 19.02 to 37.06 ms |
| Production after the fix | not counted in this side | 160.28 ms | 152.50 to 187.86 ms |

Production tracked the before mirror at 1384.86 ms before the fix and tracks the after mirror at 160.28 ms after it. The harness asserts that the before lines, the after lines and the production lines are identical on the fixture, 17 lines each. Every round reported a non-zero work sink. No side reported an error.

## Code before

Each public helper normalized its own input, including inputs that were already normalized views.

```js
export function activeRuns(snapshot = EMPTY_SNAPSHOT) {
  const seen = new Set()
  return normalizeSnapshot(snapshot).active.filter(/* dedupe */)
}
export function recentRuns(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const activeTasks = new Set(activeRuns(view).map(/* task IDs */))
  // filter view.recent
}
export function stateBarItem(snapshot = EMPTY_SNAPSHOT) {
  const view = normalizeSnapshot(snapshot)
  const active = activeRuns(view)
  const recent = recentRuns(view)
  // build segments, legend and summary
}
export function sidebarModel(snapshot = EMPTY_SNAPSHOT, ...rest) {
  const view = normalizeSnapshot(snapshot)
  const bar = stateBarItem(view)
  const active = activeRuns(view)
  const recent = recentRuns(view)
  // build items
}
```

The call graph is 1 call at `sidebarModel`, 4 inside `stateBarItem`, 1 for `activeRuns` and 2 for `recentRuns`. Total: 8 normalizations of one snapshot.

## Code after

The public entry points normalize once. Internal helpers work on the resulting view.

```js
function activeFromView(view) { /* dedupe view.active */ }
export function activeRuns(snapshot = EMPTY_SNAPSHOT) {
  return activeFromView(normalizeSnapshot(snapshot))
}
function recentFromViewWith(view, active) { /* filter view.recent */ }
function recentFromView(view) { return recentFromViewWith(view, activeFromView(view)) }
export function recentRuns(snapshot = EMPTY_SNAPSHOT) {
  return recentFromView(normalizeSnapshot(snapshot))
}
function stateBarFromView(view, active = activeFromView(view), recent = recentFromView(view)) {
  // build segments, legend and summary
}
export function stateBarItem(snapshot = EMPTY_SNAPSHOT) {
  return stateBarFromView(normalizeSnapshot(snapshot))
}
export function sidebarModel(snapshot = EMPTY_SNAPSHOT, ...rest) {
  const view = normalizeSnapshot(snapshot)
  const active = activeFromView(view)
  const recent = recentFromView(view)
  const bar = stateBarFromView(view, active, recent)
  // build items
}
```

`sidebarModel` now normalizes once and passes the view down. `stateBarItem`, `activeRuns` and `recentRuns` still normalize once each when called directly, so their public contracts hold.

## Limiter

The limiter is the single `normalizeSnapshot` call that remains at the public entry point. Render-only work measured 26.53 ms median, which is 2.9% of the before median. The after median is 169.66 ms, so the one remaining normalization accounts for about 143 ms, or 84.4% of the after median. Removing seven of eight calls saved 81.6% of the before median. The change cannot go faster than one normalization without changing the contract that each public entry point accepts a raw snapshot.

In the panel, `createStatusSource` already normalizes the snapshot before `sidebarModel` sees it, so that one call is redundant for the panel. It stays because `activeRuns`, `recentRuns`, `stateBarItem` and `sidebarModel` are exported and must stay safe as standalone entry points. No wire shape or unrelated UI behavior changed.
