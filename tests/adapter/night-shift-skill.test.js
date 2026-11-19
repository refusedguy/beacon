// Адаптер-тест T3.35: подключает канонический тест формата скилла к CI.
// ci.yml запускает tests/adapter/*.test.js; импорт регистрирует те же проверки.
// Канонический файл карточки: .opencode/skills/night-shift/SKILL_test.js.

await import("../../.opencode/skills/night-shift/SKILL_test.js")
