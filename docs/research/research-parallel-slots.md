# Parallel slots: what Beacon actually counts and where the races are

Date: 2026-11-22.

Scope: read-only research on `origin/main` at `2d34645b` (40 commits ahead of the local `main` checkout at `ac4eb527`). Five later commits on `origin/main`, up to `b3bfd639`, change docs only. No code changed. Eight cards `SL-01` through `SL-08` are added under `docs/tasks/`.

## Method

1. Read the sources named in the request: `internal/app/orchestrator/decision.go`, `cmd/beacon/mcp.go`, `internal/app/orchestrator/claim.go`, `internal/infra/mcp/tool_task_claim.go`, `internal/app/taskcli/service.go`, `internal/domain/orchestrator.go`, and the workload command and app files.
2. Checked the two external references with `gh pr view 1659` and `gh issue view 1439` against `refusedguy/beacon`.
3. Measured live state with `beacon workload --format json` via `go run ./cmd/beacon workload --format json` against the live state dir, and cross-checked the `watches` table with read-only `sqlite3`.
4. Counted production callers of the slot and claim APIs with `git grep` over `origin/main`, so test-only definitions are not mistaken for runtime enforcement.
5. No `go test`, `go build`, or `beacon gates` was run locally. CI owns those checks on the PR.

Sources are cited as `path:line` at `2d34645b` unless stated otherwise.

## Measurement: Beacon holds zero slots today

`beacon workload --format json` against `/home/nbook/.local/state/beacon/projects/beacon-impl-998e8a8ae2a6d282` returned:

```json
{"total":0,"ready":0,"needsAttention":0,"watching":0,"registered":0,"byVerdict":{},"byRole":{},"byProject":{},"byAttention":{}}
```

A read-only query of the same database returned 51 rows in `watches`, all with `status = closed`. `ActiveWatches` selects only `watching` and `notifying` (`internal/infra/store/db.go:637-644`), and `LiveWatches` adds `blocked` (`internal/infra/store/db.go:646-654`). Both sets are empty. `RunWorkload` enrolls an orchestrator subscriber only for watches it read, so the report also added no subscription (`registered: 0`) (`internal/app/orchestrator/workload.go:189-223`).

The number of subagents the owner keeps busy is not visible in this report. The report counts watches, not OpenCode sessions or task claims. `Workload` has fields for counts and groups, but no field for capacity or limit (`internal/app/orchestrator/workload.go:59-75`), and `renderWorkloadJSON` prints that struct unchanged (`cmd/beacon/workload.go:173-175`).

### The configured limit is 4, not 12-17, and it is not enforced

Three separate numbers look like a slot limit but do not form one budget:

- `domain.DefaultSlotsTotal = 4` (`internal/domain/orchestrator.go:13-17`). `reconcileAutoSpawn` hardcodes it as `slotsTotal` (`cmd/beacon/mcp.go:2064-2066`).
- `defaultMaxConcurrentAgents = 4` in registry config (`internal/infra/registry/limits.go:17`). Nothing outside config parsing reads `MaxConcurrentAgents`.
- `Orchestrator.SlotsTotal` and `SlotsUsed` with `CanAcceptTask` and `Register` returning `LIMIT_EXCEEDED` (`internal/domain/orchestrator.go:80-108`). `NewOrchestrator` has no production caller. Every call is in `internal/domain/orchestrator_test.go`.

`taskcli.AdmitLaunch` does reject a launch when `slotsTotal <= 0 || slotsUsed >= slotsTotal` (`internal/app/taskcli/service.go:644-659`), but its only production caller passes the hardcoded 4 and `len(LiveWatches)`. With zero live watches, the gate sees zero used slots and allows a launch even when many subagents are already running outside Beacon.

Conclusion: there is no runtime cap for 12-17 agents. There is a per-pass cap of one spawn in `reconcileAutoSpawn`, and a hardcoded budget of 4 measured in watches. The domain budget that could express 12-17 is dead code at runtime.

## What the SlotLedger actually guards

`SlotLedger` is a wake deduplication ledger, not a concurrency limiter.

