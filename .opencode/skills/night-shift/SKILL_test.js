// Тест формата T3.35: SKILL.md с frontmatter name/description и процедурой разбора issue.
// Проверка по карточке: node --test .opencode/skills/night-shift/SKILL_test.js
// В CI тест подхватывается через tests/adapter/night-shift-skill.test.js.

import { test } from "node:test"
import assert from "node:assert/strict"
import { existsSync, readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const SKILL_DIR = dirname(SELF)
const SKILL_PATH = resolve(SKILL_DIR, "SKILL.md")

const text = readFileSync(SKILL_PATH, "utf8")

function frontmatter() {
  const match = text.match(/^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/)
  assert.ok(match, "SKILL.md должен начинаться с блока frontmatter ---")
  return match[1]
}

function field(name) {
  const match = frontmatter().match(new RegExp(`^${name}:\\s*(.+?)\\s*$`, "m"))
  assert.ok(match, `frontmatter должен содержать ${name}`)
  return match[1]
}

function body() {
  return text.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n?/, "")
}

test("T3.35 SKILL.md: frontmatter содержит name и description", () => {
  assert.equal(field("name"), "night-shift")
  assert.match(
    field("description"),
    /triage and clear GitHub issues in the night shift/i,
  )
})

test("T3.35 SKILL.md: тело содержит процедуру разбора issue", () => {
  const route = body()
  assert.match(route, /^##\s+Процедура/m)
  for (const marker of ["gh issue view", "PR", "CI", "новом факте"]) {
    assert.ok(route.includes(marker), `процедура должна упоминать ${marker}`)
  }
  const steps = route.split("\n").filter((line) => /^\d+\./.test(line.trim()))
  assert.ok(steps.length >= 3, "процедура должна содержать не менее трёх шагов")
})

test("T3.35 SKILL.md: справочники подключены по ссылке перед действием", () => {
  const route = body()
  assert.match(route, /Перед комментарием прочитать `references\/comment-rules\.md`/)
  assert.match(route, /Перед мержем прочитать `references\/pr-readiness\.md`/)
  for (const name of ["comment-rules.md", "pr-readiness.md"]) {
    const path = resolve(SKILL_DIR, "references", name)
    assert.equal(existsSync(path), true, `справочник должен существовать: ${name}`)
  }
})
