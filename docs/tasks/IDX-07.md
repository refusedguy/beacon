# IDX-07. Связь карточки с Watch и PR
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/card_links.go, internal/infra/store/card_links_test.go
Зависимости: 
Ссылка: 
Агент: ses_e02a36092ffeSeM7heDJ01J18H
Время: 2026-11-21
Цель: Таблица card_id, watch_id, pr с уникальным ключом и транзакционной записью.
Контекст: Premise: проверено 2026-11-21 по origin/main (f032156). Связки карточки с Watch в store нет: нет таблицы card_links, методов Link и Unlink и столбца card_id. Мерж не может закрыть карточку, потому что в store нечего закрывать. Дубля нет.
baseline: до правки в internal/infra/store было 0 таблиц card_links, 0 методов Link и Unlink и 0 столбцов card_id. Подход не новый: watch_bindings.go и webhook_events.go создают свои таблицы на первый вызов и пишут через d.Write с INSERT OR IGNORE. Поэтому db.go не менялся.
Шаги: Добавлен internal/infra/store/card_links.go. Таблица card_links хранит card_id как PRIMARY KEY, watch_id и pr. Таблица создаётся в первой же транзакции через CREATE TABLE IF NOT EXISTS. Link пишет через INSERT OR IGNORE и возвращает Created для первой записи или Duplicate с сохранённой строкой для повтора. Unlink удаляет связь по card_id и возвращает, была ли строка. Оба метода отклоняют пустые card_id и watch_id. Тесты в card_links_test.go покрывают повтор Link, повтор с другим Watch, пустые ключи, 16 параллельных Link на одну карточку, 16 параллельных Link на разные карточки и параллельные Link с Unlink.
Проверка: gofmt -w применён к обоим файлам. go test и go build локально не запускались по правилу AGENTS.md, проверки прогоняет CI на PR.
Приёмка: Одна карточка связана с одним Watch. Повтор Link не создаёт вторую запись: 16 параллельных вызовов дают ровно одну строку и ровно один Created. Unlink удаляет связь, повтор Unlink ничего не меняет.
