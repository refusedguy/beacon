# VCI-03: доказательство уже влитого решения

Режим документа: reference. Этот файл фиксирует, что работа VCI-03 уже находится в `origin/main`. Исполнитель получил `card VCI-03 not found` из store и не стал писать дубль кода.

## Доказательство

- Полный SHA: `39a0905c352c009d0522e2f6915ae6aa9de196fc`.
- PR: [#1917](https://github.com/refusedguy/beacon/pull/1917), состояние `MERGED`, `mergeCommit` равен этому SHA.
- `git merge-base --is-ancestor 39a0905c352c009d0522e2f6915ae6aa9de196fc origin/main` вернул 0. Проверка повторена на базе `e589b3e49f292b7d6b8153bf3982b03db9a45830`. База из брифа `79dc16941bdbb5ff312f85e8602dda0e67be1a2a` тоже содержала коммит.
- Карточки `docs/tasks/VCI-03.md` нет ни в store, ни в `origin/main`.

## Символы на базе `e589b3e4`

| Место | Символ | Происхождение |
| --- | --- | --- |
| `internal/infra/store/blockers.go:319` | `(*DB).ResolveBlockersByBaseGreen` | `39a0905c` (PR #1917) добавил чтение legacy строк в запросе на строке 335 |
| `internal/infra/store/blockers.go:335` | `base_ref IN ('main', '')` | `39a0905c` (PR #1917) |
| `internal/infra/store/blockers_test.go:302` | `TestResolveBlockersByBaseGreenLegacyEmptyBaseRef` | `39a0905c` (PR #1917) |
| `internal/infra/cibridge/verdict_applier.go:491` | `(*verdictApplier).resolveBaseGreenBlockers` | `f82897a5` (PR #1771) |
| `internal/infra/cibridge/bridge.go:1199` | вызов `resolveBaseGreenBlockers` после цикла по Watch | `f82897a5` (PR #1771) |

## Почему дубль не написан

Карточки VCI-03 нет в store, а код уже в main. Новая карточка или повторная правка тех же файлов создали бы дубль. OwnerFiles из брифа (`verdict_applier.go`, `blockers_test.go`, `bridge.go`) заняты открытыми PR #2230, #2193 и #2173. Этот PR меняет только данный документ.
