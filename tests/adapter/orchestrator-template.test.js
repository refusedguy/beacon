// Адаптер-тест T3.8: шаблон задания оркестратора как исполняемая проверка.
// Читает docs/templates/orchestrator-task.md через node:fs и проверяет маркеры,
// затем прогоняет модель решения оркестратора на трёх ситуациях из карточки.
// Запуск: node --test tests/adapter/orchestrator-template.test.js
//
// Источники: WORKER-PROMPT §4.7 и §13, приёмка T3.2 из BEACON-AUDIT,
// ADR 0001 (чек из четырёх пунктов), ADR 0014, ADR 0015, ADR 0017,
// ADR 0019, ADR 0020, ADR 0021, ADR 0022. Решение D2 убирает авто-мерж.
// Файл шаблона только читается и не меняется этим тестом.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const TEMPLATE_PATH = resolve(REPO_ROOT, "docs", "templates", "orchestrator-task.md")

const text = readFileSync(TEMPLATE_PATH, "utf8")

const MARKERS = [
  {
    name: "мерж только после get_result с verdict=ready",
    pattern: /Мерж разрешён только после вызова `beacon_get_result` с `verdict=ready`/,
  },
  {
    name: "чек ADR 0001 из четырёх пунктов",
    pattern: /ADR 0001[\s\S]*?Критерии приёмки[\s\S]*?Скоуп[\s\S]*?Ревью[\s\S]*?Branch protection/,
  },
  {
    name: "ключ идемпотентности (watchID, revision)",
    pattern: /Ключ идемпотентности мержа это пара `\(watchID, revision\)`[\s\S]*?Повторный prompt с тем же `\(watchID, revision\)` не создаёт второй мерж/,
  },
  {
    name: "subagent-отчёт не вердикт",
    pattern: /`<subagent>`-отчёт о завершении раунда не является вердиктом/,
  },
  {
    name: "возврат исполнителя решает оркестратор",
    pattern: /Возврат исполнителя на доработку решает оркестратор, а не Beacon/,
  },
  {
    name: "нет авто-меража со стороны Beacon",
    pattern: /Нет авто-мержа со стороны Beacon[\s\S]*?Мерж выполняет оркестратор своими правами/,
  },
]

// Модель из трёх условий: вердикт из get_result, чек перед мержем, ключ от второго мержа.
function createOrchestrator() {
  const merged = new Set()
  const decisions = []

  function decide({ verdict, check, key }) {
    let decision
    if (verdict !== "ready") {
      decision = { merge: false, reason: "no-ready" }
    } else if (!check.passed) {
      decision = { merge: false, reason: "check-failed" }
    } else if (merged.has(key)) {
      decision = { merge: false, reason: "duplicate" }
    } else {
      merged.add(key)
      decision = { merge: true, reason: "ok" }
    }
    decisions.push(decision)
    return decision
  }

  return { decide, decisions, get mergeCount() { return merged.size } }
}

function runPreMergeCheck(input) {
  const failed = []
  if (input.failedChecks.some((c) => c.required && c.conclusion !== "SUCCESS")) failed.push("criteria")
  if (!input.scopeOk) failed.push("scope")
  if (!input.ignoreReviews && input.reviewDecision === "CHANGES_REQUESTED") failed.push("review")
  const allowed = ["CLEAN", "HAS_HOOKS", "BEHIND"]
  if (
    input.mergeable !== "MERGEABLE" ||
    !allowed.includes(input.mergeStateStatus) ||
    input.draft ||
    (input.mergeStateStatus === "BEHIND" && input.requireUpToDate)
  ) failed.push("branch-protection")
  return { passed: failed.length === 0, failed }
}

const READY_SNAPSHOT = {
  verdict: "ready",
  revision: 1,
  watchID: "watch-t38-ready",
  failedChecks: [],
  scopeOk: true,
  reviewDecision: "APPROVED",
  ignoreReviews: true,
  mergeable: "MERGEABLE",
  mergeStateStatus: "CLEAN",
  draft: false,
  requireUpToDate: false,
}

// Отчёт ребёнка о завершении раунда не несёт вердикта.
function verdictFromIdleReport(report) {
  return report.type === "subagent" && report.status === "waiting" ? null : report.verdict
}

test("T3.8 оркестратор: маркеры шаблона читаются из docs/templates", () => {
  assert.ok(text.length > 0)
  for (const marker of MARKERS) {
    assert.match(text, marker.pattern, `нет маркера: ${marker.name}`)
  }
  assert.equal(/Beacon мержит/.test(text), false, "в шаблоне нет авто-меража Beacon")
})

test("T3.8 оркестратор: преждевременный idle-отчёт блокирует мерж", () => {
  const orch = createOrchestrator()
  const idleReport = { type: "subagent", status: "waiting", watchID: "watch-t38-idle" }
  const check = runPreMergeCheck(READY_SNAPSHOT)

  const result = orch.decide({
    verdict: verdictFromIdleReport(idleReport),
    check,
    key: `${idleReport.watchID}:1`,
  })

  assert.equal(result.merge, false)
  assert.equal(result.reason, "no-ready")
  assert.equal(orch.mergeCount, 0)
})

test("T3.8 оркестратор: ready с пройденным чеком разрешает мерж", () => {
  const orch = createOrchestrator()
  const check = runPreMergeCheck(READY_SNAPSHOT)
  assert.deepEqual(check.failed, [])

  const result = orch.decide({
    verdict: READY_SNAPSHOT.verdict,
    check,
    key: `${READY_SNAPSHOT.watchID}:${READY_SNAPSHOT.revision}`,
  })

  assert.equal(result.merge, true)
  assert.equal(result.reason, "ok")
  assert.equal(orch.mergeCount, 1)
})

test("T3.8 оркестратор: провал одного пункта чека блокирует мерж", () => {
  const orch = createOrchestrator()
  const check = runPreMergeCheck({ ...READY_SNAPSHOT, scopeOk: false })
  assert.deepEqual(check.failed, ["scope"])

  const result = orch.decide({
    verdict: READY_SNAPSHOT.verdict,
    check,
    key: "watch-t38-scope:1",
  })

  assert.equal(result.merge, false)
  assert.equal(result.reason, "check-failed")
  assert.equal(orch.mergeCount, 0)
})

test("T3.8 оркестратор: двойной prompt с одним (watchID, revision) даёт один мерж", () => {
  const orch = createOrchestrator()
  const key = `${READY_SNAPSHOT.watchID}:${READY_SNAPSHOT.revision}`
  const check = runPreMergeCheck(READY_SNAPSHOT)

  const first = orch.decide({ verdict: "ready", check, key })
  const second = orch.decide({ verdict: "ready", check, key })

  assert.equal(first.merge, true)
  assert.equal(second.merge, false)
  assert.equal(second.reason, "duplicate")
  assert.equal(orch.mergeCount, 1)
  assert.equal(orch.decisions.length, 2)
})
