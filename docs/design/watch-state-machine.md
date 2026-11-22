# Формальная модель жизненного цикла Watch

Модель проверяет три правила Watch: один успешный мерж на Watch, вердикт меняется только вместе с ревизией, блок снимается только после `blocked:resolved`. Исходник модели лежит в `docs/design/watch-state-machine.tla`. Проверку запускает `scripts/verify-watch-model.sh`. Модель не импортируется в Go-код, не добавляет зависимостей в `go.mod` и не меняет число MCP-инструментов.

## Состояние

В модели восемь переменных. Первые пять заданы карточкой PSK-03.

| Переменная | Тип | Смысл |
| --- | --- | --- |
| `watch` | watching, notifying, blocked, parked, needs_input, closed | Текущий статус Watch из `internal/domain/transition.go`. |
| `revision` | 0..MaxRev | Ревизия Watch. Растёт на каждую смену вердикта. |
| `verdict` | Verdicts | Вердикт из `internal/domain/verdict.go` и `internal/domain/transition.go`. |
| `subscriber` | watching, notified, finished | Фаза подписчика после публикации вердикта. |
| `mergeLedger` | подмножество `{W} \X RevRange` | Ревизии, на которых зафиксирован успешный мерж. |
| `verdictLog` | подмножество `RevRange \X Verdicts` | История пар «ревизия, вердикт». Нужна для правила 2. |
| `blockEpoch` | 0..MaxRev | Номер текущей эпохи блока. Растёт на каждом `Block`. |
| `resolveCount` | 0..MaxRev | Число доставленных сигналов `blocked:resolved`. |

`verdictLog`, `blockEpoch` и `resolveCount` добавляют историю. Без неё правила 2 и 3 нельзя выразить инвариантом состояния: TLC проверяет предикаты на состоянии, а не сравнение двух последовательных состояний.

## Параметры и границы

`W == "watch-1"` задаёт одну идентичность Watch. Наследник после `head_changed` получает новый ID в коде (`cibridge.handleHeadChanged`, `internal/infra/cibridge/verdict_applier.go:20-89`), поэтому в модели он не появляется. Проверка мержа для наследника требует отдельного прогона с другим `W`.

`MaxRev == 3` ограничивает ревизию. Это граница поиска, а не свойство протокола. Трёх шагов хватает, чтобы получить контрпример на два мержа или на снятие блока. Большее значение увеличивает число состояний и не меняет вывода.

`RevRange == 0..MaxRev`. `Room == revision < MaxRev` не даёт действиям выйти за границу. `Stutter` разрешает шаг без изменений в `closed`, поэтому TLC не останавливается на терминальном тупике.

В модель не входят:

- содержимое `Snapshot`, правила приоритета строк `EvaluatePR` и формула `ShipAllowed`. Модель начинается с уже выбранного вердикта.
- доставка wake, лестница повторов, obligations и их идемпотентность.
- время, TTL, дедлайны и `no-checks timeout`. Они сводятся к вердикту `timeout`.
- замена головы ветки как создание нового Watch. Модель только закрывает текущий Watch действием `HeadChanged`.
- проверка `requireApproval`, `waitFor` и `waitForMain` как вычислимых условий. Они сводятся к вердикту `wait_dependency`.

Эти границы фиксируют проверяемое ядро: переходы вердиктов и три инварианта.

## Действия

Каждый вердикт движка записан отдельным действием с меткой.

| Действие | Источник в коде |
| --- | --- |
| `VerdictWatching` | `EvaluatePR`, строки 9 и 10 (`internal/domain/verdict.go:281-292`) |
| `VerdictRebase` | `EvaluatePR`, строка 4 (`internal/domain/verdict.go:205-209`) |
| `VerdictRetry` | `EvaluatePR`, флейки (`internal/domain/verdict.go:263-267`) |
| `VerdictKeepWorking` | `EvaluatePR`, красные проверки (`internal/domain/verdict.go:268`) |
| `VerdictTimeout` | `EvaluatePR`, строка 8 (`internal/domain/verdict.go:286-289`) |
| `VerdictRecheck` | `cibridge.publishRecheck` (`verdict_applier.go:330-379`) |
| `VerdictWaitDependency` | `cibridge.publishMergeWait` (`verdict_applier.go:232-275`) |
| `VerdictReady` | `EvaluatePR`, строка 6 (`internal/domain/verdict.go:271-279`) |
| `VerdictClosed` | `EvaluatePR`, строки 1 и 2 (`internal/domain/verdict.go:188-193`) |
| `Merge` | `cibridge.applyMerge` и `closeWatch` (`verdict_applier.go:421-430`, `577-623`) |
| `Block` | `cibridge.applyBlocked` (`verdict_applier.go:710-755`) |
| `ResolveSignal`, `Unblock` | `cibridge.unblockFixed` и `domain.UnblockEvent` (`verdict_applier.go:986-1072`) |
| `HeadChanged`, `Cancel`, `Accept` | `domain.Transition` (`internal/domain/transition.go:301-346`) |
| `NeedsInput`, `Continue`, `Park`, `Unpark` | `domain.Transition` (`internal/domain/transition.go:348-431`) |

