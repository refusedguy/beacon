// Тест формата T3.35: SKILL.md с frontmatter name/description и процедурой разбора issue.
// Проверка по карточке: node --test .opencode/skills/night-shift/SKILL_test.js
// В CI тест подхватывается через tests/adapter/night-shift-skill.test.js.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const SKILL_PATH = resolve(dirname(SELF), "SKILL.md")

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

test("T3.35 SKILL.md: frontmatter содержит name и description", () => {
  assert.equal(field("name"), "night-shift")
  assert.match(
    field("description"),
    /triage and clear GitHub issues in the night shift/i,
  )
})

test("T3.35 SKILL.md: тело содержит процедуру разбора issue", () => {
  const body = text.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n?/, "")
  assert.match(body, /^##\s+Процедура/m)
  for (const marker of ["gh issue view", "PR", "CI", "новом факте"]) {
    assert.ok(body.includes(marker), `процедура должна упоминать ${marker}`)
  }
  const steps = body.split("\n").filter((line) => /^\d+\./.test(line.trim()))
  assert.ok(steps.length >= 3, "процедура должна содержать не менее трёх шагов")
})
