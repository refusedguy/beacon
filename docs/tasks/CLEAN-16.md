# CLEAN-16. Проверить список REACH-UNWIRED перед очисткой
Статус: todo
Owner-decision: требуется, конфиг не меняется до решения владельца
Фаза: P2
Тип: task
Владелец файлов: tools/beacon-gates/gate/config/reachability.json, tools/beacon-gates/beacon-gates
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сверить 27 символов REACH-UNWIRED с реальным кодом и подготовить план очистки тех причин, которые устарели.
Контекст: Источник: docs/research/deadcode-audit.md, предпоследняя строка таблицы. Гейт разрешает символам быть недостижимыми, а причина обещает подключение в Waves 6-8 и Wave 7. Часть причин противоречит ADR 0263. Риск: высокий. Нужен owner-decision: это официальный список отложенного кода, удалять его можно только вместе с закрытием волн.
Шаги: Разбить 27 записей на три группы: символы удалены, символы подключены, символы всё ещё ожидают проводки. По каждой группе предложить действие владельцу. Не трогать конфиг до решения.
Проверка: Есть таблица из 27 строк с фактом по каждому символу и решением владельца.
Приёмка: Конфиг reachability не изменён до решения владельца. Карточка остаётся todo.

Baseline: origin/main = b9937c0, в reachability.json 20 записей REACH-UNWIRED. В устаревшей локальной ветке main = d3b1bfb их 27. Коммит 9899cbdb уже убрал 7 записей из конфига на origin/main. Замер: `rg -n -w <symbol> --glob '*.go' internal cmd tools` по каждому из 27 имён. Итог: 7 имён отсутствуют в коде, 20 присутствуют. Из 20 присутствующих 2 достижимы из cmd/, 18 не достижимы. Модель достижимости описана в tools/beacon-gates/reachability/reachability.go:64,65,277. Методы не являются узлами графа, поэтому вызов T210Deliver из метода daemonWaker.Wake не подключает T21x-адаптеры.

Подтверждение аудита: все 27 символов проверены 21 ноября 2026 года в worktree am2 на HEAD b9937c0. Конфиг reachability.json и бинарник beacon-gates в этом PR не изменены.

## Результат сверки

Группы: удалён = имя отсутствует в internal/, cmd/, tools/, подключён = есть ссылка из объявления в cmd/, ожидает проводки = объявление есть, но ссылки из cmd/ нет. Риск указан для удаления кода или исключения из конфига.

