# План закрытия 14 карточек-блокеров цикла CLEAN и ARDH

Дата: 2026-11-21. Основание: `docs/runbooks/CLOSE-PROCEDURE.md` и поля «Подтверждение» в самих карточках. Реализации и доказательства собраны из текста карточек и истории коммитов. Статусы карточек не правились вручную, индекс не трогался, тесты и сборки не запускались.

## Сейчас происходит

После PR #1233 Beacon связывает карточку с Watch по `cardID` и переводит карточку в `done` при вердикте `merged`. Для 14 карточек из цикла CLEAN и ARDH такого вердикта не будет. Требование уже закрыто другим изменением, premise устарела, либо нужен ответ владельца. Эти карточки висят в `todo` или `in_progress` с зафиксированным доказательством. Их обрабатывает оркестратор по `docs/runbooks/CLOSE-PROCEDURE.md`.

`beacon_task_close` из PR #1229 пока не влит в main, поэтому план использует готовый CLI `beacon task done --summary --evidence` и `beacon task needs-input --question`. Обе команды берут flock и пишут текст карточки через atomic write. Beacon сам меняет статус. Ручная правка `Статус:` запрещена, индекс пересобирается только командой `beacon index build` в CI.

Карточки семейства ATX в этом списке нет. Их hashes в полях «Контекст» ссылаются на прошлые исправления того же класса, а не на реализацию самой карточки. По ATX блокеров с доказательством нет, они остаются обычными задачами.

## Таблица закрытия

Графа «Сейчас» содержит команду, которую оркестратор выполняет немедленно. Для карточек без ответа владельца сначала идёт `needs-input`, а `done` выполняется после фиксации решения. Evidence в командах повторяет доказательство из таблицы. Все хеши это коммиты из истории `origin/main`, проверка доступна через `git merge-base --is-ancestor <hash> origin/main`.

| Карточка | Причина | Доказательство | Команда сейчас |
| --- | --- | --- | --- |
| ARDH-04 | уже влито | PR #1209, коммит `8090ea6b`, общий `attachTool` в регистрации инструментов | `beacon task done ARDH-04` |
| ARDH-08 | уже влито | PR #1160, коммит `34d235df`, merge 2026-11-21T13:15:13Z; `docs/tasks/ARDH-08.md:20` | `beacon task done ARDH-08` |
| ARDH-21 | ложная premise | PR #1154, коммит `d4f7b326`, пакет `httproute` удалён; `docs/tasks/ARDH-21.md:17` | `beacon task done ARDH-21` |
| ARDH-22 | уже влито | PR #1212, коммит `4dbeade6`, MCP-ответ возвращает копию срезов | `beacon task done ARDH-22` |
| CLEAN-05 | owner-решение | ответа нет; `origin/main` `3f575e01`, `docs/tasks/CLEAN-05.md:16` | `beacon task needs-input CLEAN-05` |
| CLEAN-06 | owner-решение | ответа нет; `docs/tasks/CLEAN-06.md:63-68` | `beacon task needs-input CLEAN-06` |
| CLEAN-08 | owner-решение | ответа нет; ADR 0016 фиксирует вариант A, `docs/tasks/CLEAN-08.md:29-33` | `beacon task needs-input CLEAN-08` |
| CLEAN-10 | ложная premise | PR #1154, коммит `d4f7b32`, вторая шина удалена; `docs/tasks/CLEAN-10.md:18-20` | `beacon task done CLEAN-10` |
| CLEAN-11 | уже влито | PR #1182, коммит `b6aaf6fe`, два каталога удалены; `docs/tasks/CLEAN-11.md:19-23` | `beacon task done CLEAN-11` |
| CLEAN-12 | чужой PR | PR #493, `WIRING-493` в todo; нет owner-decision по ADR 0060, `docs/tasks/CLEAN-12.md:16` | `beacon task needs-input CLEAN-12` |
| CLEAN-13 | чужой PR | PR #1153 `94f6d29c` и PR #1156 `370041d8`, обе wiring-карточки закрыты; `docs/tasks/CLEAN-13.md:19-30` | `beacon task done CLEAN-13` |
| CLEAN-14 | уже влито | PR #1188, коммит `1f3f55bf`, head `67c8aa41`; `docs/tasks/CLEAN-14.md:18` | `beacon task done CLEAN-14` |
| CLEAN-15 | ложная premise | PR #1183, коммит `3bc14d27`, guard уже в wake-пути; `docs/tasks/CLEAN-15.md:18-29` | `beacon task done CLEAN-15` |
| CLEAN-16 | owner-решение | ответа нет; 7 символов удалены, 2 подключены, 18 ожидают; `docs/tasks/CLEAN-16.md:55-65` | `beacon task needs-input CLEAN-16` |

Итого 9 карточек закрываются через `done` сразу. Пять карточек ждут ответа владельца и получают `needs-input`. Это разделение следует таблице причин в `docs/runbooks/CLOSE-PROCEDURE.md`. Строка про решение владельца даёт `done`, только когда решение зафиксировано, и `needs-input`, когда формулировка неоднозначна. В CLEAN-05, CLEAN-06, CLEAN-08, CLEAN-12 и CLEAN-16 в карточках прямо записано, что ответа нет. Придумывать решение за владельца запрещено.

## Полные команды, группа done

Команды выполняет оркестратор. `--summary` и `--evidence` это разные обязательные поля. Первое не заменяет второе. После каждой команды проверить, что в карточке появились «Итог» и «Доказательство».

