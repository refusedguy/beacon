# BCX status sync, wave 2

This change updates `docs/research/batches/bcx.json` for cards BCX-46 through BCX-82. Wave 1 (PR #1472) synced only BCX-01 through BCX-45, so the second half of the batch stayed `todo` in the JSON even after the fixes reached main. The stale statuses make batch-apply and close agents create duplicate work.

For each card I searched `git log --all --grep=BCX-NN -i` and ran `git merge-base --is-ancestor <sha> origin/main`. A card changed status only when a commit whose subject names that card is an ancestor of origin/main. The note records the PR and the full SHA. No other field changed.

## Status distribution

| Scope | Status | Before | After |
|---|---|---|---|
| All 82 cards | done | 54 | 79 |
| All 82 cards | todo | 27 | 2 |
| All 82 cards | dropped | 1 | 1 |
| BCX-46 to BCX-82 | done | 12 | 37 |
| BCX-46 to BCX-82 | todo | 25 | 0 |

Wave 2 flipped 25 cards from `todo` to `done`. The BCX-46 to BCX-82 range now has no `todo` cards.

## Cards updated

Every row below has a merge commit that names the card in its subject and has `git merge-base --is-ancestor <sha> origin/main` exit 0.

| Card | PR | Commit | Merged subject |
|---|---|---|---|
| BCX-46 | #1299 | `4dafcf6912b4` | docs(mcp): list every get_result response field in ADR 0094 (BCX-46) (#1299) |
| BCX-49 | #1302 | `5dc4e1e8d2db` | docs(mcp): align wake REACH-UNWIRED reasons with ADR 0263 (BCX-49) (#1302) |
| BCX-51 | #1315 | `93497d6a115f` | fix(gates): register tool-contract gate in CLI and CI (BCX-51) (#1315) |
| BCX-52 | #1322 | `50223cf0a878` | fix(mcp): derive tool contract from one name list (BCX-52) (#1322) |
| BCX-54 | #1323 | `a95678fe0f57` | feat(testsystem): run seeded regression artery in CI (BCX-54) (#1323) |
| BCX-55 | #1324 | `397d87bda630` | test(e2e): load repository plugin in live wake scenarios (BCX-55) (#1324) |
| BCX-56 | #1337 | `151fa9877cbe` | test(chaos): live steer dedup across restart (BCX-56) (#1337) |
| BCX-57 | #1334 | `44f9e53de41e` | test(human): cover needs_input_continue_of_same_watch (BCX-57) (#1334) |
| BCX-58 | #1336 | `b990b5180842` | test(registry): live project boundary with two projects (BCX-58) (#1336) |
| BCX-59 | #1355 | `83436479ba44` | test(e2e): verdict полного цикла вычисляется демоном из фактов (BCX-59) (#1355) |
| BCX-60 | #1319 | `b8c81e1fb5fc` | test(wake): extend ACP smoke to real wake and role matrix (BCX-60) (#1319) |
| BCX-61 | #1312 | `7089eae5d5d8` | test(server): cover old PID after crash mid wait_result (BCX-61) (#1312) |
| BCX-63 | #1328 | `3607970ecc34` | docs(adrs): resolve ADR 0060 TUI contradiction (BCX-63) (#1328) |
| BCX-64 | #1331 | `b6cefb28ea6c` | docs(adrs): BCX-64 replace unreachable live wake check with unit tests (#1331) |
| BCX-68 | #1332 | `a59acd23ee5f` | fix(catalog): list test command per ADR 0016 (BCX-68) (#1332) |
| BCX-69 | #1329 | `8da24238d8d0` | fix(mcp): use one error conversion core (BCX-69) (#1329) |
| BCX-74 | #1333 | `acce7fe9c968` | fix(cmd): validate ACP env command and parse quoted args (BCX-74) (#1333) |
| BCX-75 | #1340 | `8d4ed897c5fa` | docs: collect production env vars in one table (BCX-75) (#1340) |
| BCX-76 | #1348 | `f7049b4850c0` | refactor(config): unify project root env names under BEACON_PROJECT_ROOT (BCX-76) (#1348) |
| BCX-77 | #1349 | `2f4a2a471f4e` | fix(env): unify boolean env parsing across Beacon variables (BCX-77) (#1349) |
| BCX-78 | #1338 | `3540ff43a975` | fix(config): warn on invalid env values and reject bad deadline (BCX-78) (#1338) |
| BCX-79 | #1343 | `88a830d99fb0` | fix(observe): drop grpc from advertised OTLP protocols (BCX-79) (#1343) |
| BCX-80 | #1347 | `6a1c5d5103b9` | fix(cmd): drop dead wake-target env and scope gates to doctor (BCX-80) (#1347) |
| BCX-81 | #1339 | `05e0018b1986` | fix(gc): add scheduled orphan sweep (BCX-81) (#1339) |
| BCX-82 | #1353 | `c35c727900f3` | fix(plugins): ограничить research-агента и опасный shell (BCX-82) (#1353) |

## Checks

`python3 -m json.tool docs/research/batches/bcx.json` passed. The file is valid JSON.

`python3 docs/research/batches/fix_bcx.py` passed with `cards=82 missing=0 directory_refs=0 overlap_groups=0` and exit 0.

Evidence check: 25 cards flipped, 25 with merge-base exit 0, 0 without evidence.

Integrity check: a field-by-field diff against origin/main found 50 changes, all in `status` and `note`, and 0 changes to `id`, `owner_files`, `type`, `phase` or `deps`. `source_reports` and card order are unchanged. I read `internal/infra/batchapply/apply.go`: `done` is in `allowedCardStatuses`, and unknown JSON keys such as `note` are ignored by the decoder.

## Remaining todo cards

Two `todo` cards remain in the batch, both outside the assigned BCX-46 to BCX-82 range. I left them unchanged.

- BCX-05 (wave 1 range). A merged commit #1471 names it, but the subject is `docs(tasks): BCX-05 drop false-premise card and fix OwnerFiles`. That points to a `dropped` decision, not a `done` implementation. Closing it as `done` would be wrong, and it is outside this wave.
- BCX-17 (wave 1 range). No commit across all refs names this card, and no evidence of a close. It stays `todo` until an owner supplies evidence.

## Cards flagged in the task context

BCX-65 (ADR number duplicates) was already `done` in the batch before this wave, backed by the merged close PR #1309 for the fix in PR #1264. I did not change it.

BCX-68 (catalog test command) was `todo` and is now `done`. The merged commit `a59acd23ee5f` is PR #1332, `fix(catalog): list test command per ADR 0016 (BCX-68)`.

BCX-46, 49, 54, 57, 59, 60, 74, 76 and 79 from the task context were all confirmed merged and flipped, with the exact PR and SHA listed above.
