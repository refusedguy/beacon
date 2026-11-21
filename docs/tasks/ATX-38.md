# ATX-38. Сообщать о потере строк в beacon logs и поднимать ошибку сканера
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/logs.go, internal/infra/observe/query.go
Зависимости: 
Ссылка: 
Агент: exe113
Время: 2026-11-21T17:57:47Z
Цель: Не терять старые записи и длинные строки молча.
Контекст: Источник: docs/research/output-limits-audit.md, раздел 6 и строка таблицы 173. cmd/beacon/logs.go:23 и :31-34 задают --tail по умолчанию 100, --tail 0 означает все. internal/infra/observe/query.go:76-94 оставляет хвост среза в :92-94 без предупреждения. bufio.Scanner имеет предел 4 MB на строку в :77, ошибка сканера игнорируется в :89. Continuation token нет. Baseline: 100 записей и 4 MB на строку.
Шаги: Печатать предупреждение при отсечении хвоста. Документировать --tail 0. Поднимать ошибку сканера вместо игнорирования. Либо заменить Scanner на Reader с повышенным лимитом.
Проверка: CI зелёный. Тест с 150 записями видит предупреждение. Тест со строкой длиннее 4 MB возвращает ошибку или полную строку.
Приёмка: Пользователь видит, что вывод неполный. Длинная строка не исчезает молча.

Итог: Уже влито в PR #1246 (commit fedf9e29efff97e05a03fa60d60bfecec73d45de), дубль кода не создан
Доказательство: git merge-base --is-ancestor fedf9e29efff97e05a03fa60d60bfecec73d45de origin/main exits 0; PR #1246; https://github.com/refusedguy/beacon/pull/1246; коммит меняет ровно OwnerFiles cmd/beacon/logs.go и internal/infra/observe/query.go: предупреждение OnTruncate, документация --tail 0, возврат ошибки scanner.Err
Время закрытия: 2026-11-21T17:59:33Z