# VCI-08. Цена опроса ghci и jitter в backoff

Premise: проверено 2026-11-23 по свежему `origin/main` `79dc16941bdbb5ff312f85e8602dda0e67be1a2a`. Источник требования: `/tmp/opencode/backlog/04-verdict-ci.md`, раздел VCI-08, PR 1883 и PR 1914. Повторная проверка по HEAD показала, что оба исправления уже в main. Дубль кода не пишется.

Доказательство:

- `gh pr view 1883 --json mergeCommit` даёт `68af732bed2dfec39954a5e362f9d2fdd0661149`, статус MERGED, заголовок `fix(ghci): bound retry budget after three consecutive errors (CV-08)`.
- `gh pr view 1914 --json mergeCommit` даёт `20161bb37aba6978d70c885069b4765bf6e3978f`, статус MERGED, заголовок `fix(ghci): add jitter to provider retry backoff (CV-03)`.
- `git merge-base --is-ancestor <SHA> origin/main` вернул 0 для обоих коммитов.
- `git log --all --grep='VCI-08' -i` не нашёл коммита с дублем. `gh pr list --state open --search 'ghci jitter'` не нашёл открытого PR с тем же исправлением.

Состояние в main: `internal/infra/ghci/provider.go` задаёт `maxJitter = time.Second` и `defaultMaxConsecutiveErrors = 3`. Поле `Config.Jitter` позволяет подменить генератор в тесте. `defaultJitter` берёт случайное число через `math/rand/v2` из интервала `[0, maxJitter]`. Строка `e.nextAllowed = now.Add(e.backoff + p.jitter())` добавляет разброс к сроку повтора и сохраняет потолок `maxBackoff`. После трёх ошибок подряд `PRState` возвращает `BudgetExhaustedError`, обнуляет `nextAllowed` и больше не запускает gh. Функция `fetch` делает ровно два вызова gh на один снимок: `pr view` и `pr checks`. Успешный результат кэшируется на `defaultCacheTTL`, поэтому несколько Watch на одном PR читают один снимок. Ошибка остаётся ошибкой, поэтому cibridge не публикует ready по устаревшему наблюдению.

Тесты в main: `TestProviderBackoffJitterSpreadsRetries`, `TestProviderBackoffWithoutJitterKeepsCeiling` и `TestProviderRetryBudgetExhausted` в `internal/infra/ghci/provider_test.go` фиксируют детерминированные генераторы, границы интервала и число вызовов gh.

Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/ghci/provider.go, internal/infra/ghci/provider_test.go
Зависимости: нет
Ссылка: /tmp/opencode/backlog/04-verdict-ci.md, PR 1883, PR 1914, карточки CV-03 и CV-08
Агент: vci08
Время: 2026-11-23
Цель: Ограничить цену опроса ghci и развести повторы провайдеров по времени.
Контекст: В исходном разрыве `consecutiveErrors` рос без границы, а срок повтора был одинаковым у всех провайдеров. Исправления в PR 1883 и PR 1914 закрывают оба пункта. Требование «сохранить лимит два запроса GitHub, GraphQL-батч и внешний источник истины» относится к коду, который уже работает в main. Файл `internal/infra/cibridge/bridge.go` и зоны CN-06 не меняются.
Шаги: Сверить premise с живым origin/main через git merge-base --is-ancestor для SHA из PR 1883 и 1914. Подтвердить наличие jitter, бюджета повторов и двух вызовов gh в provider.go. Код не менять. Добавить новую карточку со статусом done, полными SHA и номером PR.
Проверка: gofmt -l на internal/infra/ghci/provider.go и provider_test.go не дал замечаний. Полные проверки проходят в CI на PR. Тесты с детерминированным генератором уже лежат в main.
Итог: Код в main закрывает требование. В PR только новая карточка с доказательством.
Причина: already landed in PR #1883 and PR #1914, merge-base --is-ancestor exit 0.

Приёмка: jitter присутствует, разброс лежит в интервале 0-1 с, число вызовов gh ограничено двумя на снимок и четырьмя после трёх ошибок, тесты с фиксированным генератором есть в main.
