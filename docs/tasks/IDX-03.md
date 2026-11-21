# IDX-03. Команда сборки индекса и pre-commit
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/index.go, .githooks/pre-commit
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: beacon index build пересобирает индекс, pre-commit делает это fail-closed.
Контекст: Premise: сейчас pre-commit fail-open для аренды. Смешивать режимы нельзя, нужна отдельная fail-closed проверка. baseline: grep -rlE 'BuildCardIndex|RebuildIndex|IndexCards|cardindex|taskstore\.Index' --include='*.go' . | wc -l даёт 0. Файл internal/infra/taskstore/index.go отсутствует. IDX-01 со статусом todo владеет этим файлом. tools/beacon-gates/cardindex/check.go тоже отсутствует, владелец IDX-04 со статусом todo.
Шаги: Команда build, вызов в хуке, отдельная ошибка сгенерированного индекса, аренда остаётся fail-open.
Проверка: gofmt, go test в CI.
Приёмка: Устаревший индекс блокирует коммит, аренда не блокирует.

## Блокер

Зелёного света на импорт индекса нет. IDX-01 определяет схему SQLite и импорт карточек через ParseCard в internal/infra/taskstore/index.go. Файла нет, статус IDX-01 равен todo, агент не назначен. Пересобирать пока нечего.

Генератора индекса тоже нет. Гейт tools/beacon-gates/cardindex/check.go, который должен пересобирать индекс по IDX-04, отсутствует. В tools нет ни одного Go файла с index в имени. Правило слоёв запрещает cmd/beacon читать и писать файлы напрямую, поэтому beacon index build должен вызывать генератор, которого нет. Fail-closed проверка в pre-commit не сможет отличить устаревший индекс от отсутствующего, пока генератор не создаёт индекс.

Проверку аренды оставляем fail-open и не смешиваем режимы. Код в OwnerFiles IDX-03 не меняется, чтобы не добавлять команду без индекса и генератора. Go файлы не менялись, gofmt не требуется, go test и go build локально не запускались.

Точный блокер: internal/infra/taskstore/index.go, владелец IDX-01, статус todo. Второй блокер: tools/beacon-gates/cardindex/check.go, владелец IDX-04, статус todo.

## Подтверждение

Проверено 2026-11-21 по `git log` и `gh pr view`. Реализация влита в origin/main через PR #931, https://github.com/refusedguy/beacon/pull/931. Merge commit 42af2b209b58b0f1c991fc8a2ba022ae400c3e74 добавил cmd/beacon/index.go и .githooks/pre-commit. Оба блокера сняты: internal/infra/taskstore/index.go и tools/beacon-gates/cardindex/check.go есть на main. Статус переведён в done.
