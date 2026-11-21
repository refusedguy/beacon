# ATX-27. Не выдавать keep_working за informational-проверку
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/domain/verdict.go, internal/infra/github/fixtures_contract_test.go
Зависимости: 
Ссылка: 
Агент: exe102
Время: 2026-11-21T17:55:10Z
Цель: Не буди исполнителя чинить проверку с required: false.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 1 и строка таблицы 113. internal/infra/cibridge/bridge.go:608-629 перебирает все проверки и не смотрит на Required. internal/domain/verdict.go:205-213 фильтрует только conclusion через harmlessConclusion. Упавший informational-гейт даёт VerdictKeepWorking и wake с текстом про failedChecks. AGENTS.md:113 прямо запрещает считать такую проверку блокером. Фикстурный контракт в internal/infra/github/fixtures_contract_test.go:225-260 читает rollup из StatusCheckRollup.State и не ловит расхождение.
Шаги: Фильтровать необязательные проверки в checkFacts или передать Required в Snapshot и учесть в realFailures. Добавить тестовый кейс с упавшим informational-гейтом и отдельным required-гейтом.
Проверка: CI зелёный. Тест с упавшим required: false даёт вердикт без keep_working и без wake исполнителю.
Приёмка: Информационные проверки не влияют на вердикт. Обязательные проверки по-прежнему формируют failedChecks.