- Only `report_to_orchestrator` opens a slot (`internal/app/orchestrator/decision.go:510-515`, `:600-622`).
- A token key is `watchID@revision`, so two signals from one Watch revision cannot open two slots (`:530-545`).
- `Burn` frees the token only after a verifier proves the artifact, a PR or `PLAN.md`, exists. A missing artifact or a verifier error keeps the slot held (`:624-647`).
- The ledger is an in-memory map with no global count and no maximum (`:588-598`). It cannot say "12 slots used, stop".

The daemon owns the wiring. `daemonArtifactVerifier` checks a PR in the Watch or `PLAN.md` for the linked card (`cmd/beacon/daemon.go:238-273`). `daemonWakeGate` issues tokens under a mutex, stores them, and burns them in a later pass (`:275-350`). PR 1439 (AUTOSP-07) added that burn path and owns `cmd/beacon/daemon.go` and its test. PR 1659 (AUTOSP-04) added auto-spawn to `cmd/beacon/mcp.go` and its test. The two mechanisms do not share a budget: auto-spawn never reads a `SlotToken` and never calls `Issue` or `Burn`.

## How auto-spawn picks work

`reconcileAutoSpawn` runs once per `ReconcilePass` when the MCP config has a spawner (`cmd/beacon/mcp.go:2162-2167`). The cycle interval is adaptive 10s or 30s (`cmd/beacon/mcp.go:1643-1645`). Per pass it:

1. Reads `LiveWatches` and sets `slotsUsed = len(watches)`, `slotsTotal = domain.DefaultSlotsTotal` (`:2055-2066`).
2. Builds the set of `in_progress` cards with `OwnerFiles` (`:2075-2085`).
3. Takes the first `todo` card with `OwnerFiles` that does not intersect those active files (`:2086-2107`).
4. Runs `AdmitLaunch` with the live quota check and the active plus candidate assignments (`:2109-2122`).
5. Calls `sp.start`, which creates an OpenCode session and sends `spawnPrompt(cardID, title)` (`:2123-2126`, `:2129-2155`).

`spawnPrompt` says "Выполни задачу Beacon <ID>: <title>" (`cmd/beacon/spawn.go:134-142`). It names the card but does not tell the child to call `beacon_task_claim` with that `cardID`, and it does not tell the child which card was reserved for it.

The PR body for 1659 states the scope limit directly: terminal-signal gating by `report_to_orchestrator` and the red-main gate are not part of that change. The auto-spawn loop therefore starts agents on every pass where a free watch slot and a candidate card exist, without a `SlotToken`.

## Claim mechanism and the gaps behind the accidental claims

`beacon_task_claim` takes `agentID` as required and `cardID` as optional (`internal/infra/mcp/tool_task_claim.go:37-43`). `FileTaskClaimStore.Claim` holds one flock over the card directory (`:252-267`), then chooses a card:

- With `cardID`, it looks up that exact card (`:274-285`).
- Without `cardID`, it filters by status `todo`, phase, and handoff validity, then takes the first candidate by ID. With a graph it sorts by phase rank and ID, then skips cards with cycles or unresolved dependencies (`:286-326`).

That selection is deterministic and has no knowledge of the card named in the child's spawn metadata. `autoSpawnStartSession` writes `spawnMetaTaskID` into session metadata (`cmd/beacon/mcp.go:2138`), but the claim path never reads it. An auto-started agent that calls `beacon_task_claim` with only its `agentID` gets the first available card by ID, not the card it was spawned for. This is the mechanism behind the reported accidental captures of ATX-04, ATX-25, ATX-30, and ATX-41 in this session. The working tree of the main checkout shows those four cards modified alongside other work, which is consistent with the report but is not itself proof of intent.

Three more gaps follow from the same code:

