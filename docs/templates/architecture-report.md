# Шаблон отчёта по архитектурным кандидатам

Отчёт показывает кандидатов на углубление до выбора одного. Каждый кандидат получает карточку из шести полей и метку силы рекомендации. Финальный раздел называет главную рекомендацию. Отчёт живёт во временном каталоге ОС и не попадает в репозиторий. В репозиторий кладётся только итоговая карточка задачи.

Основа: `mattpocock/skills@abeeaf9`, `skills/engineering/improve-codebase-architecture/SKILL.md:37-60`. Для слов об архитектуре используй `codebase-design`, для слов предметной области `GLOSSARY.md`. Выбор интерфейса после выбора кандидата описан в CMx-04 (`docs/design/design-it-twice.md`).

<!-- architecture-report:contract
report-location: os-temp
tmpdir-env: TMPDIR
tmpdir-fallback: /tmp
filename: architecture-review-<timestamp>.html
open: true
report-absolute-path: true
commit-to-repo: false
repo-artifact: task-card
field-count: 6
strength-values: Strong, Worth exploring, Speculative
final-section: Top recommendation
interfaces-before-choice: forbidden
interface-card: CMx-04
architecture-report:contract -->

## Где писать отчёт

Пиши самодостаточный HTML-файл во временный каталог ОС. Возьми каталог из `$TMPDIR`. Если переменной нет, используй `/tmp`. Имя файла: `architecture-review-<timestamp>.html`, где `<timestamp>` уникален для каждого запуска.

Открой файл и сообщи абсолютный путь. Не коммить HTML-файл. Он временный и живёт вне дерева. В репозиторий положи только итоговую карточку задачи с принятым решением.

## Визуализация

Подключи Tailwind и Mermaid через CDN. У каждого кандидата должна быть визуализация до и после. Схемы, связи и последовательности рисуй Mermaid. Масс-диаграммы, разрезы и редакционные иллюстрации делай вручную на CSS или SVG.

## Карточка кандидата

Для каждого кандидата заполни шесть полей. Не пропускай ни одного поля.

### Files

<какие файлы и модули участвуют>

### Problem

<какое трение создаёт текущая архитектура>

### Solution

<простыми словами, что изменится>

### Benefits

<как вырастут locality и leverage, как улучшатся тесты>

### Before / After diagram

<две схемы рядом: текущая мелкая структура и углублённая>

### Recommendation strength

<одна из трёх меток: Strong, Worth exploring, Speculative>

## Сила рекомендации

Используй ровно три значения.

- `Strong`. Углубление решает реальную проблему, выгода очевидна.
- `Worth exploring`. Проблема есть, нужна проверка ограничений.
- `Speculative`. Идея гипотетическая, данные слабые.

Выведи метку бейджем в карточке кандидата. Не придумывай четвёртое значение.

## Top recommendation

<какого кандидата взять первым и почему>

Раздел завершает отчёт. Назови одного кандидата и объясни выбор. Не оставляй список на выбор.

## Словарь, ADR и граница интерфейсов

Используй слова из `GLOSSARY.md` для предметной области. Используй слова из `codebase-design` для архитектуры: module, interface, depth, seam, adapter, leverage, locality. Не подменяй их словами component, service, API или boundary.

Если кандидат противоречит существующему ADR, отметь конфликт в карточке явно. Формулируй пометку так: `противоречит ADR-NNNN, но стоит пересмотреть, потому что ...`. Не перечисляй теоретические рефакторинги, которые ADR запрещает. Показывай только конфликт с реальным трением.

Не предлагай интерфейсы до выбора кандидата. После того как отчёт записан, спроси, какого кандидата разобрать. Когда кандидат выбран, переходи к проектированию интерфейса по CMx-04 (`docs/design/design-it-twice.md`). Не повторяй в этом отчёте проверки глубины из CM-03.

<!-- architecture-report:checks
six-fields: Files, Problem, Solution, Benefits, Before-After-diagram, Recommendation-strength
strength-values: Strong, Worth exploring, Speculative
temp-file-rule: os-temp-not-committed
final-section: Top recommendation
adr-conflict-marked: true
interface-pointer: CMx-04
architecture-report:checks -->
