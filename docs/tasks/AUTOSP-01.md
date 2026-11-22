# AUTOSP-01. Закрыть обход политики авто-мержа в cibridge
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/verdict_applier.go, internal/infra/cibridge/bridge_test.go
Зависимости: 
Ссылка: ADR 0307
Агент: нет
Время: нет
Цель: Не мержить PR на VerdictReady, пока RequireApproval, WaitFor и WaitForMain не проверены.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 1. internal/infra/cibridge/verdict_applier.go:101-117 applyVerdict, ветка VerdictReady в :106-109 смотрит только current.MergePolicy.AutoMerge. Поля политики перечислены в internal/domain/types.go:244-249, полная проверка живёт в internal/app/orchestrator/orchestrator.go:146-181 canAutoMerge. orchestrator.Run вызывается из CLI и порта демона, но не в пути мержа cibridge. baseline: обход воспроизведён чтением кода, тест на RequireApproval в цикле cibridge отсутствует.
Шаги: Вынести проверку MergePolicy в отдельный метод verdict applier. На RequireApproval держать Watch и публиковать wait без merger. Для WaitFor и WaitForMain подключить DependencyResolver через новый порт, при nil или ошибке держать wait. Вызвать проверку до applyMerge. Добавить тесты в bridge_test.go на три условия и на зелёный результат без условий.
Проверка: CI зелёный. Тест с RequireApproval не вызывает merger. Тест с неразрешённым WaitFor не вызывает merger. Тест с пустой политикой сохраняет текущий мерж.
Приёмка: Ни один PR с удержанием политики не попадает в applyMerge. Поведение по умолчанию не меняется.

Итог: Already landed in #1448 (e724f0eca86582a9f2353257b06293c18023af89). No duplicate code needed.
Доказательство: PR #1448 commit e724f0eca86582a9f2353257b06293c18023af89 merge-base --is-ancestor e724f0eca86582a9f2353257b06293c18023af89 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1448 e724f0e
Время закрытия: 2026-11-22T11:29:08Z
