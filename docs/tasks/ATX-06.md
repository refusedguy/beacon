# ATX-06. Заменить time.Sleep(1ms) барьером в TestWebhookConcurrentRedeliverySingleEffect
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/github/points_test.go
Зависимости: 
Ссылка: 
Агент: exe87
Время: 2026-11-21T17:50:21Z
Цель: Сделать параллельную доставку 16 горутин детерминированной.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 5.6 и строка таблицы 154. internal/infra/github/points_test.go:602 содержит time.Sleep(1ms) в fake source, который специально расширяет гонку. Тест проверяет, что повторных доставок ноль. Длительность цикла зависит от нагрузки машины, поэтому на медленной машине итераций меньше. Baseline: 16 горутин, задержка 1 мс.
Шаги: Добавить atomic.Int64 счётчик прибывших и канал release. Каждая горутина увеличивает счётчик и ждёт release. Закрыть release, когда до source дошли все 16 горутин. Передавать в deliverPointer фиксированную метку времени.
Проверка: CI зелёный. В points_test.go нет time.Sleep в fake source.
Приёмка: Все горутины доходят до source до продолжения. Синхронизация не зависит от реального времени.

Итог: Уже влито в PR #1210, коммит 1d960da335e46a9141d1ed2c4db469e87a18926b
Доказательство: 1d960da3 в origin/main, merge-base --is-ancestor=0; TestWebhookConcurrentRedeliverySingleEffect использует канальный барьер ready/start и не содержит time.Sleep в fake source
