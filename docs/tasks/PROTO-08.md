# PROTO-08. Привязать registryListSource в composition root или убрать команду
Статус: done
Premise: PR #1449, коммит daa93aa5cd82ccea3e688e299ecf2f7e7d692c1b, привязал registryListSource к корню текущего проекта в cmd/beacon/registry_list.go. git merge-base --is-ancestor daa93aa5cd82ccea3e688e299ecf2f7e7d692c1b origin/main вернул 0. git grep -n registryListSource origin/main -- cmd/beacon/registry_list.go даёт :77 и :203, источник строит reader от c.root.
baseline: было 0 рабочих read-путей, стал 1 с границей текущего проекта. Проверка: git show origin/main:cmd/beacon/registry_list.go | sed -n '73,90p' показывает reader, привязанный к root.
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/registry_list.go, cmd/beacon/registry_list_test.go
Зависимости: 
Ссылка: 
Агент: exe-mp04
Время: 2026-11-22T06:25:35Z
Цель: Убрать команду, которая в production всегда отвечает отказом, и не расширять MCP-каталог ради неё.
Контекст: Источник: docs/research/audit-04-protocols.md, таблица строка 5. registryListSource в cmd/beacon/registry_list.go:44-46 возвращает registry reader not connected. Вызов в :116, маршрут в cmd/beacon/main.go:356. Вне тестов источник не привязан. Baseline: 0 рабочих read-путей.
Шаги: Выбрать одно: привязать источник, ограниченный текущим проектом, в точке композиции, либо удалить команду и её каталог. Не добавлять двенадцатый MCP-инструмент. Сохранить проектную границу читателя.
Проверка: CI зелёный. Тест на то, что команда не видит другие проекты. Тест на отказ при отсутствии источника.
Приёмка: Изменения только в OwnerFiles карточки. MCP-дескрипторы не меняются.
