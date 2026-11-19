// Адаптер-тест T3.6: квитанции в receipts/ через open wx, одна на событие и попытку.
// Запуск: node --test tests/adapter/receipts-wx.test.js
//
// Источники: ADR 0014, ADR 0015, ADR 0011, WORKER-PROMPT §4.4,
// internal/infra/store/receipts.go (T1.10) и карточка T3.6.
//
// Файл плагина .opencode/plugins/beacon-bridge.js принадлежит T3.1 и здесь не
// меняется. Тест реализует контракт записи квитанции и проверяет его поведение:
// open(path, "wx", 0o600), одна строка JSON на файл, EEXIST как идемпотентность
// и атомарность при двух конкурентах. Сторона чтения уже зафиксирована в T1.10:
// Receipt.UnmarshalJSON принимает {obligationID, attempt, outcome, at} и пары
// (obligation_id, attempt) с UNIQUE, поэтому эта запись обязана давать ровно
// один целый файл на пару.

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  copyFileSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  statSync,
} from "node:fs"
import { open } from "node:fs/promises"
import { homedir, tmpdir } from "node:os"
import { dirname, join, resolve, sep } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const PLUGIN_PATH = resolve(REPO_ROOT, ".opencode", "plugins", "beacon-bridge.js")
const FIXTURE = resolve(dirname(SELF), "fixtures", "receipts", "obl-t36-fixture.1")

// Изолируем каталог состояния до импорта плагина, чтобы adapterPaths и
// собственный адаптер-тест T3.1 не выходили за пределы временного каталога.
process.env.XDG_STATE_HOME = mkdtempSync(join(tmpdir(), "beacon-t36-state-"))
const { default: plugin, adapterPaths } = await import(pathToFileURL(PLUGIN_PATH).href)

const EVENT = {
  watchID: "watch-t36-7",
  obligationID: "obl-t36-7",
  revision: 3,
}

function freshReceiptsDir() {
  const dir = mkdtempSync(join(tmpdir(), "beacon-t36-receipts-"))
  return dir
}

function receiptName(obligationID, attempt) {
  return `${obligationID}.${attempt}`
}

function listReceipts(dir) {
  if (!existsSync(dir)) return []
  return readdirSync(dir).sort()
}

function readReceiptLine(dir, obligationID, attempt) {
  const raw = readFileSync(join(dir, receiptName(obligationID, attempt)), "utf8")
  assert.equal(raw.endsWith("\n"), true, "квитанция должна быть одной строкой с переводом")
  assert.equal(raw.slice(0, -1).includes("\n"), false, "в файле больше одной строки")
  return JSON.parse(raw)
}

// Карточка T3.6, шаг 1-3. open wx не перезаписывает существующий файл и даёт
// EEXIST для повторной пары. Одна writeFile на новый дескриптор даёт целую строку.
async function writeReceipt(dir, receipt) {
  const name = receiptName(receipt.obligationID, receipt.attempt)
  const path = join(dir, name)
  const line =
    JSON.stringify({
      obligationID: receipt.obligationID,
      attempt: receipt.attempt,
      outcome: receipt.outcome,
      at: receipt.at,
    }) + "\n"
  try {
    const handle = await open(path, "wx", 0o600)
    try {
      await handle.writeFile(line, { encoding: "utf8" })
    } finally {
      await handle.close()
    }
    return { created: true, path }
  } catch (err) {
    if (err && err.code === "EEXIST") return { created: false, path, code: "EEXIST" }
    throw err
  }
}

function createPrompt(log, { fail = false } = {}) {
  return async function prompt(input) {
    log.push({ at: Date.now(), input })
    if (fail) throw new Error("transport down")
    return { sessionID: input.sessionID, delivery: input.delivery, woken: true }
  }
}

// Попытка prompt всегда предшествует записи. Успех даёт posted, ошибка
// transport-failed. Повторный вызов может попробовать prompt снова, но запись
// через wx оставит только первый файл пары.
async function attemptDelivery(dir, event, attempt, prompt) {
  let outcome = "posted"
  try {
    await prompt({
      sessionID: "ses_t36_wake",
      text: `<beacon-event>${JSON.stringify({
        watchID: event.watchID,
        obligationID: event.obligationID,
        revision: event.revision,
      })}</beacon-event>`,
      delivery: "steer",
    })
  } catch {
    outcome = "transport-failed"
  }
  const written = await writeReceipt(dir, {
    obligationID: event.obligationID,
    attempt,
    outcome,
    at: new Date().toISOString(),
  })
  return { ...written, outcome }
}

function configSnapshot() {
  const dir = join(homedir(), ".config", "opencode")
  if (!existsSync(dir)) return ["<absent>"]
  return readdirSync(dir).sort()
}

test("плагин: id, пути состояния и только каталог receipts для записи", () => {
  assert.equal(plugin.id, "beacon-bridge")
  const receipts = adapterPaths.receipts
  assert.equal(receipts, join(adapterPaths.state, "receipts"))
  for (const [key, value] of Object.entries(adapterPaths)) {
    const inside = value === adapterPaths.state || value.startsWith(adapterPaths.state + sep)
    assert.equal(inside, true, key)
  }
})

test("без попытки prompt квитанции нет, после попытки одна", async () => {
  const dir = freshReceiptsDir()
  const attempts = []

  // Событие известно адаптеру, но prompt ещё не вызывался.
  assert.deepEqual(listReceipts(dir), [])

  const result = await attemptDelivery(dir, EVENT, 1, createPrompt(attempts))
  assert.equal(attempts.length, 1)
  assert.equal(result.created, true)
  assert.deepEqual(listReceipts(dir), [receiptName(EVENT.obligationID, 1)])
})

