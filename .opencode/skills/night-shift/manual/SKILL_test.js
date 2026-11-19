// Тест T3.36: ручной скилл ночной смены не вызывается моделью автоматически.
// Проверяет frontmatter `.opencode/skills/night-shift/manual/SKILL.md`.
// Локальный запуск: node --test .opencode/skills/night-shift/manual/SKILL_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const SKILL_PATH = resolve(dirname(SELF), "SKILL.md")
const text = readFileSync(SKILL_PATH, "utf8")

function frontmatterLines() {
  const m = text.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n/)
  assert.ok(m, "SKILL.md must begin with a YAML frontmatter block")
  return m[1].split(/\r?\n/)
}

test("T3.36 ручной скилл: disable-model-invocation равен true", () => {
  const lines = frontmatterLines()
  const flag = lines.filter((line) => /^disable-model-invocation\s*:/.test(line))
  assert.equal(flag.length, 1, "flag must appear exactly once in frontmatter")
  assert.match(
    flag[0],
    /^disable-model-invocation:\s*true\s*$/,
    "manual skill must set disable-model-invocation: true"
  )
})

test("T3.36 ручной скилл: есть name и description", () => {
  const lines = frontmatterLines()
  assert.ok(
    lines.some((line) => /^name:\s*\S/.test(line)),
    "frontmatter must declare name"
  )
  assert.ok(
    lines.some((line) => /^description:\s*\S/.test(line)),
    "frontmatter must declare description"
  )
})
