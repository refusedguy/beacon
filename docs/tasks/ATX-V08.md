# ATX-V08. Зафиксировать информационную проверку в контракте вердикта
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: docs/GLOSSARY.md, docs/RULES-CHECK-CONTRACT.md
Зависимости: 
Ссылка: docs/research/verdict-contract-gaps.md, раздел 8, docs/templates/executor-task.md:199-211
Агент: нет
Время: нет
Цель: Перенести правило informational-проверки из AGENTS.md и шаблона исполнителя в канонический контракт вердикта.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 8. В `GLOSSARY.md` есть статьи `Verdict`, `BlockerIncident` и `blocked:not_mine`, но нет статьи об информационной проверке. `RULES-CHECK-CONTRACT.md` упоминает `continue-on-error: true` только в разделе про rules-gate job (:29) и там же запрещает его. Про CI-проверки с `required: false` контракт не говорит ничего. Baseline: ноль определений термина в двух канонических документах, правило живёт в двух других файлах.
Шаги: Добавить в GLOSSARY статьи «Информационная проверка» и `required:false` с указанием, что такие проверки не дают `VerdictBlocked`, `keep_working` и `blocked:not_mine`. Добавить в RULES-CHECK-CONTRACT раздел о связи informational-проверки с вердиктом CI. Перечислить все источники `VerdictBlocked` и `keep_working`, включая rule 5, исчерпание бюджета ребейза и merge-ошибки. Указать, что источники вне проверок не зависят от `Required`.
Проверка: CI зелёный. Термины есть в обоих документах. Перечень источников совпадает с путями в коде после карточек ATX-V01..V07. Документы не противоречат шаблону исполнителя.
Приёмка: Контракт вердикта полон без обращения к AGENTS.md и к шаблону исполнителя.