```bash
beacon task done ARDH-04 \
  --summary "Общий скелет регистрации MCP-инструментов введён, отдельная правка не нужна" \
  --evidence "PR #1209 merged, commit 8090ea6b; git merge-base --is-ancestor 8090ea6b origin/main -> exit 0; docs/tasks/ARDH-04.md"

beacon task done ARDH-08 \
  --summary "Общий список полей недостижим между модулями, взаимные ссылки зафиксированы" \
  --evidence "PR #1160 merged 2026-11-21T13:15:13Z, commit 34d235df; git merge-base --is-ancestor 34d235df origin/main -> exit 0; docs/tasks/ARDH-08.md:20"

beacon task done ARDH-21 \
  --summary "Второго формата HTTP-ошибки нет, пакет httproute удалён" \
  --evidence "PR #1154, commit d4f7b326; git merge-base --is-ancestor d4f7b326 origin/main -> exit 0; docs/tasks/ARDH-21.md:17"

beacon task done ARDH-22 \
  --summary "MCP-ответ возвращает копию срезов, ссылка на глобальный срез убрана" \
  --evidence "PR #1212, commit 4dbeade6; git merge-base --is-ancestor 4dbeade6 origin/main -> exit 0"

beacon task done CLEAN-10 \
  --summary "Вторая event bus удалена, жива одна шина httpapi.EventBus" \
  --evidence "PR #1154, commit d4f7b32; git merge-base --is-ancestor d4f7b32 origin/main -> exit 0; docs/tasks/CLEAN-10.md:18-20"

beacon task done CLEAN-11 \
  --summary "Два мёртвых пакета удалены, testsystem жив по seed-spec" \
  --evidence "PR #1182, commit b6aaf6fe; git merge-base --is-ancestor b6aaf6fe origin/main -> exit 0; docs/tasks/CLEAN-11.md:19-23"

beacon task done CLEAN-13 \
  --summary "Premise устарела: wiring-карточки закрыты, пакет импортируется демоном" \
  --evidence "PR #1153 commit 94f6d29c, PR #1156 commit 370041d8, оба достижимы из origin/main; docs/tasks/CLEAN-13.md:19-30"

beacon task done CLEAN-14 \
  --summary "Невызываемые wake-конструкторы удалены" \
  --evidence "PR #1188, commit 1f3f55bf, head 67c8aa41; git merge-base --is-ancestor 1f3f55bf origin/main -> exit 0; docs/tasks/CLEAN-14.md:18"

beacon task done CLEAN-15 \
  --summary "registryWakeAllowed уже подключён к продуктивному wake-пути, удалять нельзя" \
  --evidence "PR #1183, commit 3bc14d27, ветка fix/clean15-guard, 18c3c5b9; docs/tasks/CLEAN-15.md:18-29"
```

## Полные команды, группа owner

Команды оставляют статус `in_progress` или `todo` и пишут «Состояние: needs_input» и «Вопрос». FIX-агента по ним не спавнят. После ответа владельца оркестратор вызывает `beacon task done` со ссылкой на сообщение владельца в `--evidence`.

```bash
beacon task needs-input CLEAN-05 \
  --question "Нужно решение владельца: сохранять историю всех редакций ADR или удалить 35 дублей и снизить baseline_limit с 40 до 5? В main, GitHub и research решения нет."

beacon task needs-input CLEAN-06 \
  --question "Нужно письменное решение: архивировать 326 dropped-карточек и 124 worktree или оставить? Укажите карточки с обоснованиями, которые трогать нельзя."

beacon task needs-input CLEAN-08 \
  --question "Нужно решение по модели доставки beacon-gates: оставить закоммиченный бинарник по ADR 0016 или перейти на сборку в CI без бинарника?"

beacon task needs-input CLEAN-12 \
  --question "Нужно решение по WIRING-493 и PR #493: разрешить ребро cmd к internal/tui и передать cmd/beacon/mcp.go для claim и release, либо явно отменить проводку и разрешить удаление internal/tui?"

beacon task needs-input CLEAN-16 \
  --question "Нужно решение по reachability: убрать 7 удалённых и 2 подключённых символа сейчас, 18 ожидающих оставить до закрытия волн Waves 6-8 и Wave 7?"
```

## Проверка перед закрытием

Оркестратор проходит список из `docs/runbooks/CLOSE-PROCEDURE.md` до каждой команды.

1. Прочитать «Цель» и «Приёмка» карточки и сформулировать требование.
2. Сверить доказательство именно с этим требованием.
3. Убедиться, что доказательство содержит хеш коммита, номер PR или ссылку на решение.
4. Убедиться, что карточка активна и ещё не в `done` или `dropped`.
5. Подготовить непустые `--summary` и `--evidence`.
6. Закрывать только карточки, закреплённые за оркестратором.
7. После команды сверить поля «Итог» и «Доказательство» или «Состояние» и «Вопрос».

Команды идут по одной. Повторный `done` на уже закрытой карточке получает отказ без записи. Если карточка уже в `done`, её пропускают.

<!-- blocker-close-plan
source: docs/runbooks/CLOSE-PROCEDURE.md
cards: ARDH-04, ARDH-08, ARDH-21, ARDH-22, CLEAN-05, CLEAN-06, CLEAN-08, CLEAN-10, CLEAN-11, CLEAN-12, CLEAN-13, CLEAN-14, CLEAN-15, CLEAN-16
done-now: 9
needs-input: 5
cli: beacon task done --summary --evidence, beacon task needs-input --question
status-writer: Beacon
manual-card-edit: false
index-edit: false
-->