- No `OwnerFiles` check at claim time. `AdmitLaunch` checks intersections before spawn (`internal/app/taskcli/service.go:644-659`), but a claim made later can pick a different card whose files overlap another agent's card. `checkAdmissionConflicts` also skips a conflict when both assignments are already `in_progress` (`:661-691`), so an existing overlap is tolerated rather than reported.
- One agent can hold many cards. `CardIDForAgent` explicitly returns the most recently claimed card "when an agent holds several" (`internal/infra/mcp/tool_task_claim.go:385-409`). There is no guard in `Claim` against a second in_progress card for the same `agentID`. A card can look busy in the board while no fresh work happens on it, and its slot token stays live until an artifact appears.
- `OwnerFiles` overlap is checked against in-progress cards at spawn, but not between two `todo` candidates in one launch plan. `CheckLaunch` and `PlanLaunch` do compare each pair (`internal/app/taskcli/service.go:597-613`, `:693-727`), and the orchestration rules require calling them before a wave. `reconcileAutoSpawn` calls only `AdmitLaunch`, which checks the one candidate against active cards.

### Concrete race: duplicate spawn for one card

The card is still `todo` after `autoSpawnStartSession` returns. The child writes `in_progress` only when it claims. In the 10s or 30s gap, the next reconcile pass reads the same `todo` card, sees the same zero or low watch count, and starts a second agent for the same card. Both children can then call `beacon_task_claim` with the same `cardID`. The flock makes the status swap atomic, so one of them wins, but the second session is already running and may claim another card by ID if its prompt did not pin one. The same pattern repeats with any number of passes.

### Concrete race: two agents, overlapping files

Two passes pick cards A and B whose `OwnerFiles` do not overlap any current in-progress card, but do overlap each other. Each pass checks only its own candidate against active cards. If both children claim without `cardID`, they may also receive a third and fourth card. No later check repairs the split.

## Where a check or hint can go

| Gap | Location | Change to make |
|---|---|---|
| Slot budget is 4 and measured in watches | `cmd/beacon/mcp.go:2064-2066`, `internal/app/orchestrator/workload.go:59-75` | Read the configured budget, count live claims as well as watches, and expose `slotsTotal` and `slotsUsed` in the workload report. |
| Spawn does not reserve the card | `cmd/beacon/mcp.go:2086-2127`, `internal/infra/mcp/tool_task_claim.go:231-356` | Mark the card in progress inside the same step that selects it, before creating the session. Pass the reserved cardID to the child. |
| Child prompt does not pin the claim | `cmd/beacon/spawn.go:134-142` | Add an explicit line "call beacon_task_claim with cardID=<id>" and reject a claim without cardID for sessions that carry `spawnMetaTaskID`. |
| No file overlap at claim | `internal/infra/mcp/tool_task_claim.go:269-335` | Reuse `taskcli.CheckLaunch` over the candidate and all in-progress cards before the status swap. |
| One agent holds many cards | `internal/infra/mcp/tool_task_claim.go:238-335`, `:385-409` | Reject a second in_progress card for the same agent, or require an explicit release. Make `CardIDForAgent` report the conflict instead of picking a winner. |
| Auto-spawn ignores terminal signal and red main | `cmd/beacon/mcp.go:2162-2167`, `internal/app/orchestrator/decision.go:600-622` | Require a live slot token from `report_to_orchestrator` and a green required check on main before calling the spawner. |
| Auto-spawn ignores task source priority | `cmd/beacon/mcp.go:2068-2107` | Try `beacon deferred --format json` first, then the board and graph, then owner questions, as the orchestration rules require. |

The claim store and the auto-spawner are owned by different cards in the current wave, so each change needs its own owner files. The cards below keep those boundaries.

## Limitation of this research

The numbers above describe one point in time and one state dir. A different project root or `BEACON_STATE_DIR` gives a different workload report. The claim selections were read from code and from the owner's report; no agent was spawned and no card was claimed during this research.

## Cards

| Card | Subject |
|---|---|
| SL-01 | Make the slot budget configurable up to 17 and visible in workload |
| SL-02 | Count real agents as slots, not only watches |
| SL-03 | Reserve the card atomically before spawn |
| SL-04 | Pin cardID in the child prompt and reject unpinned claims |
| SL-05 | Check OwnerFiles overlap for every launch assignment |
| SL-06 | Release the slot when a task is closed or claim is dropped |
| SL-07 | Reject unpinned claims and OwnerFiles overlap at claim time |
| SL-08 | Measure 12, 14 and 17 parallel slots end to end |