Операция `Publish` воспроизводит правило `domain.transitionVerdict`: одинаковый вердикт не меняет состояние, а смена вердикта увеличивает ревизию на единицу (`internal/domain/transition.go:489-523`).

## Инварианты

- `NoDoubleMerge`. `Cardinality(mergeLedger) <= 1`. Страховка от повторного успешного мержа одного Watch. В коде её держат ранняя проверка `StatusClosed` в `applyVerdict` (`verdict_applier.go:143-148`) и проверка `next.Revision == w.Revision` в `closeWatch` (`verdict_applier.go:580-583`).
- `NoSilentVerdictChange`. Одна ревизия не несёт два разных вердикта. Проверка перебирает все пары в `verdictLog` для каждой ревизии.
- `BlockLiftedOnlyAfterResolve`. После начала эпохи блока Watch возвращается в `watching` или `notifying` только когда `resolveCount >= blockEpoch`. `Block` увеличивает `blockEpoch` и сбрасывает счётчик неявно: старый сигнал не покрывает новую эпоху.

`TypeOK` проверяет, что все переменные остаются в объявленных множествах.

## Воспроизведение

Скрипт скачивает фиксированную сборку TLC и проверяет её SHA1 перед запуском. Версия привязана к релизу `v1.7.4` (`tla2tools.jar`, SHA1 `bee4a54f3ee3d4afc347c3240ec2d9e93b075104`). Кэш лежит в `${XDG_CACHE_HOME:-$HOME/.cache}/beacon/tla`; путь меняется переменной `TLC_CACHE_DIR`.

```bash
bash scripts/verify-watch-model.sh
```

Скрипт печатает версию инструмента, счётчики TLC и код выхода. Нормальный прогон заканчивается кодом 0.

Замер на коммите `d91e9c85`: TLC v1.7.4, 3951 сгенерированное состояние, 1929 различных состояний, код выхода 0.

## Отрицательные проверки

Каждое правило проверено на кратчайшем контрпримере. Скрипт копирует модель во временный каталог и меняет там одну строку. Исходный файл не меняется.

```bash
bash scripts/verify-watch-model.sh --control=double-merge
bash scripts/verify-watch-model.sh --control=silent-verdict
bash scripts/verify-watch-model.sh --control=early-unblock
```

Каждая команда обязана завершиться ненулевым кодом и напечатать путь до нарушения.

| Режим | Что убрано | Инвариант | Путь | Код |
| --- | --- | --- | --- | --- |
| `double-merge` | строка `/\ NotAlreadyMerged` в `Merge` удалена | `NoDoubleMerge` | `Init -> Merge -> Merge`, 3 состояния | 12 |
| `silent-verdict` | `NewRev == revision + 1` заменено на `NewRev == revision` | `NoSilentVerdictChange` | `Init -> VerdictRebase`, 2 состояния | 12 |
| `early-unblock` | строка `/\ resolveCount >= blockEpoch` в `Unblock` удалена | `BlockLiftedOnlyAfterResolve` | `Init -> Block -> Unblock`, 3 состояния | 12 |

### Сверка путей с кодом

`double-merge`. Контрпример делает два мержа подряд. Второй merж начинается из `closed`. В коде такой шаг отсекается до вызова мержера: `applyVerdict` возвращается для `StatusClosed` (`internal/infra/cibridge/verdict_applier.go:143-148`), а `closeWatch` не пишет повтор, если ревизия не выросла (`internal/infra/cibridge/verdict_applier.go:580-583`). Снятый страж `NotAlreadyMerged` объединяет обе проверки в одну строку модели.

`silent-verdict`. Контрпример меняет вердикт с `watching` на `rebase` при ревизии 0. В движке `transitionVerdict` сначала сравнивает вердикт со старым и возвращается при равенстве, а при смене вызывает `bumpRevision` (`internal/domain/transition.go:495-503`). Публикация в адаптере дополнительно блокируется, если ревизия не выросла (`internal/infra/cibridge/verdict_applier.go:280-284`). Движок `EvaluatePR` сам ревизию не меняет: он возвращает пару `Verdict, []WakeTarget` (`internal/domain/verdict.go:186-293`).

`early-unblock`. Контрпример ставит блок, затем снимает его без сигнала `blocked:resolved`. В коде `applyBlocked` переводит Watch в `StatusBlocked` (`internal/infra/cibridge/verdict_applier.go:714-755`). Обратный переход делают только `ResolveBlocker` и следующий за ним `UnblockEvent` (`internal/infra/cibridge/verdict_applier.go:1045-1062`). Повторная проверка CI заблокированный Watch не снимает: `transitionVerdict` игнорирует вердикты для `StatusBlocked` (`internal/domain/transition.go:489-493`).

## Что модель не доказывает

Модель доказывает три инварианта для перечисленных действий при `MaxRev == 3`. Она не доказывает, что `EvaluatePR` выбирает правильный вердикт по входным фактам, и не проверяет доставку wake. Эти части остаются за тестами Go и CI.
