# ATX-29. Закрывать blocked Watch при смене head и запрещать перезапись блоков
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/mcp/tool_watch_pr.go, internal/domain/fanout.go, internal/infra/store/db.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не оставлять заблокированный Watch после force-push и повторной регистрации.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 4 и строка таблицы 115. internal/infra/cibridge/bridge.go:539-559 обрабатывает StatusBlocked раньше проверки head и делает continue. closeSuperseded в internal/infra/mcp/tool_watch_pr.go:679-711 берёт Watch только из ActiveWatches, а internal/infra/store/db.go:386-392 возвращает watching и notifying. Fanout.Subscribe в internal/domain/fanout.go:92-113 тоже ищет алиас только среди active. Повторная регистрация на том же SHA перезаписывает blocked запись свежим Watch в статусе watching и теряет блок без blocked:resolved.
Шаги: Проверять смену head до ветки blocked. В closeSuperseded и Fanout искать алиас в LiveWatches, а не только в active. Запрещать перезапись blocked Watch без blocked:resolved.
Проверка: CI зелёный. Тест с новым headSHA и Watch в blocked закрывает старый Watch и создаёт преемника. Тест повторной регистрации на том же SHA сохраняет блок.
Приёмка: Blocked Watch не переживает force-push. Блок и связь с инцидентом не теряются при регистрации.
