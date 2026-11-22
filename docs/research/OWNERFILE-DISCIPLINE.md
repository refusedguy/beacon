# OwnerFiles discipline audit, open PR wave 1435-1448

Date: 2026-11-22.

## Method

1. Listed open PRs with `gh pr list --state open --limit 100`.
2. Took each PR's changed files from `gh pr diff <n> --name-only`.
3. Read `owner_files` for each card from the five batch files under `docs/research/` on `origin/main` (`audit-01` through `audit-05`).
4. Compared the two lists per PR. Checked three conditions: files outside the card's `owner_files`, two PRs touching the same file, and one card claimed by two PRs.

## Scope

12 PRs are in scope: 1435-1446, plus 1447 and 1448 because they carry AUTOSP-12 and AUTOSP-01 cards. PR 1405 (BCX-17, `fix/exe135`) is open but has no AUTOSP/MEM/TUI/PROTO/INTG card, so it is out of scope.

The docs/research exception applies to files under `docs/research/`, which `.gitignore:31` ignores and agents force-add. No other docs path is exempt.

## Result table

| PR | card | files | verdict |
|----|------|-------|---------|
| 1435 | AUTOSP-03 | `internal/infra/wake/acp.go`, `internal/infra/wake/acp_test.go` | OK |
| 1436 | AUTOSP-09 | `internal/infra/store/card_links.go`, `internal/infra/store/card_links_test.go` | OK |
| 1439 | AUTOSP-07 | `cmd/beacon/daemon.go`, `cmd/beacon/daemon_wake_adapter_test.go` | OK |
| 1440 | AUTOSP-05 | `internal/infra/mcp/tool_task_claim.go`, `internal/infra/mcp/tool_task_claim_test.go` | OK |
| 1441 | AUTOSP-02 | `internal/infra/opencode/client.go`, `internal/infra/opencode/client_test.go` | OK |
| 1442 | AUTOSP-06 | `internal/app/taskcli/service.go`, `internal/app/taskcli/service_test.go` | OK |
| 1443 | PROTO-01 | `internal/infra/mcp/contract_test.go`, `tests/e2e/e2e_test.go` | OK |
| 1444 | MEM-05 | `cmd/beacon/gc.go`, `cmd/beacon/gc_test.go` | OK |
| 1445 | PROTO-02 | `internal/infra/httpapi/server.go`, `internal/infra/httpapi/server_test.go` | OK |
| 1446 | TUI-02 | `internal/tui/app.go`, `internal/tui/board.go` | OK |
| 1447 | AUTOSP-12 | `docs/research/audit-01-autospawn.md` | OK |
| 1448 | AUTOSP-01 | `docs/adrs/0307-cibridge-merge-policy-gate.md`, `internal/infra/cibridge/bridge_test.go`, `internal/infra/cibridge/verdict_applier.go` | violation |

## Violations

### 1. PR 1448, card AUTOSP-01: file outside owner_files

File: `docs/adrs/0307-cibridge-merge-policy-gate.md`.

AUTOSP-01 owner_files are `internal/infra/cibridge/verdict_applier.go` and `internal/infra/cibridge/bridge_test.go`. The PR adds a third file. It is not under `docs/research/`, so the ignore rule does not exempt it. Only `docs/adrs/README.md` is ignored (`.gitignore:26`); the ADR file itself is a normal tracked path.

The PR body states the file was added on purpose: "ADR 0307 records the baseline and the decision." This is a scope decision for the PR owner, not a silent mistake. The audit records it as a discipline violation because the change reaches a file the card does not own.

No overlap and no duplicate card were found. All 12 PRs map to distinct cards, and no file path appears in two PR diffs.

## Watch items (not current violations)

- `internal/infra/opencode/client.go` and its test are owner_files of both AUTOSP-02 (PR 1441) and MEM-01. No open MEM-01 PR exists today.
- `internal/app/taskcli/service.go` and its test are owner_files of both AUTOSP-06 (PR 1442) and MEM-06. No open MEM-06 PR exists today.
- `internal/infra/httpapi/server.go` is an owner_file of PROTO-02 (PR 1445) and TUI-04. `cmd/beacon/daemon.go` is an owner_file of AUTOSP-07 (PR 1439) and MEM-04. Neither TUI-04 nor MEM-04 has an open PR, so no live overlap.
- PR 1435 notes a stale four-name assertion in `internal/infra/wake/acp_runtime_test.go`. The PR does not touch that file, so the card boundary holds. CI may still flag it.

## Counts

- PRs checked: 12.
- Violations: 1.
- Cross-PR file overlaps: 0.
- Duplicate card claims: 0.

## Typed context-file roles (CA-11)

The PR 1448 violation shows that the edit boundary lives only in prose. A context entry has no field that separates the card's own files from the other files passed to the executor. The audit catches the breach after the PR, not at write time.

aider handles the same problem with two sets, `abs_fnames` and `abs_read_only_fnames` (`aider/commands.py:1328-1397`). The model sees both sets but edits only the first. The `/read-only` command moves a file into the read-only set. The `/add` command promotes it back when the path exists in the repository.

CA-11 adds a role to the context entry. The `editable` role comes from the card's OwnerFiles. The `readonly` role covers every other file passed to the executor. A write checks the role first and returns the `readonly_file` code on refusal. A promotion command changes the role to `editable` without restarting the session. Ownership rules stay the same, and the card's OwnerFiles remains the only source of the edit boundary.

Acceptance counts 20 writes, ten to `editable` and ten to `readonly`. All ten `readonly` writes are refused with the role code, all ten `editable` writes pass, and the role change takes effect at once. The baseline is one violation in 12 reviewed PRs.
