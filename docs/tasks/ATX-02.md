# ATX-02. Сохранять installedServerEnsurer в t3145RestorePorts
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/main_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать зависимость тестов пакета cmd/beacon от порядка следования.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 3.2 и строка таблицы 150. Хелпер в cmd/beacon/main_test.go:542 сохраняет watchLiveSource, spawnSessionSpawner и deliverTarget, но не installedServerEnsurer. Тесты в :723 и :750 пишут nil в cleanup вместо прежнего значения. Переменная в :129 проверяется как индикатор чистого состояния. Baseline: детерминированный отказ TestT3152InstallOpenCodePortsLeavesReaderClientUnwired в :757, подтверждённый в concurrency-audit.md:124.
Шаги: Расширить t3145RestorePorts до всех четырёх портов. Убрать т.Cleanup(func() { SetServerEnsurer(nil) }) из тестов в :723 и :750. Полная правка: заменить четыре globals на opencodePortRegistry, который installOpenCodePorts получает аргументом.
Проверка: CI зелёный. Поиск SetServerEnsurer(nil) в cmd/beacon/main_test.go не находит вызовов в cleanup тестов.
Приёмка: Хелпер восстанавливает все четыре порта. Тест проходит при любом порядке тестов в пакете.

Итог: Уже влито в PR #1208
Доказательство: afeba0d6 в origin/main, merge-base --is-ancestor=0