# ATX-C02. Убрать дублирование setup-go и сетевую нестабильность e2e
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: .github/workflows/e2e.yml
Зависимости: 
Ссылка: docs/research/ci-gates-gaps.md, находка C02; PR #1235, #1237, #1239
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Перевести e2e на общий экшен подготовки Go и убрать повторную загрузку инструментов из сети.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C02. Замер из `docs/research/ci-speed-audit.md:32`: e2e имеет медиану 90 с и максимум 104 с по 56 прогонам, шаг Run e2e занимает 68 с. `.github/workflows/e2e.yml:45-48` вызывает `actions/setup-go@v5` напрямую с `cache: false`, хотя #1237 перевёл остальные workflow на `.github/actions/go-runner-setup`, а #1235 включил кэш. Шаг Install opencode скачивает tarball без `--max-time`.
Шаги: Заменить шаг Set up Go на `./.github/actions/go-runner-setup`. Убрать `cache: false`. Оставить setup-node без изменений. Добавить `--max-time` к curl и кэшировать распакованный бинарник opencode по версии из шага. Сохранить path-фильтры, расписание, `timeout-minutes: 5` и `continue-on-error`.
Проверка: CI зелёный. `rg -n 'actions/setup-go|cache: false' .github/workflows/e2e.yml` даёт 0 строк. PR с изменением `tests/e2e/**` по-прежнему запускает E2E. Лог Set up Go показывает восстановление кэша.
Приёмка: Все определения setup-go находятся в одном композитном экшене. e2e запускается по тем же триггерам и не зависит от повторной загрузки toolchain.
