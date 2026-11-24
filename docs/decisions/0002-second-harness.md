# 2. Second harness support with ACP first

Status: recorded, implemented by T-20261124-19651f36-01 through -05.

## Why this is here

Beacon's orchestration code is already harness neutral. The concrete OpenCode wiring lives in the composition root. This note records that boundary, the capability ports added to cross it, and the five cards that made ACP the first second harness. It exists so a later adapter author knows where provider-specific code belongs.

## What the investigations found

The findings come from four read-only investigations on main b9f287e0.

- `pkg/core/ports.go` defines `AgentHarness` with `Spawn`, `Wake`, `Stop`, `IsAlive`, `NotifyParent` and `Events`, plus the optional `WorktreeRefresher`. `SpawnRequest` already carries `ParentSessionID` and `ModelRef`.
- `internal/app` imports only `pkg/core`. No OpenCode type or `/api/` path appears there. `arch_test.go` checks import direction but does not check concrete wiring in `cmd`.
- Concrete coupling sits in `cmd/beacon`: `main.go:144` asserts `h.(*opencode.Harness)` to set `Repo`; `main.go:248-249` builds `opencode.New` and assigns `ModelRef`; `resolveModel` at `main.go:261-307` calls `GET /api/session/<parent>`, parses OpenCode JSON and hardcodes `providerID` to `opencode`; `doctor.go:291` imports `opencode.ApplyAuth`. `idleWatcher` in `serve.go` is a local interface that only OpenCode implements.
- `internal/infra/harness` has a CLI harness that implements `AgentHarness` but ignores `ParentSessionID` and `ModelRef`, and leaves `Stop`, `IsAlive` and `NotifyParent` as no-ops.
- The MCP tool catalog is harness neutral. The generated `opencode.json` permissions are host specific.

## Boundary rule

`pkg/core` and `internal/app` stay free of OpenCode types and HTTP paths. Optional ports in `pkg/core` let a harness advertise a capability without forcing every harness to implement it. `cmd/beacon` is the only place that knows a concrete provider, endpoint or token source.

The ports added to `pkg/core` are `RepoSetter`, `ModelResolver` and `IdleWatcher`. They join the existing `WorktreeRefresher`. OpenCode claims all four. ACP claims none: it has no parent channel, no worktree registry and no local model lookup.

`resolveModel` stays in `cmd/beacon` and returns a `core.ModelRef`. It reads `BEACON_MODEL`, then `harness.model`, then the orchestrator session. Dispatch passes the resolved ref in `SpawnRequest`. A harness that can resolve its own model may implement `ModelResolver`; the composition root does not require that port.

## Five child cards

The epic plan created five cards in dependency order. Card 01 comes before card 02, which comes before card 04, which comes before card 05. Card 03 is independent of that chain.

| Card | Title | Depends |
| --- | --- | --- |
| T-20261124-19651f36-01 | Add core capability ports for a second harness | none |
| T-20261124-19651f36-02 | Make OpenCode harness implement the capability ports | -01 |
| T-20261124-19651f36-03 | Move auth handling into a neutral shared helper | none |
| T-20261124-19651f36-04 | Implement a minimal ACP harness module | -01, -02 |
| T-20261124-19651f36-05 | Make harness config provider neutral | -01, -04 |

Card 01 defines the ports in `pkg/core`. Card 02 makes OpenCode implement them and moves the concrete assertions out of `cmd`. Card 03 extracts bearer and basic auth into `internal/infra/shared` so `doctor` and OpenCode share one helper. Card 04 adds the ACP module and wires `harness.provider: acp` in the composition root. Card 05 makes the config loader store provider values without adding an endpoint or provider name of its own, and records the fields in `README.md`.

## What changed

- `pkg/core/ports.go` gains the three optional ports. `pkg/core/ports_test.go` pins the capability assertions.
- `harnesses/opencode` implements `SetRepo`, `ResolveModel`, `WatchIdle` and the existing `RefreshWorktrees`. Its constructor takes a `Config` struct. The CLI-facing HTTP and SSE paths stay inside this module.
- `internal/infra/shared/auth.go` holds `ApplyAuth`. `cmd/beacon/doctor.go` and OpenCode call it instead of each formatting headers.
- `harnesses/acp` launches one agent process per session over stdio and speaks the baseline ACP methods: `initialize`, `session/new`, `session/prompt` and `session/cancel`. It is a separate module in `go.work` and imports only `pkg/core`.
- `internal/infra/config` exposes `harness.provider`. An empty provider keeps the default harness. `cmd/beacon` selects `opencode`, `acp` or a CLI provider from that field.
- `README.md` documents the provider-neutral config block, the field-to-adapter map, and ACP as a second adapter.

## What stays out

`pkg/core` and `internal/app` gain no OpenCode type and no `/api/` path. The MCP tool catalog stays harness neutral. The generated `opencode.json` permissions stay host specific and are not derived from the harness. The CLI harness stays a fallback for providers without a dedicated module. Its no-op lifecycle methods are unchanged by this epic.

This card changes no code. It owns `docs/decisions/0002-second-harness.md` and the `README.md` prose only.

## Code and source pointers

- T-20261124-19651f36-01 through -05, all with status `done` and `base_branch: beacon/T-20261124-19651f36`.
- `pkg/core/ports.go`: `AgentHarness`, `WorktreeRefresher`, `RepoSetter`, `ModelResolver`, `IdleWatcher`.
- `cmd/beacon/main.go`: provider switch, `resolveModel`, `setHarnessRepo`, decision sink wiring.
- `harnesses/opencode/opencode.go`: capability ports and `New(Config)`.
- `internal/infra/shared/auth.go`: `ApplyAuth`.
- `harnesses/acp/acp.go` and `harnesses/acp/rpc.go`: process lifecycle and JSON-RPC framing.
- `internal/infra/config/config.go` and `README.md`: provider-neutral config.
