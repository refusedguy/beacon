# RS-CMP-01. Проверка мержа против свежего main
Premise: проверено 2026-11-21 по origin/main, база ветки d3b1bfb. ADR 0266 предписывает перед мержем перечитывать baseRef и возвращать задачу на повторную проверку при расхождении с head main. Сейчас MergePRWithState в internal/infra/github/batch_provider.go:371 получает снимок obs.pr из того же батча: вызов в internal/infra/cibridge/bridge.go:846. Перечитать baseRef можно в моём файле, но условия реализации лежат вне OwnerFiles.

Первое условие: для сравнения нужен base SHA на момент батча. Тип параметра domain.PRState, internal/domain/types.go:380, хранит только BaseRefName, строка 384. SHA базы в нём нет. Base SHA лежит в domain.BaseBranchState.HeadSHA, internal/domain/types.go:442, внутри PRSnapshot.Base. В MergePRWithState это значение не попадает: ciObservation в bridge.go:444 держит pr и base отдельно, а в вызов на строке 846 уходит только obs.pr. Чтобы передать base SHA, надо менять сигнатуру MergePRWithState, интерфейс stateMerger, bridge.go:134, и сам вызов bridge.go:846.

Второе условие: отдельный результат должен трактоваться оркестратором как повторная проверка. Результат мержа разбирает Bridge.applyMerge, internal/infra/cibridge/bridge.go:860. Он знает только merged, hold с причиной и auto-rebase через isMergeBehind, bridge.go:1923. Вердикта recheck в domain.Verdict нет, internal/domain/types.go:27-38. Чтобы добавить повторную проверку, надо править bridge.go, а при новом вердикте ещё и domain/types.go.

Третье препятствие: все тесты MergePRWithState лежат в internal/infra/github/batch_provider_test.go, строки 365-748. Этот файл не входит в OwnerFiles. Мой rebase_test.go проверяет только Rebase. Файл internal/infra/github/rebase.go из OwnerFiles в этой ветке не существует, Rebase определён в batch_provider.go:329.

baseline: `grep -n 'func (b \*BatchProvider) MergePRWithState' internal/infra/github/batch_provider.go` = 371; `grep -n 'MergePRWithState(ctx, w.Repo' internal/infra/cibridge/bridge.go` = 846; `grep -n 'type stateMerger' internal/infra/cibridge/bridge.go` = 134; `grep -n 'type ciObservation struct' internal/infra/cibridge/bridge.go` = 444; `grep -n 'type PRState struct' internal/domain/types.go` = 380, `BaseRefName` = 384; `grep -n 'HeadSHA.*json:"headSHA"' internal/domain/types.go` = 442; `grep -n 'func (b \*Bridge) applyMerge' internal/infra/cibridge/bridge.go` = 860; `grep -n 'func isMergeBehind' internal/infra/cibridge/bridge.go` = 1923; `grep -n 'func TestMergePR' internal/infra/github/batch_provider_test.go` = 365, 423, 466, 512, 585, 694, 730; `ls internal/infra/github/rebase.go` = файла нет; `git rev-parse --short origin/main` = d3b1bfb.
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/github/batch_provider.go, internal/infra/github/rebase.go, internal/infra/github/rebase_test.go
Зависимости: 
Ссылка: ADR 0266
Агент: нет
Время: нет
Цель: Перепроверять mergeability сразу перед мержем и возвращать rebase при расхождении baseRef, а не после неудачного мержа.
Контекст: Premise: проверено 2026-11-21, MergePRWithState использует состояние из того же батча, полученного до решения. baseline: `grep -n 'func (b \*BatchProvider) MergePRWithState' internal/infra/github/batch_provider.go` = строка 371. Источник: Mergify merge queue и GitHub merge queue.
Шаги: Добавить перечтение baseRef в MergePRWithState. При расхождении возвращать отдельный результат, который оркестратор трактует как повторную проверку. Тест на два PR, меняющих один файл: первый мержится, второй должен провалиться на подготовке поверх свежего main.
Проверка: gofmt чистый. Тесты в CI. Негативный контроль: старый снимок больше не даёт ready.
Приёмка: Вердикт ready подтверждён проверкой на актуальном main. Конфликт не превращается в мерж с последующим rebase.

## Блокер

- 2026-11-21. Шаги карточки нельзя выполнить в OwnerFiles `internal/infra/github/batch_provider.go`, `internal/infra/github/rebase.go`, `internal/infra/github/rebase_test.go`.

  Перечитать baseRef перед мержем можно в batch_provider.go, но сравнение требует base SHA из батча. Он лежит в `domain.BaseBranchState.HeadSHA` (internal/domain/types.go:442) и доходит до `ciObservation.base` (bridge.go:444), а в `MergePRWithState` уходит только `obs.pr` (bridge.go:846). Передача SHA ломает сигнатуру `stateMerger` (bridge.go:134) и вызов в bridge.go.

  Отдельный результат для повторной проверки разбирает `Bridge.applyMerge` (bridge.go:860). Сейчас он превращает hold с причиной в auto-rebase через `isMergeBehind` (bridge.go:1923) или в вердикт rebase и keep_working. Вердикта recheck в `domain.Verdict` нет (internal/domain/types.go:27-38). Новый результат требует правки bridge.go и, если нужен новый вердикт, domain/types.go.

  Тест на два PR естественно ложится в `internal/infra/github/batch_provider_test.go` (строки 365-748), вне OwnerFiles. Файл `internal/infra/github/rebase.go` из OwnerFiles в ветке отсутствует.

  Поведение из ADR 0266 в main ещё нет: сейчас работает rebase после неудачного мержа. Блокер не в дубле, а в чужой проводке оркестратора.

  Статус todo сохранён. OwnerFiles не менял, код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, дубль не создал. Блокер возвращён оркестратору с именем файла `internal/infra/cibridge/bridge.go`. Для реализации нужны OwnerFiles с bridge.go, а также доступ к domain/types.go и batch_provider_test.go или переформулировка шагов под существующие поля и интерфейс.

## Подтверждение
- PR #878 MERGED: 2026-11-21 (squash).