| № | Символ из конфига | В коде | Группа | Факт | Риск | Действие владельца |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | internal/infra/mcp/ACPWaitService | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 2 | internal/infra/mcp/MentionWaitService | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 3 | internal/infra/mcp/NewACPWaitService | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 4 | internal/infra/mcp/NewMentionWaitService | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 5 | internal/infra/wake/T215Adapter | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 6 | internal/infra/wake/T216Bridge | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 7 | internal/infra/wake/T218Adapter | нет | удалён | 0 совпадений в internal/, cmd/, tools/ | нет | ничего, уже убран коммитом 9899cbdb |
| 8 | internal/infra/mcp/WakeHandler | да | подключён | internal/infra/mcp/tool_wake.go:645; тип указан в cmd/beacon/mcp.go:445 | низкий | убрать исключение после одобрения |
| 9 | internal/infra/mcp/NewWakeHandler | да | подключён | internal/infra/mcp/tool_wake.go:716; вызов в cmd/beacon/mcp.go:446 | низкий | убрать исключение после одобрения |
| 10 | internal/infra/ghci/Provider | да | ожидает проводки | internal/infra/ghci/provider.go:67; ghci.New только в cibridge/bridge_test.go:1000,1034,1097 | высокий | оставить исключение до закрытия волны |
| 11 | internal/infra/github/GitHubActionsAdapter | да | ожидает проводки | internal/infra/github/errors.go:471; регистрация в NewStatusParser, errors.go:412, только в errors_test.go | высокий | оставить исключение до закрытия волны |
| 12 | internal/infra/github/GitLabAdapter | да | ожидает проводки | internal/infra/github/errors.go:594; регистрация в errors.go:413, только в errors_test.go | высокий | оставить исключение до закрытия волны |
| 13 | internal/infra/github/NewBitbucketAdapter | да | ожидает проводки | internal/infra/github/errors.go:664; регистрация в errors.go:414, только в errors_test.go | высокий | оставить исключение до закрытия волны |
| 14 | internal/infra/github/NewGitHubActionsAdapter | да | ожидает проводки | internal/infra/github/errors.go:475; регистрация в errors.go:412, только в errors_test.go | высокий | оставить исключение до закрытия волны |
| 15 | internal/infra/github/NewGitLabAdapter | да | ожидает проводки | internal/infra/github/errors.go:598; регистрация в errors.go:413, только в errors_test.go | высокий | оставить исключение до закрытия волны |
| 16 | internal/infra/mcp/ClaimHandler | да | ожидает проводки | internal/infra/mcp/tool_claim.go:377; AttachClaim, tool_claim.go:570, только в tool_claim_test.go | высокий | оставить исключение до закрытия волны |
| 17 | internal/infra/mcp/HolderLeaseHandler | да | ожидает проводки | internal/infra/mcp/tool_claim.go:1255; AttachHolderLease, tool_claim.go:1323, только в tool_claim_test.go | высокий | оставить исключение до закрытия волны |
| 18 | internal/infra/mcp/NewCapabilityFallbackWakeHandler | да | ожидает проводки | internal/infra/mcp/tool_wake.go:751; только в tool_wake_test.go:1637,1666 | высокий | оставить исключение до закрытия волны |
| 19 | internal/infra/mcp/NewCapabilityWakeHandler | да | ожидает проводки | internal/infra/mcp/tool_wake.go:724; только в tool_wake_test.go | высокий | оставить исключение до закрытия волны |
| 20 | internal/infra/mcp/NewClaimHandler | да | ожидает проводки | internal/infra/mcp/tool_claim.go:389; только в tool_claim_test.go | высокий | оставить исключение до закрытия волны |
| 21 | internal/infra/mcp/NewFallbackWakeHandler | да | ожидает проводки | internal/infra/mcp/tool_wake.go:743; только в tool_wake_test.go | высокий | оставить исключение до закрытия волны |
| 22 | internal/infra/mcp/NewHolderLeaseHandler | да | ожидает проводки | internal/infra/mcp/tool_claim.go:1261; только в tool_claim_test.go | высокий | оставить исключение до закрытия волны |
| 23 | internal/infra/mcp/NewWorktreeLeaseHandler | да | ожидает проводки | internal/infra/mcp/tool_claim.go:897; только в tool_claim_test.go | высокий | оставить исключение до закрытия волны |
| 24 | internal/infra/mcp/NewWorktreeWakeHandler | да | ожидает проводки | internal/infra/mcp/tool_wake.go:734; только в tool_wake_test.go | высокий | оставить исключение до закрытия волны |
| 25 | internal/infra/mcp/WorktreeLeaseHandler | да | ожидает проводки | internal/infra/mcp/tool_claim.go:891; AttachWorktreeLease, tool_claim.go:993, только в tool_claim_test.go | высокий | оставить исключение до закрытия волны |
| 26 | internal/infra/wake/T210DisposingAdapter | да | ожидает проводки | internal/infra/wake/deliver.go:1017; вызов в T210settle, deliver.go:1128, цепочка до cmd/ обрывается на методе daemonWaker.Wake | высокий | оставить исключение до закрытия волны |
| 27 | internal/infra/wake/T210ObservingAdapter | да | ожидает проводки | internal/infra/wake/deliver.go:1013; вызов в T210settle, deliver.go:1125, цепочка до cmd/ обрывается на методе daemonWaker.Wake | высокий | оставить исключение до закрытия волны |

Итого: 7 удалённых, 2 подключённых, 18 ожидающих проводки. По признаку «присутствует в коде» 20 живых и 7 мёртвых. По признаку «достижим из cmd/» 2 живых и 25 мёртвых.

## План очистки

1. Семь удалённых символов. Проверка подтвердила отсутствие в коде. Коммит 9899cbdb уже удалил их записи из reachability.json на origin/main. Действие не требуется. Риск нулевой.

2. Два подключённых символа. `internal/infra/mcp/WakeHandler` и `internal/infra/mcp/NewWakeHandler` имеют ссылку из cmd/beacon/mcp.go:445,446. Исключения в reachability.json для них устарели. Предложить владельцу убрать две записи из конфига отдельным PR после одобрения. Код удалять нельзя, он работает. Риск низкий: гейт перестанет подавлять несуществующие находки, другие проверки не меняются.

3. Восемнадцать ожидающих символов. Все объявления на месте, доступ только из тестов или из внутренней цепочки deliver. Причины в конфиге пока описывают фактическое состояние. Предложить владельцу оставить 18 записей до закрытия волн Waves 6-8 и Wave 7. При закрытии каждой волны выбрать одно: подключить символ к composition root и убрать исключение, или удалить символ вместе с тестами и убрать исключение. Удаление кода сейчас сломает тесты и закроет путь для запланированной проводки. Риск высокий.

4. Порядок после решения владельца. Сначала удалить два устаревших исключения. Затем по мере закрытия волн обновлять оставшиеся 18 записей. Конфиг и бинарник beacon-gates в этом PR не трогаются. Карточка остаётся todo до решения владельца.
