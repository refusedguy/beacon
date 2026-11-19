// Плагин-мост OpenCode. Один JS-файл, без package.json и без SDK хоста.
// T3.1 задаёт каркас: id, подписку на события и fail-open cleanup.
// T3.16 добавляет доставку diff-комментария: плагин читает указатель,
// будит сессию каналом steer и пишет квитанцию по ADR 0014.
import { existsSync, mkdtempSync, readdirSync, readFileSync, statSync } from "node:fs"
import { mkdir, open } from "node:fs/promises"
import { homedir, tmpdir } from "node:os"
import { dirname, join, resolve, sep } from "node:path"
import { fileURLToPath } from "node:url"

const ID = "beacon-bridge"

// Единственный корень для любых записей адаптера (ADR 0011).
const STATE_DIR = join(
  process.env.XDG_STATE_HOME || join(homedir(), ".local", "state"),
  "beacon",
)
const statePath = (...parts) => join(STATE_DIR, ...parts)

// Пути объявлены в одном месте, чтобы последующие карточки не добавляли
// запись в конфиг OpenCode или в другой каталог.
export const adapterPaths = Object.freeze({
  state: STATE_DIR,
  receipts: statePath("receipts"),
  events: statePath("events.jsonl"),
  pointer: statePath("wake-pointer.json"),
})

// ADR 0030 п6: комментарий из diff — это review. Он идёт тем же каналом steer,
// что и обычный follow-up из ADR 0026; source это происхождение сообщения,
// а не новая доставка. Указатель содержит только идентификаторы и source:
// содержимого проверок в нём нет (ADR 0017).
const REVIEW_SOURCE = "review"
const STEER_DELIVERY = "steer"
const POINTER_FILE = "wake-pointer.json"

// Читает указатель, который адаптер со стороны Beacon оставляет в каталоге
// состояния. Отсутствие файла — не ошибка: это обычное состояние покоя.
export function readWakePointer(stateDir = adapterPaths.state) {
  const path = join(stateDir, POINTER_FILE)
  let raw
  try {
    raw = readFileSync(path, "utf8")
  } catch (err) {
    if (err && err.code === "ENOENT") return null
    throw err
  }
  const parsed = JSON.parse(raw)
  if (!parsed || typeof parsed !== "object") {
    throw new Error("wake pointer: expected an object")
  }
  // Белый список полей: лишние ключи из указателя не попадают ни в prompt,
  // ни в квитанцию.
  return {
    sessionID: parsed.sessionID,
    watchID: parsed.watchID,
    obligationID: parsed.obligationID,
    revision: parsed.revision,
    attempt: parsed.attempt,
    source: parsed.source,
  }
}

// Квитанция ADR 0014: одна строка на событие и попытку, запись через wx,
// режим 0600. Повтор пары даёт EEXIST и не перезаписывает первый файл.
// Поля messageSource и delivery не нужны читающей стороне ADR 0015, но
// фиксируют review и steer в самом факте доставки.
async function writeReceipt(stateDir, pointer, outcome) {
  const receiptsDir = join(stateDir, "receipts")
  await mkdir(receiptsDir, { recursive: true })
  const line =
    JSON.stringify({
      obligationID: pointer.obligationID,
      attempt: pointer.attempt,
      outcome,
      at: new Date().toISOString(),
      delivery: STEER_DELIVERY,
      messageSource: REVIEW_SOURCE,
    }) + "\n"
  const path = join(receiptsDir, `${pointer.obligationID}.${pointer.attempt}`)
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

// Доставляет review-указатель в сессию каналом steer и пишет квитанцию.
// Ошибка prompt даёт transport-failed, а не исключение и не posted (ADR 0014).
export async function deliverReviewPointer(ctx, pointer, stateDir = adapterPaths.state) {
  if (!pointer || pointer.source !== REVIEW_SOURCE) {
    throw new Error("wake pointer is not a review")
  }
  if (!pointer.sessionID || !pointer.obligationID || !Number.isInteger(pointer.attempt)) {
    throw new Error("wake pointer is missing sessionID, obligationID or attempt")
  }
  // Указатель наружу: только watchID, obligationID, revision и source.
  const text =
    `<beacon-event>${JSON.stringify({
      watchID: pointer.watchID,
      obligationID: pointer.obligationID,
      revision: pointer.revision,
      source: pointer.source,
    })}</beacon-event>`
  let outcome = "posted"
  try {
    await ctx.session.prompt({
      sessionID: pointer.sessionID,
      text,
      delivery: STEER_DELIVERY,
    })
  } catch {
    outcome = "transport-failed"
  }
  const receipt = await writeReceipt(stateDir, pointer, outcome)
  return { delivery: STEER_DELIVERY, outcome, receipt }
}

const plugin = {
  id: ID,

  setup(ctx) {
    const ac = new AbortController()

    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({
          types: ["session.execution.started", "beacon.review.comment"],
          signal: ac.signal,
        })) {
          if (!event || event.type !== "beacon.review.comment") continue
          try {
            // Хост может передать другой каталог состояния; по умолчанию
            // используется путь ADR 0011 из adapterPaths.
            const stateDir = ctx.beaconStateDir ?? adapterPaths.state
            const pointer = readWakePointer(stateDir)
            if (!pointer || pointer.source !== REVIEW_SOURCE) continue
            await deliverReviewPointer(ctx, pointer, stateDir)
          } catch {
            // fail-open: ошибка адаптера не ломает сессию (ADR 0006).
          }
        }
      } catch {
        // fail-open: ошибка адаптера не ломает сессию (ADR 0006).
      }
    })()

    return () => ac.abort()
  },
}

