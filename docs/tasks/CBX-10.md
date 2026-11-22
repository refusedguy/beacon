# CBX-10. Каноническое сравнение remote URL вместо сравнения строк

Статус: todo
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/prcreate.go, internal/infra/github/repo_detect.go, internal/infra/github/repo_detect_test.go
Зависимости: 
Ссылка: https://github.com/gastownhall/beads (коммит 78624109904655d7a4609f27d34eb0982cd8963f)
Агент: нет
Время: нет
Baseline: beads: internal/doltremote/remote.go:121-133 перечисляет шесть классов эквивалентности URL; beacon parseRepoSlug в cmd/beacon/prcreate.go:79-102 не снимает credentials и не складывает регистр хоста, поэтому https://user:pass@github.com/org/repo даёт slug pass@github.com/org.

Цель: Распознавать один и тот же репозиторий, записанный в разных схемах, с credentials и в SCP-форме.

Контекст: Источник, реальный код beads. Normalize в internal/doltremote/remote.go:33-59 возвращает dolt-native схемы как есть и добавляет git+ только к git-формам; SCP-распознавание на :96-119 опирается на признак '://' и не путает путь с @ с SCP-адресом. CanonicalForComparison на :121-140 снимает завершающий слэш и .git, убирает credentials и складывает регистр хоста. stripCredentialsAndFoldHostCase на :142-170 сохраняет пользователя SSH, кроме канонического git@, и не трогает http и https как разные схемы. Разрыв в beacon: parseRepoSlug на cmd/beacon/prcreate.go:81-102 извлекает owner/name из тремя способами, но не имеет ни Normalize, ни CanonicalForComparison; DetectRepoSlugWithOverride в internal/infra/github/repo_detect.go:35-53 сравнивает уже извлечённый slug.

Шаги: Вынести Normalize, CanonicalForComparison и Redact в отдельный файл пакета github. Сохранить users SSH, снимать credentials HTTP. Не складывать http и https. Добавить тест по корпусу из шести форм beads. Использовать каноническую форму при сравнении remote и сконфигурированного repo.

Проверка: Измеримая. Корпус из шести форм, перечисленных в remote.go:124-129, даёт одно каноническое значение. https и http дают два разных значения. https://user:pass@github.com/org/repo.git и https://github.com/org/repo дают одинаковое значение и slug org/repo. s3://bucket/team@prod не превращается в git+ssh. Тест фиксирует восемь известных ограничений SCP-грамматики.

Приёмка: Redaction в internal/infra/observe/log.go:97-100 остаётся отдельной задачей логов; здесь только сравнение адресов.
