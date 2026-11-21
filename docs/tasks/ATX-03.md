# ATX-03. Убрать опрос лога и TOCTOU порта в e2e старте opencode serve
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: tests/e2e/e2e_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать запуск opencode serve в e2e детерминированным и не засорять общий каталог /tmp/opencode.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.1 и строка таблицы 151. tests/e2e/e2e_test.go:340 и :350 читают лог с диска и ищут регуляркой server password и server listening, спят 200 мс в цикле с дедлайтом 30 с. freePort в :132 закрывает слушатель перед повторным bind. tmpRoot в :79 и newBase в :88 используют os.MkdirTemp без t.Cleanup. ID из time.Now().UnixNano() в :673, :895, :896 и :929. Тот же класс ошибки зафиксирован коммитом c316617c.
Шаги: Сканировать stdout и stderr процесса через канал и ждать конкретную строку. Дедлайн передать через context.WithTimeout. Заменить freePort на слушатель без close-rebind. Добавить t.Cleanup(os.RemoveAll) сразу после MkdirTemp. Заменить ID на фиксированные строки, уникальность обеспечивает base.
Проверка: CI зелёный. В tests/e2e/e2e_test.go нет цикла опроса лога с time.Sleep и нет ID из UnixNano.
Приёмка: Готовность сервера приходит по каналу. Каталог e2e удаляется после падения теста. Порт не освобождается между закрытием и bind.
