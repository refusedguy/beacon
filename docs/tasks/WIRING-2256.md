# WIRING-2256. Migrate CAP-FILE-IN-APP permanent row after taskcli file split
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: tools/beacon-gates/gate/config/permanent.json, tools/beacon-gates/beacon-gates
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 2256
Ссылка: PR 2256
SourceWatch: watch:refusedguy/beacon:2256:19e40dae937fa87720dd504a925627a8d80ee338@1795416903:1800
PR: 2256
Агент: нет
Время: нет
Цель: Перенести permanent-строку CAP-FILE-IN-APP на новые файлы taskcli и пересобрать бинарник гейтов.
Контекст: RSOL-10 перенёс файловый I/O из service.go в cards.go, handoff.go и journals.go. Закоммиченный бинарник гейтов встраивает permanent.json, где строка CAP-FILE-IN-APP всё ещё указывает на service.go. Гейт capabilities сообщал 23 находки и одну устаревшую строку. Для исправления надо было заменить одну строку тремя и пересобрать tools/beacon-gates/beacon-gates на тулчейне CI go 1.26.0.
Шаги: Замена строки и пересборка бинарника выполнены в PR 2256. После мержа вызвать `beacon deferred done` с watchID из SourceWatch и перевести карточку в done.
Проверка: Гейт capabilities проходит без CAP-FILE-IN-APP и CAP-PERMANENT-STALE; гейт gates-binary подтверждает побайтовое совпадение закоммиченного бинарника со сборкой.
Приёмка: Wiring применён, запись в `beacon deferred --format json` имеет status: done, CI зелёный.

## Причина

OwnerFiles карточки RSOL-10 изначально не включали конфиг гейтов и закоммиченный бинарник. Правка и пересборка вошли в тот же PR после повторных verdict keep_working от Beacon. Тулчейн CI воспроизведён локально через `GOTOOLCHAIN=go1.26.0`, пересборка до правки дала побайтово совпадающий бинарник.
