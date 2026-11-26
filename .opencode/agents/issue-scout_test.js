// T3.38: тест прав профиля issue-scout.
// Проверка по карточке: node --test .opencode/agents/issue-scout_test.js
// В CI тест подхватывается через tests/adapter/issue-scout.test.js.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const AGENT_PATH = resolve(dirname(SELF), "issue-scout.md")
const text = readFileSync(AGENT_PATH, "utf8")

function frontmatter() {
  const match = text.match(/^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/)
  assert.ok(match, "issue-scout.md должен начинаться с блока frontmatter ---")
  return match[1]
}

function field(name) {
  const match = frontmatter().match(new RegExp(`^${name}:\\s*(.+?)\\s*$`, "m"))
  assert.ok(match, `frontmatter должен содержать ${name}`)
  return match[1]
}

// Разбирает строки вида "- action: X" / "resource: Y" / "effect: Z".
function unquote(value) {
  return value.replace(/^["']|["']$/g, "")
}

function rules() {
  const found = []
  const pattern = /-\s*action:\s*(\S+)\s*\r?\n\s*resource:\s*(\S+)\s*\r?\n\s*effect:\s*(\S+)/g
  let match
  while ((match = pattern.exec(frontmatter())) !== null) {
    found.push({
      action: unquote(match[1]),
      resource: unquote(match[2]),
      effect: unquote(match[3]),
    })
  }
  return found
}

test("T3.38: issue-scout работает в режиме subagent", () => {
  assert.equal(field("mode"), "subagent")
})

test("T3.38: прав для edit, shell и write нет: всё запрещено", () => {
  const found = rules()
  for (const action of ["edit", "shell", "write"]) {
    const matches = found.filter((item) => item.action === action)
    assert.ok(matches.length >= 1, `должно быть правило запрета для ${action}`)
    for (const item of matches) {
      assert.equal(item.effect, "deny", `${action} обязан быть deny`)
      assert.equal(item.resource, "*", `${action} обязан покрывать все ресурсы`)
    }
  }
})

test("T3.38: профиль описан как read-only исследование issue и PR", () => {
  assert.match(field("description"), /read-only/i)
  const body = text.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n?/, "")
  assert.match(body, /issue/i)
  assert.match(body, /pull request|PR/i)
  assert.match(body, /never edit or write files/i)
})
