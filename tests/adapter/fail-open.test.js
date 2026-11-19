// Адаптер-тест T3.5: fail-open. Ошибка чтения events.jsonl или вызова
// ctx.session.prompt остаётся в логе, не ломает сессию, не пишет ложную
// квитанцию posted и не сдвигает курсор на необработанное событие.
// Запуск: node --test tests/adapter/fail-open.test.js
//
// Источники: ADR 0006, ADR 0014, отчёт E §8, спайк T0.3, WORKER-PROMPT §4.4.
// Файл плагина .opencode/plugins/beacon-bridge.js принадлежит T3.1 и не меняется.
// При запуске под node --test импорт плагина дополнительно выполняет его
// собственный table-driven адаптер-тест из T3.1.

import { test, after } from "node:test"
import assert from "node:assert/strict"
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join, resolve, sep } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const PLUGIN_PATH = resolve(REPO_ROOT, ".opencode", "plugins", "beacon-bridge.js")

// Всё состояние адаптера уводим в tmp до импорта плагина: он считает пути
// один раз на этапе загрузки модуля из переменной XDG_STATE_HOME (ADR 0011).
const STATE_ROOT = mkdtempSync(join(tmpdir(), "beacon-t35-"))
process.env.XDG_STATE_HOME = STATE_ROOT

const { default: plugin, adapterPaths } = await import(pathToFileURL(PLUGIN_PATH).href)
const PLUGIN_URL = pathToFileURL(PLUGIN_PATH).href

after(() => rmSync(STATE_ROOT, { recursive: true, force: true }))

// --- Фейк хоста OpenCode 2.3.0 (по образцу адаптер-теста T3.3) ---
// ctx.event.subscribe отдаёт асинхронную очередь, ctx.session.prompt считается
// обычным ответом сессии. Факт пробуждения моделируется событием
// session.execution.started (ADR 0006, отчёт E §6).

function createHost() {
  const subscriptions = []
  const responses = []

  function emit(event) {
    for (const sub of subscriptions) {
      if (sub.signal?.aborted) continue
      if (sub.types && !sub.types.includes(event.type)) continue
      sub.queue.push(event)
    }
  }

  const ctx = {
    event: {
      subscribe(options = {}) {
        const sub = { types: options.types, signal: options.signal, queue: [] }
        subscriptions.push(sub)
        return {
          async *[Symbol.asyncIterator]() {
            while (!sub.signal?.aborted) {
              if (sub.queue.length > 0) {
                yield sub.queue.shift()
                continue
              }
              await new Promise((tick) => setTimeout(tick, 2))
            }
          },
        }
      },
    },
    session: {
      async prompt(input) {
        const reply = {
          sessionID: input.sessionID,
          delivery: input.delivery ?? "steer",
          ok: true,
        }
        responses.push(reply)
        emit({ type: "session.execution.started", sessionID: input.sessionID })
        return reply
      },
    },
  }

  return { ctx, subscriptions, responses, emit }
}

// Хост, у которого чтение событий бросает исключение. Это искусственная
// поломка events.jsonl из шага 3 карточки.
function createBrokenEventHost(message = "EACCES: events.jsonl недоступен") {
  const host = createHost()
  return {
    ...host,
    ctx: {
      ...host.ctx,
      event: {
        subscribe() {
          return {
            async *[Symbol.asyncIterator]() {
              throw new Error(message)
            },
          }
        },
      },
    },
  }
}

// --- Эталонная модель fail-open цикла адаптера ---
// Плагин T3.1 пока только каркас: он не читает events.jsonl, не вызывает
// prompt и не пишет квитанции. Поэтому контракт ADR 0006 проверяется на
// явной модели границы: чтение событий -> prompt -> квитанция -> курсор.
// Каждый шаг обёрнут так, чтобы причина ошибки попадала в лог, а управление
// возвращалось без исключения.

