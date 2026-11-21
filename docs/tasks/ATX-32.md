# ATX-32. Считать wake по подписчику и добавить выход из parked
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/wake/deliver.go, internal/domain/types.go
Зависимости: 
Ссылка: 
Агент: exe107
Время: 2026-11-21T17:56:27Z
Цель: Не парковать Watch после трёх циклов и не списывать общий бюджет на нескольких подписчиков.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 6 и строка таблицы 118. Счётчик wake живёт в обязательстве wakes:<watchID> и общий для всех подписчиков. Один цикл с targets для executor и orchestrator тратит две единицы. Предел в шесть доставок в internal/domain/types.go:114-137 достигается за три цикла. parkForWakeCap ставит StatusParked и пишет строку в ESCALATIONS.md. Запись от 2026-11-20 подтверждает сценарий. parked не входит в ActiveWatches и не видится оркестратору.
Шаги: Считать wake по паре (watchID, subscriberID) или не тратить бюджет на orchestrator. Перед парковкой писать структурированную эскалацию с watchID и инцидентом, а не только строку в файл. Добавить явный путь вывода из parked.
Проверка: CI зелёный. Тест с executor и orchestrator на одном Watch не достигает капа за три цикла. Тест parked показывает запись с watchID и инцидентом.
Приёмка: Бюджет не расходуется на фонового подписчика. Parked Watch имеет структурированную эскалацию.

Итог: Fix уже влит в origin/main через PR #1252 (коммит a67afaf02961837b47ae3d7a7acd5b3985bf3be2, 'fix(wake): count wake budget per subscriber and leave parked after notice'). git merge-base --is-ancestor a67afaf02961837b47ae3d7a7acd5b3985bf3be2 origin/main -> exit 0. Дубль кода не нужен: PR #1252 переносит счётчик wake на пару (watchID, subscriberID), пишет структурированную эскалацию с watchID и инцидентом перед парковкой и возвращает Watch из parked в watching после доставки уведомлений.
