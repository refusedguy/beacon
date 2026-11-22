# BVF-01. Развести конфликты owner_files между пакетами аудитов в одной волне
Статус: done
Premise: PR #1474, коммит 174307e30cb95d1869fbcb3d132d04507765816f, развёл семь пересечений owner_files между пакетами аудитов. git merge-base --is-ancestor 174307e30cb95d1869fbcb3d132d04507765816f origin/main вернул 0.
Фаза: P0
Тип: task
Владелец файлов: docs/research/audit-01-batch.json, docs/research/audit-02-batch.json, docs/research/audit-03-batch.json, docs/research/audit-04-batch.json
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать семь пересечений owner_files между карточками AUTOSP, MEM, TUI и PROTO в рамках одной фазы.
Контекст: Отчёт docs/research/BATCH-VALIDATION.md, раздел «Конфликты волн между пакетами аудитов». В P0 конфликтуют internal/infra/opencode/client.go, internal/infra/opencode/client_test.go и cmd/beacon/watch.go. В P1 конфликтуют cmd/beacon/mcp.go, internal/app/taskcli/service.go, internal/app/taskcli/service_test.go и internal/infra/httpapi/server.go. Два исполнителя одной волны получат один файл.
Шаги: Для каждой из семи пар назначить одному владельцу другой owner_file, либо перенести одну карточку в другую фазу, либо добавить зависимость deps так, чтобы карточки не запускались параллельно. Сохранить валидные значения type, phase, status и id. Прогнать проверку пересечений по всем четырём пакетам.
Проверка: Нет ни одного файла, закреплённого за двумя карточками с одинаковым phase в разных пакетах.
Приёмка: Проверка пересечений по четырём пакетам аудитов возвращает пустой результат. Отчёт обновлён.
