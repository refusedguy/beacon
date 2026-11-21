# IDX-11. Вывод производных документов из git
Premise: проверено 2026-11-21 по origin/main (f032156). Три документа в docs/ порождаются генераторами, но хранятся в git и разошлись с источниками. docs/FACTS.md и docs/adrs/README.md пишет internal/infra/batchapply/apply.go: renderFacts (строка 870) и renderADRIndex (строка 882). docs/ROADMAP_STATUS.md пишет tools/roadmap-sync/main.go через applyRoadmap (строка 459). Generator batch-apply дополнительно вызывает git add с путями docs/adrs и docs/FACTS.md (строка 901), поэтому без правки генератора производные файлы вернутся в индекс. Docs gate пропускает docs/adrs/README.md и не проверяет FACTS.md и ROADMAP_STATUS.md, удаление безопасно для CI. В OwnerFiles сделано: три файла удалены из индекса и добавлены в .gitignore. Шаг «обновить генераторы и инструкции» требует правки других пакетов и не выполнен.
baseline: до правки `git ls-files docs/FACTS.md docs/ROADMAP_STATUS.md docs/adrs/README.md` выдавал все три файла, `grep -nE 'FACTS|ROADMAP|adrs' .gitignore` давал пусто. После правки `git ls-files` по этим путям пуст, в .gitignore три новые строки. `grep -nE 'docs/FACTS.md|adrs.*README|ROADMAP_STATUS' internal/infra/batchapply/apply.go tools/roadmap-sync/main.go` = строки 870, 882, 901 и 459. `grep -n 'README.md' tools/beacon-gates/docsgate/docs.go` = 253, файл пропускается гейтом. Дубля нет. Живой зонд не нужен, правка только текстовая, gofmt не aplicable.
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: docs/FACTS.md, docs/ROADMAP_STATUS.md, docs/adrs/README.md, .gitignore
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Производные файлы перестают коммититься и генерируются по запросу.
Контекст: Premise: владелец жалуется на засорение git истории. Три документа уже расходятся с источником.
Шаги: Удалить сгенерированные файлы из индекса, добавить их в gitignore, обновить генераторы и инструкции.
Проверка: gofmt не aplicable, docs gate в CI.
Приёмка: В репозитории остаются только источники данных, производные файлы создаются командой.

## Блокер

- 2026-11-21. Шаг «обновить генераторы и инструкции» не выполнен: он требует правки файлов вне OwnerFiles карточки.

  Сделано в OwnerFiles: `git rm docs/FACTS.md docs/ROADMAP_STATUS.md docs/adrs/README.md` убрал файлы из индекса и рабочего дерева, в `.gitignore` добавлены три строки с этими путями. Проверка `git ls-files` по ним пуста. Docs gate пропускает `docs/adrs/README.md` (tools/beacon-gates/docsgate/docs.go:253) и не проверяет два других файла, поэтому удаление безопасно для CI.

  Чтобы производные файлы перестали коммититься, нужна правка генераторов:
  - `internal/infra/batchapply/apply.go`: строка 870 пишет `docs/FACTS.md`, строка 882 пишет `docs/adrs/README.md`, строка 901 выполняет `git add docs/adrs docs/tasks docs/evals docs/FACTS.md` и возвращает оба файла в индекс при флаге `--commit`.
  - `tools/roadmap-sync/main.go`: строка 459 задаёт путь `docs/ROADMAP_STATUS.md` для записи при флаге `--write`.

  Ссылки на эти пути в инструкциях `AGENTS.md` и `README.md` входят в карточку IDX-14 и здесь не менялись.

  Статус todo сохранён. Чужие файлы, пакеты генераторов, wiring и `.beacon` не тронуты, код не писался, gofmt не применялся, дубль не создан. Блокер возвращён оркестратору с именами файлов `internal/infra/batchapply/apply.go` и `tools/roadmap-sync/main.go`.
