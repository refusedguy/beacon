# PSK-03. Формальная модель жизненного цикла Watch
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: docs/design/watch-state-machine.tla, docs/design/watch-state-machine.md, scripts/verify-watch-model.sh
Зависимости: 
Ссылка: https://raw.githubusercontent.com/cursor/plugins/main/pstack/skills/statechart/SKILL.md
Агент: нет
Время: 2026-11-22
Цель: Описать состояния Watch и переходы вердиктов в проверяемой модели и показать кратчайший путь до каждого нарушения правила.
Контекст: Источник: docs/research/research-pstack-skills.md, строка таблицы statechart и доказательство 3. `git grep -i -E 'TLA\+|model checker|formal verification|формальная верификация' origin/main -- docs README.md AGENTS.md GLOSSARY.md` вернул пустой вывод. `GLOSSARY.md` определяет Watch, Verdict, WorkerPhase и Ladder как состояния с переходами, а `AGENTS.md:121-139` задаёт правила оркестратора и авто-мерж. Описание переходов есть, проверки инвариантов нет. Инварианты, которые стоит проверить: не два успешных мержа одного Watch, вердикт не меняется без новой ревизии, блок снимается только после `blocked:resolved`. Риск: средний. Модель живёт вне Go-бинарника и не добавляет зависимостей в `go.mod`.
Шаги: Написать `docs/design/watch-state-machine.tla` с переменными watch, revision, verdict, subscriber и mergeLedger. Записать каждый переход из `internal/domain/verdict.go` отдельным действием с меткой. Записать INVARIANT для каждого правила из предыдущего абзаца. Собрать `scripts/verify-watch-model.sh`, который запускает TLC на фиксированной версии инструмента и печатает счётчик состояний и путь до нарушения. Добавить в `docs/design/watch-state-machine.md` описание границ модели, параметров и команд воспроизведения. Для каждого найденного нарушения сверить путь с `internal/domain/verdict.go` и `internal/infra/cibridge/verdict_applier.go`.
Проверка: `bash scripts/verify-watch-model.sh` завершается кодом 0 на текущей модели. На контрольной правке, которая убирает проверку уже сделанного мержа, скрипт завершается ненулевым кодом и печатает путь до нарушения. После отката контрольной правки скрипт снова зелёный. Модель не импортируется в Go-код и не меняет `go.mod`.
Приёмка: Все три инварианта проверены TLC, и для каждого показано, что модель умеет находить нарушение. Документ называет границы проверки и версию инструмента. Добавление модели не меняет число MCP-инструментов, протокол Watch и required-проверки CI.
