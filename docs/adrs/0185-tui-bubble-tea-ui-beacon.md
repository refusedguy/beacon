# ADR 185: TUI на Bubble Tea как основной UI менеджера Beacon

Статус: proposed

## Контекст

Ядро на Go, MCP основной интерфейс, цель footprint меньше 100 MB, 15+ агентов. beacon task по ADR 0016 остается на стандартной библиотеке, поэтому UI не может жить в том же бинарнике. Crush на Go использует bubbletea/v2, lipgloss/v2, bubbles/v2, glamour/v2. openrig и t3code и OpenCode TUI на TypeScript, ratatui на Rust, web UI требует браузер.

## Решение

Собрать отдельный бинарник beacon-ui на Bubble Tea v2, Lip Gloss v2 и Bubbles v2. Читать state SQLite только на чтение, получать обновления по локальному control socket или loopback HTTP, действия делать идемпотентными по watchID и revision. wish подключить во второй фазе как SSH-транспорт к той же модели.

## Последствия

Появляется второй бинарник и внешние Go-модули вне ядра. Нужны прототипное сравнение с tview, гейт размера в CI и тесты рендера. Web UI и desktop GUI остаются за рамками решения, пока действует лимит footprint.
