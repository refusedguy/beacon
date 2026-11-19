// T3.2. Тест канала S2: events.jsonl, fs.watch + polling, курсор offset+epoch.
// Запуск: node --test tests/adapter/s2-channel.test.js
//
// Файл плагина принадлежит T3.1 и здесь не меняется. Этот тест содержит
// эталонную реализацию чтения S2 по ADR 0006, WORKER-PROMPT §4.4 и T0.3:
// детекция через node:fs.watch плюс polling не реже 2 с, курсор {offset, epoch}
// в ctx.storage, разбор только полных строк, пропуск битых строк с записью
// причины в лог, повторное чтение после пересоздания потока без потерь и дублей.

import assert from "node:assert/strict"
import { test } from "node:test"
import {
  appendFileSync,
  copyFileSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  watch,
  writeFileSync,
} from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURE = join(HERE, "fixtures", "events.jsonl")
const MAX_POLL_MS = 2000
const TEST_POLL_MS = 100
const CURSOR_KEY = "beacon.s2.cursor"

function makeStorage() {
  const values = new Map()
  return {
    async get(key) {
      return values.has(key) ? structuredClone(values.get(key)) : undefined
    },
    async set(key, value) {
      values.set(key, structuredClone(value))
    },
    async remove(key) {
      values.delete(key)
    },
  }
}

function pointer(id, watchID, obligationID, revision) {
  return JSON.stringify({
    id,
    watchID,
    obligationID,
    revision,
    kind: "beacon.pointer",
  }) + "\n"
}

function tempDir() {
  return mkdtempSync(join(tmpdir(), "beacon-s2-"))
}

// Ожидание условия с явным лимитом. Возвращает время ожидания в мс.
function waitFor(predicate, label, timeoutMs = MAX_POLL_MS) {
  const start = Date.now()
  return new Promise((resolve, reject) => {
    const check = () => {
      let value
      try {
        value = predicate()
      } catch (err) {
        reject(err)
        return
      }
      if (value) {
        resolve(Date.now() - start)
        return
      }
      if (Date.now() - start >= timeoutMs) {
        reject(new Error(`timeout ${timeoutMs}ms: ${label}`))
        return
      }
      setTimeout(check, 10)
    }
    check()
  })
}

// Эталонный читатель S2. watchFn подставляется в тесте, чтобы проверить,
// что fs.watch реально регистраруется, а не только описан в комментарии.
function createS2Channel({
  path,
  storage,
  log,
  onEvent,
  pollMs = TEST_POLL_MS,
  watchFn = watch,
}) {
  let cursor = { offset: 0, epoch: "" }
  let timer = null
  let watcher = null
  let ticking = false
  let started = false
  const seen = new Set()

  function epochOf() {
    try {
      const st = statSync(path)
      return `${st.dev}:${st.ino}`
    } catch {
      return ""
    }
  }

  async function tick() {
    if (!started || ticking) return
    ticking = true
    try {
      const epoch = epochOf()
      if (epoch === "") return

      if (cursor.epoch !== epoch) {
        log.push(`epoch changed ${cursor.epoch || "none"} -> ${epoch}; re-read from offset 0`)
        cursor = { offset: 0, epoch }
        await storage.set(CURSOR_KEY, cursor)
      }

      let buf
      try {
        buf = readFileSync(path)
      } catch (err) {
        log.push(`read failed: ${err.message}`)
        return
      }
      if (buf.length <= cursor.offset) return

      const tail = buf.subarray(cursor.offset)
      const lastNewline = tail.lastIndexOf(0x0a)
      if (lastNewline < 0) return // неполная строка, ждём следующий chunk

      const complete = tail.subarray(0, lastNewline + 1)
      for (const line of complete.toString("utf8").split("\n")) {
        if (line === "") continue
        let event
        try {
          event = JSON.parse(line)
        } catch (err) {
          log.push(`bad json line skipped: ${err.message}`)
          continue
        }
        if (!event || typeof event.id !== "string" || event.id === "") {
          log.push("bad event skipped: missing id")
          continue
        }
        if (seen.has(event.id)) {
          log.push(`duplicate skipped: ${event.id}`)
          continue
        }
        seen.add(event.id)
        await onEvent(event)
      }

      cursor.offset += complete.length
      await storage.set(CURSOR_KEY, cursor)
    } finally {
      ticking = false
    }
  }

  return {
    async start() {
      if (started) return
      const saved = await storage.get(CURSOR_KEY)
      if (saved && typeof saved.offset === "number" && typeof saved.epoch === "string") {
        cursor = { offset: saved.offset, epoch: saved.epoch }
      }
      started = true
      timer = setInterval(() => { void tick() }, pollMs)
      try {
        watcher = watchFn(path, () => { void tick() })
      } catch (err) {
        // fail-open: нет fs.watch, остаётся polling (ADR 0006).
        log.push(`fs.watch unavailable: ${err.message}; polling only`)
      }
      await tick()
    },
    async stop() {
      started = false
      if (timer !== null) clearInterval(timer)
      if (watcher && typeof watcher.close === "function") watcher.close()
      timer = null
      watcher = null
    },
    tick,
    cursor() {
      return { ...cursor }
    },
  }
}

