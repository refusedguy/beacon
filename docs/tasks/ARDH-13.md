# ARDH-13. Убрать capability-интерфейсы с type assertion там, где меняется гарантия
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/infra/cibridge/bridge.go, internal/domain/types.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать обязательное поведение частью основного порта, а необязательные режимы передавать явно, убрав скрытые проверки типа и ветки отката.
Контекст: Источник: docs/research/solid-gof-audit.md, оси OCP и LSP. CheckedMerger, ReasonWaker, WatchHolder, liveWatchSource, activitySource, stateMerger и fixForSource меняют значение успешного ответа или множество Watch. WithWaker(w any) молча игнорирует неизвестный тип. Риск: средний. Нужно сохранить различие между trusted Merger и CheckedMerger.
Шаги: Для каждой пары решить, разделяются ли методы по имени или слабый контракт фиксируется в основном порте. Убрать type switch в WithWaker. Обновить сборку в composition root отдельной задачей, если понадобится.
Проверка: CI зелёный. Нет веток, где отсутствие необязательного метода молча уменьшает результат.
Приёмка: Остались только расширения, не меняющие результат. Гарантии MERGED и LiveWatches сохранены.
