# IDX-09. Маркер graphReady в инструментах
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_stats.go, internal/domain/types.go
Зависимости: 
Ссылка: 
Агент: ses_e02a32d76ffeI0dJ0VyDpy95Mw
Время: 2026-11-21
baseline: до правки StatsOutput содержал 10 полей JSON, маркеров graphReady было 0, диагностических префиксов в tool_stats.go было 5. Измерено через git show HEAD и grep по двум OwnerFiles.
Цель: Агент видит, что граф построен и актуален.
Контекст: Premise: владелец попросил маркер для агентов, что граф есть.
Шаги: Поле graphReady в stats, поле в ответе claim, тест на свежий и устаревший индекс.
Проверка: gofmt, go test в CI.
Приёмка: Маркер виден без чтения файлов и меняется при устаревании индекса.

## Решение

Тип domain.GraphStatus хранит два хеша и флаг Built. Метод Ready возвращает true только когда индекс построен и хеш индекса равен хешу дерева карточек. StatsHandler читает необязательный порт GraphIndexReader и добавляет в loadErrors запись graphReady: true или graphReady: false. Помощник graphReady читает запись по префиксу и возвращает false при отсутствии маркера.

Новое поле JSON в StatsOutput не добавлялось. Схема beacon_stats выводится из StatsOutput в internal/infra/mcp/schema.go, а этот файл принадлежит IDX-06. Диагностический массив loadErrors уже несёт блокирующие счётчики и метрику ожидания, поэтому маркер использует тот же канал. Поле в ответе claim оставляем другой карточке.

Порт не привязан в composition root, пока IDX-01 и IDX-02 не построят индекс. Без привязанного порта маркер отсутствует, и помощник graphReady читает это как false. При ошибке чтения маркер равен false. Агент не получит ложное подтверждение графа. Маркер добавляется только при привязанном порте, поэтому ответы без порта сохраняют прежний loadErrors.

## Изменения

- internal/domain/types.go: тип GraphStatus и метод Ready.
- internal/infra/mcp/tool_stats.go: порт GraphIndexReader, сеттер, чтение статуса, запись маркера и помощники graphReadyEntry и graphReady.
- internal/infra/mcp/tool_stats_graph_test.go: тесты на свежий, устаревший и отсутствующий индекс, на ошибку чтения и на правило Ready.

gofmt применён к трём Go-файлам. go test и go build локально не запускались, проверка идёт в CI PR.