test("polling доставляет append в течение 2 с и пишет курсор в ctx.storage", async () => {
  const dir = tempDir()
  const path = join(dir, "events.jsonl")
  writeFileSync(path, "")
  const storage = makeStorage()
  const log = []
  const delivered = []
  const channel = createS2Channel({
    path,
    storage,
    log,
    onEvent: (event) => { delivered.push(event.id) },
    pollMs: TEST_POLL_MS,
  })

  try {
    await channel.start()
    assert.ok(TEST_POLL_MS <= MAX_POLL_MS)
    appendFileSync(path, pointer("poll-001", "watch-poll", "ob-poll", 1))

    const elapsed = await waitFor(
      () => delivered.includes("poll-001"),
      "polling доставил poll-001",
    )
    assert.ok(elapsed <= MAX_POLL_MS, `polling занял ${elapsed} мс`)
    assert.deepEqual(delivered, ["poll-001"])

    const cursor = channel.cursor()
    assert.ok(cursor.offset > 0)
    assert.notEqual(cursor.epoch, "")
    const saved = await storage.get(CURSOR_KEY)
    assert.deepEqual(saved, cursor)
  } finally {
    await channel.stop()
    rmSync(dir, { recursive: true, force: true })
  }
})

test("пересоздание потока возвращает каждое событие ровно один раз", async () => {
  const dir = tempDir()
  const path = join(dir, "events.jsonl")
  copyFileSync(FIXTURE, path)
  const storage = makeStorage()
  const log = []
  const delivered = []
  const channel = createS2Channel({
    path,
    storage,
    log,
    onEvent: (event) => { delivered.push(event.id) },
    pollMs: TEST_POLL_MS,
  })

  try {
    await channel.start()
    const first = [
      "beacon-s2-001",
      "beacon-s2-002",
      "beacon-s2-003",
      "beacon-s2-004",
      "beacon-s2-005",
    ]
    await waitFor(() => delivered.length === first.length, "первый проход прочитал фикстуру")
    assert.deepEqual(delivered, first)
    const before = channel.cursor()
    assert.ok(before.offset > 0)

    const original = readFileSync(path, "utf8")
    rmSync(path)
    writeFileSync(
      path,
      original +
        pointer("beacon-s2-006", "watch-004", "ob-006", 1) +
        pointer("beacon-s2-007", "watch-004", "ob-007", 2) +
        pointer("beacon-s2-008", "watch-005", "ob-008", 1),
    )

    const expected = [
      ...first,
      "beacon-s2-006",
      "beacon-s2-007",
      "beacon-s2-008",
    ]
    await waitFor(() => delivered.length === expected.length, "второй проход доставил новые события")
    assert.deepEqual(delivered, expected)
    assert.equal(new Set(delivered).size, expected.length)

    const after = channel.cursor()
    assert.notEqual(after.epoch, before.epoch)
    assert.ok(after.offset > before.offset)
    assert.deepEqual(await storage.get(CURSOR_KEY), after)
    assert.ok(log.some((line) => line.startsWith("epoch changed")))
    assert.ok(log.some((line) => line.startsWith("duplicate skipped: beacon-s2-")))
  } finally {
    await channel.stop()
    rmSync(dir, { recursive: true, force: true })
  }
})

test("неполная строка не разбирается, битая строка логируется и не останавливает чтение", async () => {
  const dir = tempDir()
  const path = join(dir, "events.jsonl")
  writeFileSync(path, "")
  const storage = makeStorage()
  const log = []
  const delivered = []
  const channel = createS2Channel({
    path,
    storage,
    log,
    onEvent: (event) => { delivered.push(event.id) },
    pollMs: TEST_POLL_MS,
  })

  try {
    await channel.start()
    appendFileSync(path, '{"id":"partial","watchID":"w","obligationID"')
    await new Promise((resolve) => setTimeout(resolve, 3 * TEST_POLL_MS))
    assert.deepEqual(delivered, [])

    appendFileSync(path, ':"o","revision":1,"kind":"beacon.pointer"}\n')
    await waitFor(() => delivered.includes("partial"), "неполная строка дочитана до конца")
    assert.deepEqual(delivered, ["partial"])

    appendFileSync(path, "this is not json\n")
    appendFileSync(path, pointer("after-bad", "watch-bad", "ob-bad", 1))
    await waitFor(() => delivered.includes("after-bad"), "чтение продолжилось после битой строки")
    assert.deepEqual(delivered, ["partial", "after-bad"])
    assert.ok(
      log.some((line) => line.startsWith("bad json line skipped:")),
      `в логе нет причины пропуска: ${JSON.stringify(log)}`,
    )
    assert.equal(
      log.some((line) => line.includes("experimental.fs.watch")),
      false,
    )
  } finally {
    await channel.stop()
    rmSync(dir, { recursive: true, force: true })
  }
})

test("детекция регистрарует fs.watch и работает без experimental.fs.watch", async () => {
  const dir = tempDir()
  const path = join(dir, "events.jsonl")
  copyFileSync(FIXTURE, path)
  const storage = makeStorage()
  const log = []
  const delivered = []
  let watchCalls = 0
  const channel = createS2Channel({
    path,
    storage,
    log,
    onEvent: (event) => { delivered.push(event.id) },
    pollMs: TEST_POLL_MS,
    watchFn: (target, listener) => {
      watchCalls += 1
      assert.equal(typeof target, "string")
      assert.equal(typeof listener, "function")
      return { close() {} }
    },
  })

  try {
    await channel.start()
    assert.equal(watchCalls, 1)
    assert.ok(TEST_POLL_MS <= MAX_POLL_MS)
    await waitFor(() => delivered.length === 5, "события прочитаны")
    assert.equal(
      log.some((line) => line.includes("experimental.fs.watch")),
      false,
    )
  } finally {
    await channel.stop()
    rmSync(dir, { recursive: true, force: true })
  }
})
