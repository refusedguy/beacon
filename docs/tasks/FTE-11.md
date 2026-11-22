# FTE-11. Включить окно отсрочки в gc-worktree
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/worktree/gc.go, cmd/beacon/gc.go, internal/infra/worktree/grace_test.go
Зависимости:
Ссылка: docs/runbooks/worktree-gc.md:715-719, docs/tasks/CD-02.md:14
Агент: нет
Время: нет
baseline: internal/infra/worktree/grace.go:8 задаёт DefaultGrace равным 30 с. Guard.Check определена в :54. git grep NewGuard по production-каталогам находит только объявление в grace.go:41, 0 точек вызова. cmd/beacon/gc.go:634 вызывает worktree.Remove напрямую.
Цель: Сборщик удаляет чистый worktree только после окна отсрочки и второй проверки git status --porcelain.
Контекст: docs/runbooks/worktree-gc.md:717 обещает, что кандидат ждёт 30 с и проходит вторую проверку перед git worktree remove. Там же прямо сказано, что пока сборщик не вызывает Guard.Check, отсрочка не влияет на уборку. Сейчас cmd/beacon/gc.go:796 удаляет записи плана сразу после --apply. Любое изменение в каталоге между проверками не снимает кандидата.
Шаги: Подключить Guard к gcWorktreeCollect или к пути apply через внедрённые gcWorktreeStatus и gcWorktreeRemove. Передавать ObservedAt из первого снимка статуса. Обрабатывать все пять исходов GuardOutcome в отчёте. Сохранить dry-run без записи. Покрыть тестом на свежего кандидата, изменившийся каталог и ошибку статуса. Не трогать headguard.go и marker.go.
Проверка: Свежий чистый worktree остаётся в отчёте с причиной deferred. После 30 с без изменений он удаляется. Изменение статуса между проверками даёт reason changed и оставляет каталог. Ошибка статуса не удаляет каталог.
Приёмка: Окно отсрочки работает в production, runbook совпадает с кодом, CI зелёный.
