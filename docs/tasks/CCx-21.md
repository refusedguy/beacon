# CCx-21. Версионированная запись владельца worktree
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/worktree/marker.go, internal/infra/worktree/marker_version_test.go
Зависимости: CC-02
Ссылка: docs/research/comp-codex.md, codex-rs/worktree/src/metadata.rs:19-27, :40-44
Агент: ses_ccx21c_executor
Время: 2026-11-22T18:23:01Z
Цель: Отвергать записи маркера с неизвестной версией или лишними полями.
Контекст: Источник: docs/research/comp-codex.md. Codex хранит версионированную запись `OwnerRecord` с атрибутом `deny_unknown_fields` (`codex-rs/worktree/src/metadata.rs:19-27`). Чтение отвергает запись, где версия не равна `OWNER_VERSION` или пуст `owner_thread_id` (`:40-44`). Beacon хранит `Marker` из трёх полей (`internal/infra/worktree/marker.go:31-35`) без версии. `json.Unmarshal` (`marker.go:83-86`) молча игнорирует неизвестные поля, поэтому повреждённая запись выглядит валидной. Baseline: 0 полей версии, 0 отказов по неизвестным полям.
Шаги: Добавить поле версии в `Marker`. Запретить неизвестные поля при разборе. Отвергать записи с неподходящей версией так же, как corrupt JSON. Сохранить формат пути по хэшу и TTL. Покрыть тестом на новую, старую и расширенную запись.
Проверка: CI зелёный. Тест показывает 3 исхода: 1 принятая запись текущей версии, 1 отказ для старой версии, 1 отказ для записи с лишним полем.
Приёмка: Изменения только в OwnerFiles карточки. Ранее записанные маркеры без версии должны получать понятную ошибку, а не тихий пропуск.

Причина: Landed in PR #1817, merge commit 8acc95fd8bbbea6fc19848dd1d4013d5f336e459. git merge-base --is-ancestor 8acc95fd... origin/main exits 0. internal/infra/worktree/marker.go already has MarkerVersion, DisallowUnknownFields, ErrMarkerVersion and marker_version_test.go exists in origin/main. No duplicate code written; card close fields shipped in a docs-only PR.
Закрыл: ses_ccx21c_executor
Время закрытия: 2026-11-22T18:23:26Z