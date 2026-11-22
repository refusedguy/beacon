# TUI-07. Разделение невыполнимой карточки

Исходная карточка TUI-07 требовала правки в файлах вне её OwnerFiles. Исполнитель отказался от PR и привёл три факта. Все три подтвердились чтением origin/main.

## Проверка предпосылки

| Утверждение исполнителя | Что показал origin/main |
| --- | --- |
| openEvents не шлёт ?replay | internal/tui/client.go:317 строит запрос без параметра replay. Last-Event-ID выставляется только на строке 327. |
| Snapshot без хвоста событий | Структура Snapshot в client.go:135 содержит watches, blockers и pointStats, но не содержит поля событий. |
| watch не рисует хвост | renderHTTPSnapshot в cmd/beacon/watch.go:551 печатает заголовок, таблицу, ATTENTION и ESCALATIONS. Хвоста событий в выводе нет. |
| Нет детекта stdout TTY | boardHasTerminal в cmd/beacon/board_wire.go:95 проверяет только stdin. runWatch в watch.go:212 и App.Run в app.go:125 пишут в io.Writer без проверки stdout. |
| board_wire.go без владельца | Ни одна карточка пакета не перечисляет cmd/beacon/board_wire.go в OwnerFiles. |
| Огромный буфер требует правки production | maxBodyBytes в client.go:31 равен константе 1 << 20. Файл client.go принадлежит TUI-03. |

Дубля и уже слитого фикса нет. `git log --all --grep=TUI-07 -i` вернул пустой вывод, открытых PR с этим id нет.

## Было и стало

| Было | Стало | Причина |
| --- | --- | --- |
| TUI-07, type eval, OwnerFiles: internal/tui/client_test.go и cmd/beacon/watch_w8b_test.go, deps пусто | TUI-07A, type task, те же OwnerFiles, deps [TUI-03] | Тест хвоста зависит от ?replay и поля событий. Оба изменения делает TUI-03. Production-файлы в OwnerFiles не включены. |
| Шаг про eval-отчёт без пути в OwnerFiles и отрицательным контролем на большой буфер | TUI-07B, type eval, OwnerFiles docs/evals/u01-render-15-watches.md, deps [TUI-03] | Путь отчёта теперь в OwnerFiles. Файл уже есть в origin/main и содержит замер первого кадра и RSS. Отрицательный контроль переписан без увеличения maxBodyBytes. |
| Шаг про текстовый fallback без TTY | TUI-07C, status dropped, OwnerFiles cmd/beacon/board_wire.go, deps [TUI-01, TUI-02, TUI-03] | Детект stdout TTY требует правки board_wire.go, watch.go и app.go. board_wire.go не имеет владельца в пакете, поэтому задача останется невыполнимой и после мержа TUI-01, TUI-02 и TUI-03. |
| Отрицательный контроль eval-кейса TUI-LIVE-SINGLE-SCREEN на буфер больше серверного лимита | Поток SSE с избытком кадров в тестовом провайдере | maxBodyBytes зафиксирован константой. Новый контроль не требует правки internal/tui/client.go. |

Карточка TUI-07 переименована в TUI-07A, отдельной dropped-копии с прежним id нет. Новые id TUI-07A, TUI-07B и TUI-07C не конфликтуют с существующими карточками пакета.

## Результаты проверок

Скрипт `/tmp/opencode/verify_tui07.py` не входит в PR. Его вывод приведён здесь, чтобы проверку можно было повторить.

| Проверка | Результат | Доказательство |
| --- | --- | --- |
| python3 -m json.tool | PASS | exit 0 |
| Уникальность id | PASS | 12 id, дублей нет |
| type, phase, status | PASS | Все значения из закрытых множеств apply.go: task, adr, eval, doc; P0..P8; todo, done, dropped. |
| OwnerFiles существуют в origin/main | PASS | Все пути найдены в `git ls-tree -r --name-only origin/main`. |
| Пересечения OwnerFiles внутри фазы | PASS | Пересечений нет, в том числе в P1. |
| deps ссылаются на существующие id | PASS | TUI-03, TUI-01 и TUI-02 есть в пакете. |

TUI-07A оставляет production-правки владельцу TUI-03. TUI-07B не требует правок вне docs/evals/. TUI-07C помечена dropped и не создаёт дубля активной карточки.

Изменены только docs/research/audit-03-batch.json и этот отчёт. bcx.json и docs/research/OWNERFILE-DISCIPLINE.md не тронуты. Production Go-код не менялся. go test, go build, beacon gates и batch-apply не запускались.
