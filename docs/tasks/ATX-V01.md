# ATX-V01. Свести извлечение упавших проверок к одной доменной функции
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/domain/verdict.go, internal/infra/cibridge/ci_refresh.go, internal/domain/verdict_test.go, internal/infra/cibridge/ci_refresh_test.go
Зависимости: 
Ссылка: PR #1279, docs/research/verdict-contract-gaps.md, раздел 1
Агент: нет
Время: нет
Цель: Получить один источник fail-фактов, который отбрасывает информационные проверки до того, как вердикты и wake увидят их имена.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 1. Доменная `realFailures` в verdict.go:317-341 проверяет `Required` и безвредные заключения. Инфраструктурная `realFailedNames` в ci_refresh.go:125-128 вызывает `checkFacts` (:100-121), который не смотрит на `Required` и собирает FAILURE rollup из любой упавшей проверки. Baseline: два определения fail-фактов, одно без фильтра. PR #1279 закрыл только правило 5 в verdict.go:221-229.
Шаги: Экспортировать из domain функцию, которая по карте проверок и списку Failures возвращает имена реально упавших блокирующих проверок. Использовать её в `realFailures`. Заменить `realFailedNames` в cibridge на вызов этой функции. При сборке rollup в `checkFacts` учитывать только required-проверки. Сохранить fail-closed для проверки, которой нет в карте.
Проверка: CI зелёный. Тест с `ci-build` FAILURE required и `ci-info` FAILURE optional ожидает только `ci-build` в именах и `SUCCESS` в rollup, если required-проверок больше нет. Тест с неизвестным именем сохраняет fail-closed.
Приёмка: Ни один вызывающий не собирает fail-факты из `checkFacts` напрямую. Две функции с разным поведением больше не существуют.
