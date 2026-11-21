# RS-MCP-05. Динамическая версия в описании инструмента
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/mcp/server.go, internal/infra/mcp/meta.go
Зависимости: 
Ссылка: 
Агент: t15
Время: нет
Цель: Брать версию в описании инструмента из артефакта сборки, а не из хардкода.
baseline: `git grep -h -E 'BuildVersion|versionFromBuild|DescriptionWithVersion' HEAD -- 'internal/infra/mcp/*.go' | wc -l` = 0, источника версии из сборки в пакете нет. `git show HEAD:internal/infra/mcp/server.go | grep -c ServerVersion` = 3, единственная версия это `const ServerVersion = "v1.0.0"` в server.go:27. `git grep -h 'Description:' HEAD -- 'internal/infra/mcp/*.go' | wc -l` = 18, ни одно описание не несёт версию. `git grep -n -E 'os.Stdout|fmt.Print[^F]|println\(' HEAD -- 'internal/infra/mcp/*.go'` даёт 8 строк, все это fmt.Fprintln в буфер в tool_defer_wiring.go, обращений к stdout ядра нет. Контракт tools/list закреплён в internal/infra/mcp/contract_test.go:30,35 как 16095 байт и SHA-256 `8324205f18b1b4c8e90843aeafcad90b8170c8c7ae7b49733a864b6a358f3aab`.
Контекст: Premise проверена 2026-11-21, версия в листинге не привязана к сборке. Источник: steipete.me/posts/2025/mcp-best-practices рекомендует динамическую версию в описании инструмента, логи в файл и пустой stdout.
Решение: meta.go получил `versionFromBuild`, переменную для `-ldflags "-X github.com/refusedguy/beacon/internal/infra/mcp.versionFromBuild=..."`, и `BuildVersion()`. Функция берёт значение из сборки, затем из `debug.ReadBuildInfo().Main.Version`, а для исходной сборки без штампа возвращает пустую строку. `DescriptionWithVersion` добавляет версию только в описание beacon_stats. Пустая версия оставляет описание побайтово прежним, поэтому закреплённый контракт RS-MCP-01 не меняется. server.go получил поле `Dependencies.Version` как единственный источник при создании сервера; пустое значение заменяется на `BuildVersion()`. Порядок ToolNames не тронут.
Шаги: Добавить источник версии из артефакта сборки. Передавать версию через единственный источник при создании сервера. Подставить версию в описание beacon_stats. Добавить тест, что два разных значения версии дают два разных описания при неизменном остальном листинге.
Проверка: `gofmt -l internal/infra/mcp/server.go internal/infra/mcp/meta.go internal/infra/mcp/version_test.go` пустой. Тест `TestToolsListVersionIsDynamic` в internal/infra/mcp/version_test.go сравнивает листинги для v1.2.3 и v4.5.6, проверяет одинаковый порядок, неизменные все поля кроме одного описания и пустой stdout ядра. `TestUnstampedBuildKeepsDescriptionStable` проверяет, что сборка без штампа не меняет описание. `TestBuildVersionPrefersBuildStamp` проверяет приоритет значения из сборки. `go test` и `go build` локально не запускались по правилам AGENTS.md, зелёность подтвердит CI на PR.
Приёмка: Смена версии сборки меняет описание beacon_stats без правки описаний вручную. Остальной листинг и порядок инструментов остаются прежними. stdout ядра остаётся пустым.
