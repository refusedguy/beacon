# WIRING-1680. Upload eval-results.json as CI artifact

Статус: todo
Фаза: P3
Тип: task
Владелец файлов: .github/workflows/tests.yml
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1680
Ссылка: PR 1680
SourceWatch: watch:refusedguy/beacon:1680:dc17be2e141feabe1abc0f6d6e5a8ffa5de262e7@1795351798:1800
PR: 1680
Агент: нет
Время: нет
Цель: Подключить загрузку tmp/eval-results.json как артефакта CI в шаге из EM-01.

Контекст: EM-02 пишет отчёт в tmp/eval-results.json и печатает путь в лог. Шаг загрузки артефакта живёт в .github/workflows/tests.yml, который принадлежит EM-01 (ses_dfcb8ae15). Проводка отложена, чтобы не конфликтовать с его работой над тем же файлом.

Шаги: Дождаться мержа PR 1680 и PR EM-01. Добавить в джобу registry-check шаг actions/upload-artifact с путём tmp/eval-results.json. Вызвать `beacon deferred done` для записи с watchID из SourceWatch.

Проверка: Артефакт виден в Actions. Локальный гейт WIRING-* проходит: статус todo, OwnerFiles заполнен, раздел «Причина» есть.

Приёмка: Проводка применена, CI зелёный, карточка закрыта.

## Причина

EM-02 пишет отчёт, но загрузка артефакта требует правки .github/workflows/tests.yml. Этот файл принадлежит EM-01, его PR ещё не открыт. Правка в PR 1680 создала бы конфликт, поэтому проводка отложена.
