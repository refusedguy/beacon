// Адаптер-тест T3.7: проверка шаблона задания исполнителя.
// Запуск в CI: шаг "Test JS adapters" подхватывает tests/adapter/*.test.js.
//
// Источники: карточка T3.7, WORKER-PROMPT §4.7, ADR 0004, ADR 0019, ADR 0021.
// Тест читает docs/templates/executor-task.md и разбирает его маркеры. Шаблон
// принадлежит той же карточке, поэтому тест проверяет его как контракт:
// вердикты пробуждения, точный текст отчёта, владельца re-run и отсутствие TTL.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const TEMPLATE_PATH = resolve(REPO_ROOT, "docs", "templates", "executor-task.md")

const text = readFileSync(TEMPLATE_PATH, "utf8")

function block(name) {
  const marker = `executor-task:${name}`
  const m = text.match(
    new RegExp(
      `<!--\\s*${marker}\\s*\\n([\\s\\S]*?)\\n\\s*${marker}\\s*-->`
    )
  )
  assert.ok(m, `missing block ${name}`)
  return m[1].trim()
}

function verdictSet(line) {
  return new Set(
    line.split(":")[1].split(",").map((v) => v.trim()).filter(Boolean)
  )
}

test("T3.7 шаблон: три вердикта будят, остальные нет", () => {
  const b = block("verdicts")
  const wake = verdictSet(b.split("\n").find((l) => l.startsWith("wake:")))
  const nowake = verdictSet(b.split("\n").find((l) => l.startsWith("nowake:")))

  assert.deepEqual([...wake].sort(), ["keep_working", "rebase", "retry"])
  for (const v of ["ready", "watching", "merged", "closed", "timeout"]) {
    assert.equal(wake.has(v), false, `${v} must not wake`)
    assert.equal(nowake.has(v), true, `${v} must be listed as nowake`)
  }
  for (const v of ["keep_working", "rebase", "retry"]) {
    assert.equal(nowake.has(v), false, `${v} must not be listed as nowake`)
  }
})

test("T3.7 шаблон: отчёт содержит waiting, watchID", () => {
  assert.equal(block("report"), "waiting, watchID")
})

test("T3.7 шаблон: re-run делает агент, у Beacon нет команды от его имени", () => {
  assert.equal(block("rerun-owner"), "agent")
  assert.match(text, /gh run rerun --failed/)
  const forbidden = new RegExp("beacon[^\\n]*" + "gh run" + " rerun --failed", "i")
  assert.equal(forbidden.test(text), false)
})

test("T3.7 шаблон: TTL не является причиной выхода из цикла", () => {
  assert.equal(block("ttl"), "none")
})

test("шаблон: полный цикл карточки с веткой закрытия", () => {
  const b = block("card-lifecycle")
  const fields = Object.fromEntries(
    b.split("\n")
      .filter((l) => l.includes(":"))
      .map((l) => [l.slice(0, l.indexOf(":")).trim(), l.slice(l.indexOf(":") + 1).trim()])
  )
  assert.equal(fields.orchestrator, "spawn-N, one-signal-one-spawn")
  assert.equal(
    fields.steps,
    "spawn, assign-id, claim, check-premise, implement-or-close, pr, finish"
  )
  assert.match(fields["branch-6a"], /code-PR/)
  assert.match(fields["branch-6b"], /close-card-with-evidence/)
  assert.match(fields["close-tool"], /beacon_task_close/)
  assert.match(fields["close-tool"], /beacon task done/)
  assert.match(fields.evidence, /merge-base/)
  assert.equal(fields["duplicate-code-PR"], "forbidden")
})

test("T3.404 шаблон: триаж требует сравнить failedChecks со своим diff", () => {
  const b = block("triage")
  const fields = Object.fromEntries(
    b.split("\n")
      .filter((l) => l.includes(":"))
      .map((l) => [l.slice(0, l.indexOf(":")).trim(), l.slice(l.indexOf(":") + 1).trim()])
  )
  assert.equal(fields.source, "beacon_get_result.failedChecks")
  assert.match(fields.compare, /git diff/)
  assert.equal(fields["own-diff"], "fix-branch-and-push-new-headSHA")
  assert.equal(fields["foreign-or-flaky"], "blocked:not_mine")
  assert.equal(fields.unknown, "report-no-own-changes")
  assert.equal(fields["fix-main"], "forbidden")
  assert.equal(fields.wake, "orchestrator")
})
