# ATX-29. Закрывать blocked Watch при смене head и запрещать перезапись блоков
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/mcp/tool_watch_pr.go, internal/domain/fanout.go, internal/infra/store/db.go
Зависимости: 
Ссылка: 
Агент: exe104
Время: 2026-11-21T17:56:07Z
Цель: Не оставлять заблокированный Watch после force-push и повторной регистрации.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 4 и строка таблицы 115. internal/infra/cibridge/bridge.go:539-559 обрабатывает StatusBlocked раньше проверки head и делает continue. closeSuperseded в internal/infra/mcp/tool_watch_pr.go:679-711 берёт Watch только из ActiveWatches, а internal/infra/store/db.go:386-392 возвращает watching и notifying. Fanout.Subscribe в internal/domain/fanout.go:92-113 тоже ищет алиас только среди active. Повторная регистрация на том же SHA перезаписывает blocked запись свежим Watch в статусе watching и теряет блок без blocked:resolved.
Шаги: Проверять смену head до ветки blocked. В closeSuperseded и Fanout искать алиас в LiveWatches, а не только в active. Запрещать перезапись blocked Watch без blocked:resolved.
Проверка: CI зелёный. Тест с новым headSHA и Watch в blocked закрывает старый Watch и создаёт преемника. Тест повторной регистрации на том же SHA сохраняет блок.
Приёмка: Blocked Watch не переживает force-push. Блок и связь с инцидентом не теряются при регистрации.

Итог: Уже влито в PR #1251, коммит fe8219ae7818442bdf1569ceba0117ad152d054a. Дубль кода не нужен.
Доказательство: PR #1251 merged; commit fe8219ae7818442bdf1569ceba0117ad152d054a меняет internal/domain/fanout.go, internal/infra/cibridge/bridge.go, internal/infra/mcp/tool_watch_pr.go; git merge-base --is-ancestor fe8219ae7818442bdf1569ceba0117ad152d054a origin/main exit 0; открытого PR-дубля нет.
Причина: already landed in PR #1251 fe8219ae
Время закрытия: 2026-11-21T17:56:07Z
