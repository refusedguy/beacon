# beacon init

`beacon init` prepares a Git repository for Beacon. It installs git hooks,
writes the `.beacon` templates and checks that the MCP server answers
`tools/list`. One invocation replaces the manual setup steps.

## Usage

Run the command in the project you want to prepare:

```
beacon init
beacon init --root /path/to/project
beacon init --force
```

`--root` names the target directory. Without it the command uses the current
directory. `--force` overwrites files from a previous run. Without `--force`
the command keeps existing files and prints `skip` for each one, so a second
run is safe.

## Files

The command writes four files and never touches anything else in the target.

| File | Purpose |
| --- | --- |
| `.githooks/pre-commit` | Blocks a commit on `main` or `master`, rejects unformatted staged Go files |
| `.githooks/pre-push` | Blocks a direct push to `main` or `master` |
| `.beacon/config.yaml` | Project settings: `repo`, `maxRebases`, `maxWakesPerWatch`, `slotsTotal`, `flaky` |
| `.beacon/flaky.json` | Empty flaky check list and a retry budget of two |

The `repo` key comes from the `origin` remote. When detection finds no slug,
the line stays commented and you fill it in.

The command then sets `core.hooksPath` to `.githooks` with `git config`. The
hooks become active for that working tree only.

## MCP smoke check

After writing the files, the command starts a Beacon MCP server in process and
connects a client over an in-memory transport. It lists tools and checks that
all names in `ToolNames` are present. A connection error or a missing tool
exits with code 1. No network access or running daemon is needed, so the check
works on a fresh machine.

A successful run prints one line per file, the `core.hooksPath` line and a
final line with the tool count:

```
created .githooks/pre-commit
created .githooks/pre-push
created .beacon/config.yaml
created .beacon/flaky.json
git: core.hooksPath = .githooks
MCP smoke: ok, tools/list вернул 8 инструментов
```

## Pilot on a neighboring repository

The full pilot needs write access to the neighboring repository. Ask the owner
for permission before running it against a remote. Do not run it on a shared
repository without that permission. The local smoke check above is the part
that runs without it.

With permission granted, run the pilot in a local clone:

1. Clone the target repository and create a feature branch.
2. Run `beacon init --root <clone>` from the Beacon checkout.
3. Open a PR from that branch and watch it with `beacon_watch_pr` in the
   `executor` role.
4. Confirm that the long checks finish and that jobs marked `SKIPPED` do not
   block the verdict.

The pilot card Q-D-02 records the outcome. This runbook documents the steps;
the pilot itself is not part of the init change.

## Limits

Dispatch for `beacon init` lives in `cmd/beacon/main.go` and the catalog entry
in `cmd/beacon/catalog.go`. Both files are outside the OwnerFiles of Q-D-02,
so this change adds `RegisterInit` as the wiring seam but does not add the
dispatch case. The CLI test also pins the catalog to a fixed set of commands,
so adding an entry needs a matching test update in the same change. Until that
wiring lands, call the runner through `RegisterInit` or go through a wiring
task.
