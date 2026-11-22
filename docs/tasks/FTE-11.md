# FTE-11. Включить окно отсрочки в gc-worktree
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/worktree/gc.go, cmd/beacon/gc.go, internal/infra/worktree/grace_test.go
Зависимости:
Ссылка: docs/runbooks/worktree-gc.md:715-719, docs/tasks/CD-02.md:14
Агент: ses_dfbdfd242ffeUN2VfCgTA3anP0
Время: 2026-11-22T16:40:09Z
baseline: internal/infra/worktree/grace.go:8 задаёт DefaultGrace равным 30 с. Guard.Check определена в :54. git grep NewGuard по production-каталогам находит только объявление в grace.go:41, 0 точек вызова. cmd/beacon/gc.go:634 вызывает worktree.Remove напрямую.
Цель: Сборщик удаляет чистый worktree только после окна отсрочки и второй проверки git status --porcelain.
Контекст: docs/runbooks/worktree-gc.md:717 обещает, что кандидат ждёт 30 с и проходит вторую проверку перед git worktree remove. Там же прямо сказано, что пока сборщик не вызывает Guard.Check, отсрочка не влияет на уборку. Сейчас cmd/beacon/gc.go:796 удаляет записи плана сразу после --apply. Любое изменение в каталоге между проверками не снимает кандидата.
Шаги: Подключить Guard к gcWorktreeCollect или к пути apply через внедрённые gcWorktreeStatus и gcWorktreeRemove. Передавать ObservedAt из первого снимка статуса. Обрабатывать все пять исходов GuardOutcome в отчёте. Сохранить dry-run без записи. Покрыть тестом на свежего кандидата, изменившийся каталог и ошибку статуса. Не трогать headguard.go и marker.go.
Проверка: Свежий чистый worktree остаётся в отчёте с причиной deferred. После 30 с без изменений он удаляется. Изменение статуса между проверками даёт reason changed и оставляет каталог. Ошибка статуса не удаляет каталог.
Приёмка: Окно отсрочки работает в production, runbook совпадает с кодом, CI зелёный.

Причина: Уже влито в origin/main: PR #1801 (fix/fte-11), коммит 75ab4c30c7b7401bdcf23f4d7b833f82323bb202 fix(gc-worktree): apply grace window before removal (FTE-11). merge-base --is-ancestor 75ab4c30 origin/main = 0. Дубли кода нет.
Закрыл: ses_dfbdfd242ffeUN2VfCgTA3anP0
Время закрытия: 2026-11-22T16:40:15Z