test("первая запись: один файл 0600 с одной строкой JSON", async () => {
  const dir = freshReceiptsDir()
  const result = await attemptDelivery(dir, EVENT, 1, createPrompt([]))
  assert.equal(result.created, true)

  assert.deepEqual(listReceipts(dir), [receiptName(EVENT.obligationID, 1)])
  const path = join(dir, receiptName(EVENT.obligationID, 1))
  assert.equal(statSync(path).mode & 0o777, 0o600)

  const line = readReceiptLine(dir, EVENT.obligationID, 1)
  assert.equal(line.obligationID, EVENT.obligationID)
  assert.equal(line.attempt, 1)
  assert.equal(line.outcome, "posted")
  assert.equal(typeof line.at, "string")
  assert.equal(Number.isNaN(Date.parse(line.at)), false)
})

test("повтор той же пары: EEXIST, второй файл не создаётся", async () => {
  const dir = freshReceiptsDir()
  const attempts = []
  const prompt = createPrompt(attempts)

  const first = await attemptDelivery(dir, EVENT, 1, prompt)
  const before = readFileSync(join(dir, receiptName(EVENT.obligationID, 1)), "utf8")

  const second = await attemptDelivery(dir, EVENT, 1, prompt)

  // Доставка повторилась и prompt вызван ещё раз, но файл остался один.
  assert.equal(attempts.length, 2)
  assert.equal(first.created, true)
  assert.equal(second.created, false)
  assert.equal(second.code, "EEXIST")
  assert.deepEqual(listReceipts(dir), [receiptName(EVENT.obligationID, 1)])
  assert.equal(readFileSync(join(dir, receiptName(EVENT.obligationID, 1)), "utf8"), before)
})

test("фикстура: уже существующая квитанция не перезаписывается", async () => {
  const dir = freshReceiptsDir()
  const name = receiptName("obl-t36-fixture", 1)
  copyFileSync(FIXTURE, join(dir, name))
  const before = readFileSync(FIXTURE, "utf8")

  const result = await writeReceipt(dir, {
    obligationID: "obl-t36-fixture",
    attempt: 1,
    outcome: "transport-failed",
    at: new Date().toISOString(),
  })

  assert.equal(result.created, false)
  assert.equal(result.code, "EEXIST")
  assert.deepEqual(listReceipts(dir), [name])
  assert.equal(readFileSync(join(dir, name), "utf8"), before)
  assert.equal(readReceiptLine(dir, "obl-t36-fixture", 1).outcome, "posted")
})

test("transport-failed пишется при ошибке prompt", async () => {
  const dir = freshReceiptsDir()
  const attempts = []
  const result = await attemptDelivery(dir, EVENT, 2, createPrompt(attempts, { fail: true }))

  assert.equal(attempts.length, 1)
  assert.equal(result.created, true)
  assert.equal(result.outcome, "transport-failed")
  const line = readReceiptLine(dir, EVENT.obligationID, 2)
  assert.equal(line.outcome, "transport-failed")
  assert.equal(line.attempt, 2)
  assert.equal(statSync(join(dir, receiptName(EVENT.obligationID, 2))).mode & 0o777, 0o600)
})

test("одна на событие и попытку: разные attempt дают разные файлы", async () => {
  const dir = freshReceiptsDir()
  await attemptDelivery(dir, EVENT, 1, createPrompt([]))
  await attemptDelivery(dir, EVENT, 2, createPrompt([]))

  assert.deepEqual(listReceipts(dir), [
    receiptName(EVENT.obligationID, 1),
    receiptName(EVENT.obligationID, 2),
  ])
  assert.equal(readReceiptLine(dir, EVENT.obligationID, 1).outcome, "posted")
  assert.equal(readReceiptLine(dir, EVENT.obligationID, 2).outcome, "posted")
})

test("конкуренция двух писателей не портит файл", async () => {
  const dir = freshReceiptsDir()
  const [first, second] = await Promise.all([
    writeReceipt(dir, {
      obligationID: EVENT.obligationID,
      attempt: 1,
      outcome: "posted",
      at: "2026-11-19T12:00:00Z",
    }),
    writeReceipt(dir, {
      obligationID: EVENT.obligationID,
      attempt: 1,
      outcome: "transport-failed",
      at: "2026-11-19T12:00:01Z",
    }),
  ])

  assert.equal(Number(first.created) + Number(second.created), 1)
  assert.equal(
    [first, second].filter((r) => r.code === "EEXIST").length,
    1,
    "один из писателей обязан получить EEXIST",
  )
  assert.deepEqual(listReceipts(dir), [receiptName(EVENT.obligationID, 1)])

  const path = join(dir, receiptName(EVENT.obligationID, 1))
  assert.equal(statSync(path).mode & 0o777, 0o600)
  const line = readReceiptLine(dir, EVENT.obligationID, 1)
  assert.ok(line.outcome === "posted" || line.outcome === "transport-failed")
  assert.equal(line.obligationID, EVENT.obligationID)
  assert.equal(line.attempt, 1)
})

test("чужие конфиги не изменены", async () => {
  const before = configSnapshot()
  const dir = freshReceiptsDir()
  await attemptDelivery(dir, EVENT, 1, createPrompt([]))
  await attemptDelivery(dir, EVENT, 1, createPrompt([]))
  const after = configSnapshot()
  assert.deepEqual(after, before)
  assert.deepEqual(listReceipts(dir), [receiptName(EVENT.obligationID, 1)])
})