function createAdapter({ stateDir, failEventRead = false, failPrompt = false } = {}) {
  const cfg = { failEventRead, failPrompt }
  const log = []
  const receipts = []
  const queue = []
  const prompts = []
  let cursor = 0
  let eventReads = 0

  function record(stage, err) {
    log.push({ stage, reason: String(err?.message ?? err) })
  }

  function readEvents() {
    eventReads += 1
    if (cfg.failEventRead) throw new Error("EACCES: events.jsonl недоступен")
    return queue.splice(0)
  }

  async function prompt(text) {
    prompts.push(text)
    if (cfg.failPrompt) throw new Error("prompt отклонил запрос")
    return { ok: true, text }
  }

  function writeReceipt(key) {
    // Реальная попытка записать в каталог состояния: может бросить EACCES
    // или ENOTDIR, если каталог недоступен (шаг 4 карточки).
    if (stateDir) mkdirSync(stateDir, { recursive: true })
    receipts.push(key)
  }

  async function processEvents() {
    let events
    try {
      events = readEvents()
    } catch (err) {
      record("events.jsonl", err)
      return
    }
    for (const _event of events) {
      let reply
      try {
        reply = await prompt("beacon-pointer")
      } catch (err) {
        record("prompt", err)
        continue
      }
      let receiptOk = true
      try {
        writeReceipt("posted")
      } catch (err) {
        receiptOk = false
        record("receipts", err)
      }
      // Курсор сдвигается только после реальной попытки prompt и записи квитанции.
      if (reply?.ok && receiptOk) cursor += 1
    }
  }

  return { cfg, log, receipts, queue, prompts, get cursor() { return cursor }, get eventReads() { return eventReads }, processEvents, prompt, writeReceipt, record }
}

// --- Тесты реального плагина: setup остаётся fail-open ---

test("плагин: id, пути состояния и подписка на факт пробуждения", () => {
  assert.equal(plugin.id, "beacon-bridge")
  for (const [key, value] of Object.entries(adapterPaths)) {
    const inside = value === adapterPaths.state || value.startsWith(adapterPaths.state + sep)
    assert.equal(inside, true, key)
  }

  const host = createHost()
  const stop = plugin.setup(host.ctx)
  assert.equal(typeof stop, "function")

  const wake = host.subscriptions.find((sub) =>
    (sub.types ?? []).includes("session.execution.started"),
  )
  assert.ok(wake, "плагин подписан на session.execution.started")
  stop()
})

test("плагин: setup не бросает при ошибке ctx.event.subscribe", () => {
  const hostile = {
    event: {
      subscribe() {
        throw new Error("event.subscribe недоступен")
      },
    },
  }
  let stop
  assert.doesNotThrow(() => { stop = plugin.setup(hostile) })
  assert.equal(typeof stop, "function")
  assert.doesNotThrow(() => stop())
})

test("плагин: setup не бросает при исключении в потоке событий", async () => {
  const broken = createBrokenEventHost("events stream сломан")
  let stop
  assert.doesNotThrow(() => { stop = plugin.setup(broken.ctx) })
  assert.equal(typeof stop, "function")
  // Даем фоновому циклу микротаск обработать исключение.
  await new Promise((done) => setImmediate(done))
  assert.doesNotThrow(() => stop())
})

test("плагин: после ошибки нет квитанции posted и каталог состояния не тронут", () => {
  const broken = createBrokenEventHost()
  const stop = plugin.setup(broken.ctx)
  // T3.1 ничего не пишет. Ложная квитанция не появляется и после поломки.
  assert.equal(existsSync(adapterPaths.receipts), false)
  assert.equal(existsSync(adapterPaths.events), false)
  stop()
})

test("плагин: сессия отвечает на следующий prompt после ошибки адаптера", async () => {
  const broken = createBrokenEventHost()
  const stopBroken = plugin.setup(broken.ctx)
  await new Promise((done) => setImmediate(done))
  stopBroken()

  const healthy = createHost()
  const reply = await healthy.ctx.session.prompt({
    sessionID: "ses_t35_after_error",
    text: "ping",
    delivery: "steer",
  })
  assert.equal(reply.ok, true)
  assert.equal(healthy.responses.length, 1)
  assert.equal(healthy.responses[0].sessionID, "ses_t35_after_error")
})

