# ATX-32. Считать wake по подписчику и добавить выход из parked
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/wake/deliver.go, internal/domain/types.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не парковать Watch после трёх циклов и не списывать общий бюджет на нескольких подписчиков.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 6 и строка таблицы 118. Счётчик wake живёт в обязательстве wakes:<watchID> и общий для всех подписчиков. Один цикл с targets для executor и orchestrator тратит две единицы. Предел в шесть доставок в internal/domain/types.go:114-137 достигается за три цикла. parkForWakeCap ставит StatusParked и пишет строку в ESCALATIONS.md. Запись от 2026-11-20 подтверждает сценарий. parked не входит в ActiveWatches и не видится оркестратору.
Шаги: Считать wake по паре (watchID, subscriberID) или не тратить бюджет на orchestrator. Перед парковкой писать структурированную эскалацию с watchID и инцидентом, а не только строку в файл. Добавить явный путь вывода из parked.
Проверка: CI зелёный. Тест с executor и orchestrator на одном Watch не достигает капа за три цикла. Тест parked показывает запись с watchID и инцидентом.
Приёмка: Бюджет не расходуется на фонового подписчика. Parked Watch имеет структурированную эскалацию.