export default plugin

// --- table-driven адаптер-тест (T3.1, шаг 4) ---
// Запуск: node --test .opencode/plugins/beacon-bridge.js
// При загрузке плагина OpenCode этот блок не выполняется.

const SELF_PATH = (() => {
  try {
    return fileURLToPath(import.meta.url)
  } catch {
    return ""
  }
})()

function isDirectRun() {
  if (process.env.NODE_TEST_CONTEXT) return true
  const entry = process.argv[1]
  return Boolean(entry && SELF_PATH && resolve(entry) === resolve(SELF_PATH))
}

// Тестовые помощники пишут указатель через тот же дескриптор, что и адаптер.
async function writeJSON(path, value) {
  const handle = await open(path, "w", 0o600)
  try {
    await handle.writeFile(JSON.stringify(value), { encoding: "utf8" })
  } finally {
    await handle.close()
  }
}

function reviewPointer(overrides = {}) {
  return {
    sessionID: "ses_t316",
    watchID: "watch-t316",
    obligationID: "obl-t316",
    revision: 3,
    attempt: 1,
    source: "review",
    // Лишние поля не должны попасть ни в prompt, ни в квитанцию.
    checkBody: "SECRET_CHECK_BODY_MUST_NOT_LEAK",
    ...overrides,
  }
}

function promptHarness({ fail = false } = {}) {
  const calls = []
  return {
    calls,
    ctx: {
      session: {
        async prompt(input) {
          calls.push(input)
          if (fail) throw new Error("transport down")
          return { sessionID: input.sessionID, delivery: input.delivery, woken: true }
        },
      },
    },
  }
}

