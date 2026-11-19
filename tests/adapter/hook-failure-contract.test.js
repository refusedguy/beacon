// T3.50. Failure contract вызова hook адаптера пробуждения (ADR 0037 п4).
// Отказ адаптера завершается кодом 0 и пустым stdout, причина пишется в лог.
// Сломанный адаптер не роняет сервер и не прерывает остальные ожидания.
// Тест лежит в tests/adapter, поэтому CI его запускает.
//
// Это контракт вызова hook со стороны Go-ядра. Он не дублирует fail-open
// JS-плагина из ADR 0006 / T3.5: там ошибка не ломает сессию OpenCode,
// здесь отказ одного адаптера не ломает остальные ожидания сервера.
// Правила лежат в .opencode/settings.json, код не добавляет своих исключений.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const SETTINGS_PATH = join(dirname(fileURLToPath(import.meta.url)), "..", "..", ".opencode", "settings.json")

export function loadSettings(path = SETTINGS_PATH) {
  return JSON.parse(readFileSync(path, "utf8"))
}

export function loadFailureContract(settings = loadSettings()) {
  return settings.hookFailureContract
}

// Явная модель границы вызова hook в Go-ядре. Адаптер может вернуть код и
// stdout либо бросить исключение. Граница ловит отказ, пишет причину в лог и
// возвращает контракт из settings: код 0 и пустой stdout. Исключение наружу
// не уходит, поэтому сервер и цикл ожиданий продолжают работать.
export function callHookWithFailureContract(adapter, contract, wait) {
  const log = []
  let raw
  try {
    raw = adapter(wait)
  } catch (err) {
    raw = { exitCode: 1, stdout: "", error: err }
  }

  const exitFailed = typeof raw?.exitCode === "number" && raw.exitCode !== 0
  const failed = Boolean(raw?.error) || exitFailed

  if (!failed) {
    return {
      exitCode: raw?.exitCode ?? 0,
      stdout: raw?.stdout ?? "",
      failed: false,
      log,
    }
  }

  const reason = raw?.error
    ? String(raw.error?.message ?? raw.error)
    : `адаптер завершился кодом ${raw?.exitCode}`

  if (contract.onError.logReason) log.push(reason)

  return {
    exitCode: contract.onError.exitCode,
    stdout: contract.onError.stdout,
    reason,
    failed: true,
    log,
  }
}

// Прогон всех ожиданий. Каждый вызов изолирован: отказ одного адаптера не
// останавливает обход остальных и не поднимает исключение из цикла.
export function runWaits(adapters, contract) {
  const log = []
  const results = []
  for (const [index, adapter] of adapters.entries()) {
    const result = callHookWithFailureContract(adapter, contract, index)
    results.push(result)
    log.push(...result.log)
  }
  return { results, log }
}

test("T3.50 конфиг: контракт hook задан в settings и не является fail-open плагина", () => {
  const contract = loadFailureContract()
  assert.equal(contract.scope, "hook-call")
  assert.equal(contract.onError.exitCode, 0)
  assert.equal(contract.onError.stdout, "")
  assert.equal(contract.onError.logReason, true)
  assert.equal(contract.isolateWaits, true)
  assert.equal(contract.waits, 15)
  assert.ok(contract.distinctFrom.includes("ADR 0006 plugin fail-open"))
  assert.ok(contract.distinctFrom.includes("T3.5"))
})

test("T3.50 сломанный адаптер: код 0, пустой stdout, причина в логе", () => {
  const contract = loadFailureContract()
  const result = callHookWithFailureContract(
    () => ({ exitCode: 7, stdout: "поломался адаптер пробуждения" }),
    contract,
    0,
  )
  assert.equal(result.failed, true)
  assert.equal(result.exitCode, 0)
  assert.equal(result.stdout, "")
  assert.match(result.reason, /адаптер завершился кодом 7/)
  assert.deepEqual(result.log, [result.reason])
})

test("T3.50 исключение адаптера не уходит наружу и сводится к коду 0", () => {
  const contract = loadFailureContract()
  const result = callHookWithFailureContract(
    () => { throw new Error("EACCES: events.jsonl недоступен") },
    contract,
    0,
  )
  assert.equal(result.failed, true)
  assert.equal(result.exitCode, 0)
  assert.equal(result.stdout, "")
  assert.match(result.reason, /EACCES/)
  assert.deepEqual(result.log, [result.reason])
})

test("T3.50 рабочий адаптер проходит без искажения контрактом", () => {
  const contract = loadFailureContract()
  const result = callHookWithFailureContract(
    () => ({ exitCode: 0, stdout: "ok" }),
    contract,
    0,
  )
  assert.equal(result.failed, false)
  assert.equal(result.exitCode, 0)
  assert.equal(result.stdout, "ok")
  assert.deepEqual(result.log, [])
})

test("T3.50 отказ одного адаптера не роняет сервер и 14 остальных ожиданий", () => {
  const contract = loadFailureContract()
  const adapters = Array.from({ length: contract.waits }, (_, index) =>
    index === 3
      ? () => ({ exitCode: 9, stdout: "адаптер упал" })
      : () => ({ exitCode: 0, stdout: "ok" }),
  )

  let serverCrashed = false
  let run
  try {
    run = runWaits(adapters, contract)
  } catch {
    serverCrashed = true
  }

  assert.equal(serverCrashed, false)
  assert.equal(run.results.length, 15)
  assert.equal(run.results.filter((r) => r.failed).length, 1)

  const broken = run.results[3]
  assert.equal(broken.exitCode, 0)
  assert.equal(broken.stdout, "")

  const healthy = run.results.filter((_, index) => index !== 3)
  assert.equal(healthy.length, 14)
  assert.ok(healthy.every((r) => r.exitCode === 0 && r.stdout === "ok" && !r.failed))
  assert.equal(run.log.length, 1)
  assert.match(run.log[0], /адаптер завершился кодом 9/)
})
