// Адаптер-тест T3.38: подключает канонический тест прав issue-scout к CI.
// ci.yml запускает tests/adapter/*.test.js; импорт регистрирует те же проверки.
// Канонический файл карточки: .opencode/agents/issue-scout_test.js.

await import("../../.opencode/agents/issue-scout_test.js")
