# ADR 0042. Шесть фич из отчёта Y-wave

Статус: accepted

Этот ADR фиксирует шесть фич из отчёта Y «Y-волна: 19 новых проектов» от 20 ноября 2026 года. Идеи собраны в разделе «Фичи» ([Y, строка 180](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L180)) и подтверждены разделами конкретных проектов. Здесь зафиксированы только те фичи, которых нет в ADR 0001-0041 и 133 карточках из `docs/tasks/`. Heartbeat и reaper уже в ADR 0032 пункт 3, inbox с ack в ADR 0039 пункт 6, агрегатор CI в ADR 0030, один владелец ожидания в ADR 0032 пункт 4 и ADR 0038 пункт 1, живость по PID в T3.44 и ADR 0036 пункт 4, идемпотентность по `clientRequestId` в ADR 0028. Несколько новых пунктов похожи на существующие решения по имени, но различаются по уровню. Три сигнала в фиче 2 это один составной детектор, а не одиночный мёртвый PID из T3.44. Адресация в фиче 3 меняет ключ маршрутизации уведомления, а изоляция worktree из ADR 0021 меняет только размещение задачи. Completion gate в фиче 4 требует proof и завершённых фаз, а review gate из ADR 0038 пункт 4 требует одобрения рецензента. Replay в фиче 6 повторяет сценарий целиком и проверяет отсутствие второго эффекта, а `clientRequestId` из ADR 0028 гасит повтор одной команды. Владелец велел принять оставшиеся идеи без дополнительных вопросов, поэтому статус `accepted`. По ADR 0020 карточки создаются только из принятого ADR. Карточки этим ADR не создаются, код, карточки и тесты в этой задаче не трогаются.

## Решение

Beacon получает шесть механизмов. Они описаны на уровне контракта и не привязаны к конкретному продукту.

1. Дескриптор задачи с дедлайном. Задача несёт дедлайн в своём дескрипторе. Дескриптор передаётся во все вложенные вызовы, поэтому потомки не могут жить дольше родителя, и сохраняется в хранилище при рестарте, поэтому дедлайн не сбрасывается после падения процесса. Дедлайн это жёсткий лимит: по его истечении задача завершается с причиной, а не уходит в бесконечное ожидание. Это сквозной контекст задачи, а не таймаут отдельного вызова и не бюджет ожидания из ADR 0017.

2. Трёхсигнальная детекция смерти. Beacon считает задачу мёртвой по трём сигналам вместе: записанный PID, время последней активности и возраст heartbeat. Захват задачи после такого выявления идёт атомарной транзакцией на SQLite. Один мёртвый PID из T3.44 остаётся быстрым признаком, но не единственным: PID может пережить задачу, а задача может зависнуть при живом PID. Составной детектор не отменяет T3.44, а добавляет к нему два сигнала и атомарный захват по ADR 0015.

3. Адресация уведомления к worktree. Уведомление адресуется по пути worktree, а не по sessionID и не по `PEER_NAME` из ADR 0039. Перезапущенная сессия в том же worktree получает уведомление без перенастройки адресата, а сессия в другом worktree его не получает. ADR 0021 по-прежнему изолирует агентов через worktree; здесь меняется только ключ, по которому находится получатель уведомления.

4. Completion gate с proof. Агент не закрывает задачу, пока не выполнены все фазы плана и не оставлен proof. Proof это проверяемый след выполненной фазы, записанный рядом с задачей и попадающий в архив с одобрением. Gate проверяет оба условия и при невыполненном условии возвращает задачу в работу с причиной, а не меняет вердикт. Это условие закрытия задачи, а не review gate из ADR 0038 пункт 4: там решение принимает рецензент, здесь агент обязан предъявить фазы и proof.

5. Замороженный crash-test. Набор сценариев crash-test хешируется до написания кода. Движок сверяет хеш набора сценариев при каждом запуске и отказывается работать, если набор изменился без пересмотра решения. Сценарий нельзя тихо ослабить или удалить, чтобы пройти проверку. Это правило стабильности набора тестов, а не требование ADR 0010 о наличии тестов в каждой задаче.

