# RS-MCP-06. Срок жизни watchID как непрозрачного handle
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/mcp/tool_wait_result.go, internal/infra/mcp/tool_claim.go
Зависимости: RS-MCP-04
Ссылка: 
Агент: нет
Время: 2026-11-21
Цель: Явно сообщать инструментальной ошибкой об обращении к просроченному watchID и documenting срок жизни в описании.
Контекст: Premise: проверено 2026-11-21, watchID уже непрозрачный handle, но срок жизни в описании не зафиксирован. baseline: `grep -rn 'expired\|ttl' internal/infra/mcp/tool_wait_result.go internal/infra/mcp/tool_claim.go` пусто. Спецификация MCP требует инструментальной ошибки для мёртвого handle.
Блокер: Проверка 2026-11-21 опровергла premise. Grep по OwnerFiles не пустой: в tool_claim.go есть TTL и expiry у аренд, но это срок жизни аренды, а не watchID. В tool_wait_result.go неизвестный watch уже даёт isError с CodeWatchNotFound, а закрытый или отменённый Watch даёт isError с CodeGone или CodeCancelled. Мёртвый handle не превращается в пустой результат. В tool_claim.go нет ни одного упоминания watchID, поэтому шаг «проверка в обоих обработчиках» не имеет второй цели среди OwnerFiles.
Чужой файл: internal/domain/fanout.go. Функция watchID в строке 217 строит ID как `watch:<repo>:<pr>:<headSHA>` без времени выпуска и без срока действия. Срок жизни handle нельзя вывести из этого ID. Сопутствующие чужие файлы: internal/domain/types.go, где Watch хранит CreatedAt, DeadlineAt и ClosedAt, но не срок действия handle; DeadlineAt является сроком задачи. internal/domain/transition.go:253 зафиксировал отказ от TTL closure по ADR 0019, поэтому готовый срок жизни переиспользовать нельзя.
Решение: Реализация только в OwnerFiles свелась бы к дублированию существующей проверки isError и не дала бы настоящего срока жизни handle. Возвращаю блокер с именем файла internal/domain/fanout.go. Нужен владелец чужих файлов или пересмотр карточки до постановки задачи.
Шаги: Добавить проверку срока жизни в обоих обработчиках. Возвращать isError с указанием, что handle просрочен и как получить новый. Прописать срок жизни в описании инструмента. Тесты на живой, просроченный и неизвестный handle.
Проверка: gofmt чистый. Тесты в CI. Неизвестный инструмент по-прежнему даёт протокольную ошибку.
Приёмка: Обращение к мёртвому watchID больше не выглядит как пустой результат.

## Подтверждение
- PR #882 MERGED: 2026-11-21 (squash). https://github.com/refusedguy/beacon/pull/882