const adapterCases = [
  {
    name: "один JS-файл плагина без package.json",
    check(assert) {
      const pluginDir = dirname(SELF_PATH)
      const projectDir = resolve(pluginDir, "..", "..")
      const jsFiles = readdirSync(pluginDir).filter((name) => name.endsWith(".js"))
      assert.deepEqual(jsFiles, ["beacon-bridge.js"])
      assert.equal(existsSync(join(pluginDir, "package.json")), false)
      assert.equal(existsSync(join(projectDir, "package.json")), false)
    },
  },
  {
    name: "ноль импортов SDK хоста, только встроенные модули Node",
    check(assert) {
      const source = readFileSync(SELF_PATH, "utf8")
      // Иглы собраны из частей: буквальные подстроки для поиска попали бы
      // под регулярное выражение ниже и дали ложный specifier.
      const sdkPrefix = ["@open", "code/"].join("")
      const sp = String.fromCharCode(32)
      const quotes = [String.fromCharCode(34), String.fromCharCode(39)]
      const needles = quotes.flatMap((q) => [
        ["fro", "m", sp, q].join("") + sdkPrefix,
        ["requ", "ire", "(", q].join("") + sdkPrefix,
      ])
      for (const needle of needles) {
        assert.equal(source.includes(needle), false)
      }
      const imports = [...source.matchAll(/(?:from\s+|import\s*\(|require\s*\()["']([^"']+)["']/g)]
        .map((match) => match[1])
      assert.ok(imports.length > 0)
      for (const specifier of imports) {
        assert.equal(specifier.startsWith("node:"), true, specifier)
      }
    },
  },
  {
    name: "ноль записей вне каталога состояния",
    check(assert) {
      for (const [key, value] of Object.entries(adapterPaths)) {
        const inside = value === STATE_DIR || value.startsWith(STATE_DIR + sep)
        assert.equal(inside, true, key)
      }
      // Единственная запись адаптера — квитанция через wx в receipts.
      // Синхронные записи в конфиг или другой каталог не используются.
      const source = readFileSync(SELF_PATH, "utf8")
      assert.equal(/writeFileSync\(|appendFileSync\(/.test(source), false)
      assert.equal(adapterPaths.receipts, join(adapterPaths.state, "receipts"))
      assert.equal(source.includes('join(stateDir, "receipts")'), true)
      assert.equal(source.includes('"wx"'), true)
    },
  },
  {
    name: "T3.16: review-указатель читается и доставляется steer с квитанцией",
    async check(assert) {
      const stateDir = mkdtempSync(join(tmpdir(), "beacon-t316-state-"))
      const pointer = reviewPointer()
      await writeJSON(join(stateDir, "wake-pointer.json"), pointer)

      const parsed = readWakePointer(stateDir)
      // readWakePointer возвращает только белый список полей.
      assert.deepEqual(Object.keys(parsed).sort(), [
        "attempt", "obligationID", "revision", "sessionID", "source", "watchID",
      ])
      assert.equal(parsed.source, "review")

      const h = promptHarness()
      const result = await deliverReviewPointer(h.ctx, parsed, stateDir)
      assert.equal(result.delivery, "steer")
      assert.equal(result.outcome, "posted")
      assert.equal(result.receipt.created, true)
      assert.equal(h.calls.length, 1)
      assert.equal(h.calls[0].sessionID, "ses_t316")
      assert.equal(h.calls[0].delivery, "steer")
      // Prompt содержит только указатель и source, без содержимого проверки.
      assert.ok(h.calls[0].text.startsWith("<beacon-event>"))
      assert.ok(h.calls[0].text.endsWith("</beacon-event>"))
      assert.ok(h.calls[0].text.includes('"source":"review"'))
      assert.equal(h.calls[0].text.includes(pointer.checkBody), false)

      const receiptPath = join(stateDir, "receipts", "obl-t316.1")
      const line = JSON.parse(readFileSync(receiptPath, "utf8").trim())
      assert.equal(line.obligationID, "obl-t316")
      assert.equal(line.attempt, 1)
      assert.equal(line.outcome, "posted")
      assert.equal(line.delivery, "steer")
      assert.equal(line.messageSource, "review")
      assert.equal(statSync(receiptPath).mode & 0o777, 0o600)
    },
  },
  {
    name: "T3.16: повтор той же пары review не перезаписывает квитанцию",
    async check(assert) {
      const stateDir = mkdtempSync(join(tmpdir(), "beacon-t316-state-"))
      const pointer = reviewPointer()
      const h = promptHarness()
      const first = await deliverReviewPointer(h.ctx, pointer, stateDir)
      const before = readFileSync(join(stateDir, "receipts", "obl-t316.1"), "utf8")

      const second = await deliverReviewPointer(h.ctx, pointer, stateDir)

      assert.equal(first.receipt.created, true)
      assert.equal(second.receipt.created, false)
      assert.equal(second.receipt.code, "EEXIST")
      assert.equal(h.calls.length, 2)
      assert.equal(readFileSync(join(stateDir, "receipts", "obl-t316.1"), "utf8"), before)
    },
  },
  {
    name: "T3.16: ошибка prompt даёт transport-failed, а не posted",
    async check(assert) {
      const stateDir = mkdtempSync(join(tmpdir(), "beacon-t316-state-"))
      const h = promptHarness({ fail: true })
      const result = await deliverReviewPointer(h.ctx, reviewPointer(), stateDir)
      assert.equal(result.outcome, "transport-failed")
      assert.equal(result.receipt.created, true)
      const line = JSON.parse(readFileSync(join(stateDir, "receipts", "obl-t316.1"), "utf8").trim())
      assert.equal(line.outcome, "transport-failed")
      assert.equal(line.messageSource, "review")
    },
  },
  {
    // Проверка именно репозиторийского плагина, не эталона из fixtures.
    // Плагин читает файл указателя и вызывает ctx.session.prompt; чтение
    // events.jsonl по S2 остаётся отдельной карточкой и здесь не проверяется.
    name: "T3.16: плагин читает указатель по событию review",
    async check(assert) {
      const stateDir = mkdtempSync(join(tmpdir(), "beacon-t316-state-"))
      await writeJSON(join(stateDir, "wake-pointer.json"), reviewPointer())

      const subscriptions = []
      const queue = []
      const h = promptHarness()
      const ctx = {
        ...h.ctx,
        beaconStateDir: stateDir,
        event: {
          subscribe(options = {}) {
            const sub = { types: options.types, signal: options.signal, queue }
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
      }

      const stop = plugin.setup(ctx)
      const reviewSub = subscriptions.find((sub) =>
        (sub.types ?? []).includes("beacon.review.comment"),
      )
      assert.ok(reviewSub, "плагин подписан на beacon.review.comment")
      queue.push({ type: "beacon.review.comment" })
      await new Promise((tick) => setTimeout(tick, 30))
      stop()

      assert.equal(h.calls.length, 1)
      assert.equal(h.calls[0].delivery, "steer")
      const line = JSON.parse(readFileSync(join(stateDir, "receipts", "obl-t316.1"), "utf8").trim())
      assert.equal(line.messageSource, "review")
      assert.equal(line.outcome, "posted")
    },
  },
]

if (isDirectRun()) {
  const [{ test }, assert] = await Promise.all([
    import("node:test"),
    import("node:assert/strict"),
  ])
  for (const item of adapterCases) test(item.name, () => item.check(assert))
}
