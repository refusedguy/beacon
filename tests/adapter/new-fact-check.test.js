// T3.39. Детерминированная проверка "только при новом факте" перед
// комментарием и мержем. Тест лежит в tests/adapter, поэтому CI его запускает.
// Тот же файл служит обработчиком хука из .opencode/settings.json:
//   node tests/adapter/new-fact-check.test.js --check "текст комментария"
// Без аргумента текст читается из stdin.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const SETTINGS_PATH = join(dirname(fileURLToPath(import.meta.url)), "..", "..", ".opencode", "settings.json")

export function loadSettings(path = SETTINGS_PATH) {
  return JSON.parse(readFileSync(path, "utf8"))
}

// Правила целиком лежат в settings.json. Код не добавляет своих исключений:
// и тест, и обработчик хука читают один и тот же конфиг.
export function evaluateComment(text, settings = loadSettings()) {
  const value = typeof text === "string" ? text : ""
  const check = settings.checks.newFact
  const hasPhrase = (pattern) => new RegExp(pattern, "iu").test(value)
  const template = check.templatePhrases.some(hasPhrase)
  const facts = Object.entries(check.factPatterns)
    .filter(([, pattern]) => hasPhrase(pattern))
    .map(([kind]) => kind)
  const decision = facts.length > 0 ? "allow" : "deny"
  return {
    decision,
    reason: decision === "allow"
      ? `найден новый факт: ${facts.join(", ")}`
      : "нового факта нет",
    template,
    facts,
  }
}

function checkArg() {
  const args = process.argv.slice(2)
  const index = args.indexOf("--check")
  if (index < 0) return null
  return args.length > index + 1
    ? args.slice(index + 1).join(" ")
    : readFileSync(0, "utf8")
}

const text = checkArg()
if (text !== null) {
  const result = evaluateComment(text)
  process.stdout.write(JSON.stringify({
    permissionDecision: result.decision,
    reason: result.reason,
  }) + "\n")
} else {
  test("T3.39 конфиг: хук комментария и мержа отклоняет без факта", () => {
    const hooks = loadSettings().hooks.PreToolUse
    assert.equal(hooks.length, 1)
    assert.match(hooks[0].matcher, /issue comment/)
    assert.match(hooks[0].matcher, /pr merge/)
    assert.match(hooks[0].handler, /new-fact-check\.test\.js --check/)
    assert.equal(hooks[0].on.noNewFact.permissionDecision, "deny")
  })

  test("T3.39 шаблонный комментарий без нового факта не проходит", () => {
    const result = evaluateComment("Прогресс: PR 123 влит, CI зелёный")
    assert.equal(result.template, true)
    assert.equal(result.decision, "deny")
    assert.deepEqual(result.facts, [])
  })

  test("T3.39 номер PR сам по себе не является новым фактом", () => {
    assert.equal(evaluateComment("PR 123").decision, "deny")
  })

  test("T3.39 комментарий с хешем влитого PR проходит", () => {
    const result = evaluateComment(
      "Прогресс: PR 123 влит, CI зелёный. Хеш коммита 1a2b3c4d.",
    )
    assert.equal(result.decision, "allow")
    assert.ok(result.facts.includes("mergedPRHash"))
  })

  test("T3.39 комментарий с измерением проходит", () => {
    const result = evaluateComment("Замерил p95: 120 мс до правки, 80 мс после.")
    assert.equal(result.decision, "allow")
    assert.ok(result.facts.includes("measurement"))
  })

  test("T3.39 комментарий с решением владельца или блокером проходит", () => {
    const decision = evaluateComment("Решение владельца: оставить гейт включённым.").decision
    const blocked = evaluateComment("Блокер: нет доступа к runner.")
    assert.equal(decision, "allow")
    assert.equal(blocked.decision, "allow")
    assert.ok(blocked.facts.includes("blocker"))
  })
}
