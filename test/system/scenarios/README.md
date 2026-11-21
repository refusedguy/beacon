# System scenarios

This directory describes Beacon's system scenarios. A system scenario drives the real CLI end to end against a private daemon and a private tmux server. Stub agents replace the model. A SQLite store holds coordination state. This checks coordination regressions without model cost.

## Current state

The live runner is not built. The harness in `internal/testsystem/harness.go` runs scenarios in-process. It opens no socket and starts no process. It never reads the clock, the environment, or the filesystem. Every value in its result comes from the scenario name, seed, and steps. That makes a trace repeatable and safe to pin as a golden fingerprint. It also means the harness cannot drive a real CLI, daemon, tmux, or SQLite. The stub scenarios it ships are listed below. The live system scenarios are still TODO.

## Scenario format

A scenario has a name, a seed, and an ordered list of commands. The `Scenario` type in `internal/testsystem/harness.go` defines them.

- `Name` identifies the scenario in a registry and in a regression.
- `Seed` is a 64-bit integer that drives the stub random generator. Change the seed and the receipts change, while command kinds and echoed fields stay the same.
- `Steps` is a list of `Command` values. Each command has a `Kind` and a `Fields` map of strings.

The runner turns each step into one `Event`. The event kind is the command kind with a `.done` suffix. The receipt is a seed-derived 64-bit value in hex. The fields echo the command fields plus `seed` and `step`. A `Result` holds the scenario name, seed, and all events in order.

`Result.String` renders the trace in canonical text form. `Fingerprint` hashes that text with SHA-256. A `Regression` pins a seed and a fingerprint with `Record`, then checks them with `Check` or returns the full trace with `Replay`.

## Stub scenarios in the library

`NewLibrary` returns a registry with three ready scenarios. They model watch lifecycles through the stub executor and do not contact GitHub or a provider.

- `watch-lifecycle` registers a watch on PR 73, waits for the result, then acknowledges the obligation.
- `dedupe-delivery` delivers a webhook twice and checks the stub trace stays deterministic.
- `loop-guard` registers a watch on PR 74 and sends two tick commands with a ready verdict.

A test pulls one with `NewLibrary().Get(name)` or replays it with `NewLibrary().Run(name)`. `RunAll` replays every scenario in insertion order.

## Running the scenarios

The harness runs as a Go test. From the repository root:

    go test ./internal/testsystem/...

This runs the determinism, fingerprint, seed regression, and registry tests in `internal/testsystem/harness_test.go`. CI runs the suite on each pull request, so a local pass is not the only signal.

## Adding a stub scenario

1. Add a `Scenario` with a distinct name and seed.
2. List the steps as `Command` values with kinds and fields.
3. Call `Record` once and store the returned fingerprint in a regression test.
4. Call `Check` in the test. A fingerprint change means the harness or the scenario changed.

## Live system scenarios (TODO)

The card calls for scenarios that run the real CLI and daemon. These are not implemented. Each needs a private daemon, a private tmux server, stub agents, and a SQLite store. The verbs are up, down, send, restart, and daemon. The environment must be built from only HOME, PATH, and TERM. The scenario then runs inside tmux without inheriting other variables.

- TODO: bring up a private daemon and tmux server with `up`.
- TODO: send a command to a running session with `send`.
- TODO: restart the daemon and check reconciliation with `restart`.
- TODO: tear the fixture down with `down`.
- TODO: check that the child process sees only HOME, PATH, and TERM.

Until a live runner exists, the stub scenarios pin the harness trace only. Do not claim end-to-end coordination coverage from them.