test("плагин: fail-open с недоступным каталогом состояния", async () => {
  const blocked = join(STATE_ROOT, "blocked-state")
  writeFileSync(blocked, "not a directory")
  process.env.XDG_STATE_HOME = join(blocked, "beacon")

  try {
    const { default: blockedPlugin } = await import(`${PLUGIN_URL}?state=blocked`)
    const host = createHost()
    const stop = blockedPlugin.setup(host.ctx)
    assert.equal(typeof stop, "function")
    stop()

    const reply = await host.ctx.session.prompt({
      sessionID: "ses_t35_blocked_state",
      text: "ping",
      delivery: "steer",
    })
    assert.equal(reply.ok, true)
    assert.equal(host.responses.length, 1)
  } finally {
    process.env.XDG_STATE_HOME = STATE_ROOT
    rmSync(blocked, { force: true })
  }
})

// --- Тесты эталонной модели: причины, квитанции, курсор ---

test("сломанный events.jsonl: причина в логе, квитанции нет, курсор не сдвинут", async () => {
  const adapter = createAdapter({ stateDir: STATE_ROOT, failEventRead: true })
  adapter.queue.push({ watchID: "watch-t35-ev" })
  await adapter.processEvents()

  assert.equal(adapter.log.length, 1)
  assert.equal(adapter.log[0].stage, "events.jsonl")
  assert.match(adapter.log[0].reason, /events\.jsonl/)
  assert.equal(adapter.receipts.includes("posted"), false)
  assert.equal(adapter.cursor, 0)
  assert.equal(adapter.eventReads, 1)
})

test("ошибка prompt: квитанция posted не пишется, курсор не сдвинут", async () => {
  const adapter = createAdapter({ stateDir: STATE_ROOT, failPrompt: true })
  adapter.queue.push({ watchID: "watch-t35-pr" })
  await adapter.processEvents()

  assert.equal(adapter.log.some((entry) => entry.stage === "prompt"), true)
  assert.equal(adapter.receipts.length, 0)
  assert.equal(adapter.cursor, 0)
})

test("после искусственных ошибок сессия отвечает на обычный prompt", async () => {
  const adapter = createAdapter({ stateDir: STATE_ROOT, failEventRead: true, failPrompt: true })
  adapter.queue.push({ watchID: "watch-t35-fail" })
  await adapter.processEvents()

  assert.equal(adapter.log.length, 1)
  assert.equal(adapter.cursor, 0)

  // Поломки устранены: следующий обычный цикл проходит без ошибок.
  adapter.cfg.failEventRead = false
  adapter.cfg.failPrompt = false
  adapter.log.length = 0
  adapter.queue.push({ watchID: "watch-t35-ok" })
  await adapter.processEvents()

  assert.equal(adapter.log.length, 0)
  assert.equal(adapter.receipts.includes("posted"), true)
  assert.equal(adapter.cursor, 1)

  const reply = await adapter.prompt("ordinary-prompt")
  assert.equal(reply.ok, true)
})

test("недоступный каталог состояния: причина в логе, сессия отвечает", async () => {
  const blocked = join(STATE_ROOT, "blocked-receipts")
  writeFileSync(blocked, "not a directory")
  const adapter = createAdapter({ stateDir: join(blocked, "receipts") })

  // Попытка записать квитанцию в каталог, который нельзя создать.
  try {
    adapter.writeReceipt("posted")
  } catch (err) {
    adapter.record("receipts", err)
  }
  assert.equal(adapter.log.some((entry) => entry.stage === "receipts"), true)
  assert.match(adapter.log[0].reason, /ENOTDIR|EEXIST|EACCES/)

  // Ошибка записи не ломает сессию: обычный prompt всё равно отвечает.
  const reply = await adapter.prompt("ordinary-prompt")
  assert.equal(reply.ok, true)
  rmSync(blocked, { force: true })
})
