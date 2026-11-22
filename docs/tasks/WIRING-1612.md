# WIRING-1612. Связать индекс taskstore с обработчиком claim
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/mcp/server.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1612
Ссылка: PR 1612
SourceWatch: watch исходного PR #1612 закрыт после мержа, headSHA не зафиксирован в описании PR
PR: 1612
Агент: нет
Время: нет
Цель: Передать общий индекс taskstore в TaskClaimHandler, чтобы маркер graphReady в beacon_task_claim принимал реальное значение.
Контекст: PR #1612 добавил поле graphReady и порт GraphIndexReader с сеттером. Без привязки в composition root обработчик работает с nil reader, и маркер всегда false. MP-02 уже добавил Index.GraphStatus в internal/infra/taskstore/index.go.
Шаги: Дождаться мержа PR 1612; создать общий индекс taskstore в cmd/beacon/mcp.go; передать его в TaskClaimHandler через SetGraphIndexReader при сборке mcp.Dependencies в internal/infra/mcp/server.go; проверить, что stats и claim читают один и тот же индекс; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: beacon_task_claim возвращает graphReady=true для свежего графа и false для устаревшего, тесты mcp зелёные в CI, запись WIRING-1612 переведена в done.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

Привязка живёт в cmd/beacon/mcp.go и internal/infra/mcp/server.go, вне OwnerFiles PR #1612. Поэтому её вынесли в отдельную проводку и зафиксировали карточкой в том же PR.