6. Идемпотентный replay сценария. Повторный прогон сценария целиком не создаёт второй эффект. Движок сверяет, что повтор привёл к тому же состоянию и не произвёл дополнительную запись, результат или уведомление. `clientRequestId` из ADR 0028 гасит повтор одной команды `wait_result`, `steer` или `ack`; здесь проверяется весь сценарий, включая побочные шаги между командами.

## Причина

1. pulse передаёт дедлайн во вложенные вызовы и сохраняет его ([Y, строка 186](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L186), [README](https://github.com/arun0009/pulse/blob/main/README.md#L33)). warship-harness задаёт бюджет как жёсткий лимит ([Y, строка 186](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L186), [README](https://github.com/fakiolinho/warship-harness/blob/main/README.md#L163)). Beacon сейчас не проносит дедлайн через дерево вызовов, поэтому потомок живёт дольше родителя. Код pulse не берётся: идея фиксируется на уровне контракта дескриптора.

2. parallel-sessions и durable-agents складывают детекцию смерти из нескольких признаков ([Y, строка 188](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L188), [README](https://github.com/sezeryavuz/parallel-sessions/blob/main/README.md#L243), [README](https://github.com/AleBrito124356/durable-agents/blob/main/README.md#L38)). Атомарный захват задачи на SQLite описан там же ([Y, строка 188](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L188), [README](https://github.com/AleBrito124356/durable-agents/blob/main/README.md#L38)). T3.44 и ADR 0036 пункт 4 определяют живость по артефактам и мёртвому PID одним сигналом, поэтому фича 2 добавляет к нему активность и возраст heartbeat, а не повторяет его.

3. ouijit адресует сообщения к worktree ([Y, строка 190](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L190), [README](https://github.com/ouijit/ouijit/blob/main/README.md#L35)). Inbox с ack из ADR 0039 пункт 6 использует ключ `PEER_NAME`, который привязан к сессии и меняется при её перезапуске. Для уведомлений, которые должны дойти до работы в конкретном worktree, нужен ключ пути, а не ключ сессии.

4. planning-with-files не даёт закрыть работу без заполненных фаз плана ([Y, строка 194](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L194), [README](https://github.com/OthmanAdi/planning-with-files/blob/master/README.md#L267)). speckeep требует оставить proof вместе с результатом ([Y, строка 194](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L194), [README](https://github.com/bzdvdn/speckeep/blob/master/README.md#L9)). spec-sync хранит архив с одобрением ([Y, строка 194](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L194), [README](https://github.com/CorvidLabs/spec-sync/blob/main/README.md#L13)). Вместе это даёт проверяемое условие закрытия, которого нет ни в review gate из ADR 0038 пункт 4, ни в вердикте из ADR 0001.

5. durable-agent-workflows хеширует набор сценариев до написания кода и отказывается работать при изменении набора ([Y, строка 196](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L196), [README](https://github.com/Mandy8639/durable-agent-workflows/blob/main/README.md#L15)). ADR 0010 требует тесты в каждой задаче, но не защищает сам набор сценариев от тихой правки. Числа из раздела «Перформанс» Y-отчёта это заявления авторов проектов ([Y, строка 198](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L198)), в контракт Beacon они не переносятся.

6. larkx и agent-handoff-kit проверяют, что повтор сценария не создаёт второй эффект ([Y, строка 196](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L196), [README](https://github.com/larkxai/larkx/blob/main/README.md#L46), [README](https://github.com/KMdotcom/agent-handoff-kit/blob/main/README.md#L37)). ADR 0028 решает идемпотентность по `clientRequestId` на уровне одной команды, а не целого сценария с несколькими побочными шагами. verifier snapshot из agent-handoff-kit ([Y, строка 177](/home/nbook/Projects/tmp/beacon-research/Y-wave.md#L177)) подтверждает ту же идею на границе handoff.

## Последствия

- Из каждого пункта выходит одна небольшая карточка в `docs/tasks/` по правилам ADR 0020. Карточки этим ADR не создаются и будут созданы отдельными задачами.
  - Фича 1: `internal/domain/types.go`, `internal/infra/mcp/schema.go`, `internal/infra/mcp/server.go`, `internal/app/taskcli/service.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/db.go` и тесты к ним.
  - Фича 2: `internal/domain/types.go`, `internal/domain/transition.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/db.go`, `internal/infra/mcp/tool_list_running.go`, `internal/app/taskcli/service.go` и тесты к ним.
  - Фича 3: `internal/domain/types.go`, `internal/infra/mcp/server.go`, `internal/infra/mcp/schema.go`, `internal/infra/mcp/tool_wake.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/db.go` и тесты к ним.
  - Фича 4: `internal/domain/verdict.go`, `internal/domain/transition.go`, `internal/domain/types.go`, `internal/infra/mcp/tool_ack.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/archive.go`, `internal/app/taskcli/service.go` и тесты к ним.
  - Фича 5: `cmd/beacon/doctor.go`, `cmd/beacon/main.go`, `internal/infra/store/db.go`, `docs/gates/G1-shadow.md`, `internal/app/taskcli/service.go` и тесты к ним.
  - Фича 6: `internal/infra/mcp/server.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/store/receipts.go`, `internal/infra/store/db.go`, `internal/domain/identity.go`, `internal/infra/taskstore/store.go` и тесты к ним.
- Проверка карточек: дедлайн доходит до вложенного вызова и сохраняется после рестарта, а по истечении даёт причину завершения; три сигнала вместе переводят задачу в мёртвые и захватываются одной транзакцией SQLite; уведомление доходит до сессии в том же worktree после перезапуска и не уходит в другой worktree; агент не закрывает задачу без всех фаз и proof, а gate возвращает её в работу с причиной; движок отказывается стартовать при изменённом наборе сценариев до правки решения; повтор сценария даёт то же состояние без второй записи, результата или уведомления.
- ADR 0001, 0015, 0017, 0020, 0021, 0028, 0030, 0032, 0036, 0038, 0039 и 0040 не переписываются. Heartbeat из ADR 0032 пункт 3, inbox с ack из ADR 0039 пункт 6, агрегатор CI из ADR 0030 и один владелец из ADR 0032 пункт 4 остаются на своих местах. T3.44 остаётся быстрым признаком по PID, `PEER_NAME` остаётся ключом inbox, review gate из ADR 0038 пункт 4 остаётся решением рецензента, `clientRequestId` из ADR 0028 остаётся ключом одной команды. Ключи разные: дедлайн из фичи 1 ограничивает дерево вызовов, три сигнала из фичи 2 определяют смерть и захват, путь worktree из фичи 3 адресует уведомление, proof из фичи 4 разрешает закрытие, хеш из фичи 5 фиксирует набор сценариев, а replay из фичи 6 проверяет отсутствие второго эффекта.

## Отклонённое

Сводить дедлайн к таймауту одного вызова или к бюджету ожидания из ADR 0017. Дедлайн это сквозной контекст задачи, который переживает рестарт и ограничивает потомков.

Полагаться только на мёртвый PID из T3.44 или добавлять опрос `ps`. Живой PID не доказывает, что задача работает, а опрос процессов дороже чтения трёх сохранённых сигналов.

Адресовать уведомление по sessionID или по `PEER_NAME` из ADR 0039. Оба ключа меняются при перезапуске сессии, а получатель привязан к worktree.

Считать задачу закрытой по одному вердикту без фаз и proof или подменять это review gate из ADR 0038 пункт 4. рецензент принимает решение, а proof подтверждает выполненную работу.

Править набор crash-сценариев без пересмотра решения или ослаблять сценарий, чтобы пройти проверку. Хеш фиксирует набор до написания кода.

Сводить replay к `clientRequestId` из ADR 0028. Тот ключ гасит повтор одной команды, а replay проверяет весь сценарий и его побочные эффекты.
