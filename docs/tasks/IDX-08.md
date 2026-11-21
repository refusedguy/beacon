# IDX-08. Автоматический перевод карточки в done при мерже
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/cibridge/bridge_test.go
Зависимости: 
Ссылка: 
Агент: ses_e02a32d79ffeJC7FqYlt8kIVzU
Время: 2026-11-21
Цель: Терминальный вердикт merged закрывает связанную карточку без агента.
Контекст: Premise: владелец прямо сказал: Beacon когда замерджил, задачу в дон перевёл. IDX-07 влит в main (PR #902, commit fa7fa9a), internal/infra/store/card_links.go существует с методами Link и Unlink. baseline: до правки в internal/infra/cibridge было 0 портов card links и 0 вызовов закрытия карточки на merged. Подход повторяет существующие порты: blockerControl и Rebaser объявляются в cibridge, адаптер связывает их в composition root.
Шаги: Добавлен порт CardLinks с методом CloseMerged в internal/infra/cibridge/bridge.go. closeWatch вызывает порт только при verdict=merged, поэтому MERGED из GitHub и успешный авто-мерж закрывают карточку, а hold её не трогает. Порт получает watchID, PR и время. Адаптер находит карточку по card_links, ставит done и записывает PR в подтверждение. Непривязанный порт ничего не меняет в работе Watch. Тесты в bridge_test.go покрывают MERGED из GitHub, успешный авто-мерж и hold.
Проверка: gofmt -w применён к обоим .go файлам. go test и go build локально не запускались по правилу AGENTS.md, проверки прогоняет CI на PR.
Приёмка: После merged карточка done с ссылкой на PR, при hold статус не меняется. Тесты фиксируют оба исхода.
