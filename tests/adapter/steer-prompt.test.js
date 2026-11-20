// Адаптер-тест T3.3: пробуждение сессии через ctx.session.prompt с delivery "steer".
// Запуск: node --test tests/adapter/steer-prompt.test.js
//
// Источники: ADR 0006, WORKER-PROMPT §4.4, отчёт B §2.1 и §2.2, спайк T0.3
// (tmp/p0-wake/Results.md). Канал выбран S2, поэтому heartbeat S1 не нужен.
// Плагин .opencode/plugins/beacon-bridge.js только импортируется и не меняется.
// При запуске под node --test импорт плагина дополнительно выполняет его
// собственный table-driven адаптер-тест из T3.1.

import { test } from "node:test"
import assert from "node:assert/strict"
import { dirname, resolve, sep } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const PLUGIN_PATH = resolve(REPO_ROOT, ".opencode", "plugins", "beacon-bridge.js")

const {
  default: plugin,
  adapterPaths,
  FETCH_BEFORE_WORKTREE_RULE,
  checkFetchBeforeWorktree,
} = await import(pathToFileURL(PLUGIN_PATH).href)

const TEST_SESSION = "ses_t33_wake"
const CONTROL_SESSION = "ses_t33_control"

// Указатель §4.4: watchID, obligationID, revision. Содержимого проверок нет.
const BEACON_EVENT = {
  watchID: "watch-t33-7",
  obligationID: "obl-t33-7",
  revision: 3,
  // Данные ниже не должны попасть в текст prompt.
  checkName: "CI / build",
  checkBody: "SECRET_CHECK_BODY_MUST_NOT_LEAK",
}

function buildPromptText(event) {
  const pointer = {
    watchID: event.watchID,
    obligationID: event.obligationID,
    revision: event.revision,
  }
  return `<beacon-event>${JSON.stringify(pointer)}</beacon-event>`
}

// Фейк хоста OpenCode 2.3.0: event bus и session API.
// eventLog играет роль потока GET /api/event, subscriptions роль ctx.event.subscribe.
function createHarness(sessionID) {
  const subscriptions = []
  const eventLog = []
  const promptCalls = []

  function emit(event) {
    eventLog.push(event)
    for (const sub of subscriptions) {
      if (sub.signal?.aborted) continue
      if (sub.types && !sub.types.includes(event.type)) continue
      sub.seen.push(event)
      sub.queue.push(event)
    }
  }

  const ctx = {
    event: {
      subscribe(options = {}) {
        const sub = {
          types: options.types,
          signal: options.signal,
          seen: [],
          queue: [],
        }
        subscriptions.push(sub)
        return {
          async *[Symbol.asyncIterator]() {
            while (!sub.signal?.aborted) {
              if (sub.queue.length > 0) {
                yield sub.queue.shift()
                continue
              }
              await new Promise((resolveTick) => setTimeout(resolveTick, 2))
            }
          },
        }
      },
    },
    session: {
      async prompt(input) {
        promptCalls.push(input)
        // 2.3.0: admit + schedule execution, если resume !== false.
        emit({ type: "session.inbox.enqueued", sessionID: input.sessionID })
        if (input.resume === false) {
          // Карточка T3.3 использует delivered как маркер попадания в историю.
          // Спайк T0.3 (Results.md §15) уточняет, что 2.3.0 шлёт только enqueued.
          emit({ type: "session.inbox.delivered", sessionID: input.sessionID })
          return { sessionID: input.sessionID, resume: false, woken: false }
        }
        emit({ type: "session.inbox.delivered", sessionID: input.sessionID })
        emit({ type: "session.execution.started", sessionID: input.sessionID })
        return {
          sessionID: input.sessionID,
          delivery: input.delivery ?? "steer",
          woken: true,
        }
      },
    },
  }

  // GET /api/event с фильтром по sessionID тестовой сессии.
  function apiEventFeed(type) {
    return eventLog.filter(
      (event) => event.sessionID === sessionID && (!type || event.type === type),
    )
  }

  return { ctx, subscriptions, eventLog, promptCalls, emit, apiEventFeed }
}

// Шаг 1 карточки: вызов prompt по событию Beacon. S2, без experimental-механик.
async function deliverBeaconEvent(ctx, event, sessionID) {
  return ctx.session.prompt({
    sessionID,
    text: buildPromptText(event),
    delivery: "steer",
  })
}

test("плагин: id, пути состояния и подписка на факт пробуждения", () => {
  assert.equal(plugin.id, "beacon-bridge")
  for (const [key, value] of Object.entries(adapterPaths)) {
    const inside = value === adapterPaths.state || value.startsWith(adapterPaths.state + sep)
    assert.equal(inside, true, key)
  }

  const h = createHarness(TEST_SESSION)
  const stop = plugin.setup(h.ctx)
  assert.equal(typeof stop, "function")

  const wakeSubscription = h.subscriptions.find((sub) =>
    (sub.types ?? []).includes("session.execution.started"),
  )
  assert.ok(wakeSubscription, "плагин подписан на session.execution.started")

  h.emit({ type: "session.execution.started", sessionID: TEST_SESSION })
  assert.equal(wakeSubscription.seen.length, 1)
  stop()
})

test("prompt с delivery steer будит сессию: session.execution.started", async () => {
  const h = createHarness(TEST_SESSION)
  const stop = plugin.setup(h.ctx)

  const result = await deliverBeaconEvent(h.ctx, BEACON_EVENT, TEST_SESSION)

  assert.equal(result.woken, true)
  assert.equal(h.promptCalls.length, 1)
  assert.equal(h.promptCalls[0].sessionID, TEST_SESSION)
  assert.equal(h.promptCalls[0].delivery, "steer")

  // Шаг 2 карточки: только указатель и обёртка, без содержимого проверок.
  const text = h.promptCalls[0].text
  assert.ok(text.startsWith("<beacon-event>"))
  assert.ok(text.endsWith("</beacon-event>"))
  assert.ok(text.includes(BEACON_EVENT.watchID))
  assert.ok(text.includes(BEACON_EVENT.obligationID))
  assert.ok(text.includes('"revision":3'))
  assert.equal(text.includes(BEACON_EVENT.checkName), false)
  assert.equal(text.includes(BEACON_EVENT.checkBody), false)

  // Шаг 4 карточки: факт пробуждения виден в GET /api/event с нужным sessionID.
  const wake = h.apiEventFeed("session.execution.started")
  assert.equal(wake.length, 1)
  assert.equal(wake[0].sessionID, TEST_SESSION)

  // S2 выбран спайком T0.3: heartbeat S1 и onprogress не нужны.
  assert.equal(Object.hasOwn(h.promptCalls[0], "onprogress"), false)

  stop()
})

test("контроль resume:false: delivered есть, execution.started нет", async () => {
  const h = createHarness(CONTROL_SESSION)
  const stop = plugin.setup(h.ctx)

  const result = await h.ctx.session.prompt({
    sessionID: CONTROL_SESSION,
    text: buildPromptText(BEACON_EVENT),
    delivery: "steer",
    resume: false,
  })

  assert.equal(result.woken, false)
  assert.equal(h.apiEventFeed("session.inbox.delivered").length, 1)
  assert.equal(h.apiEventFeed("session.execution.started").length, 0)

  stop()
})

// HD-011 (ADR 0292): порядок создания рабочей копии зафиксирован правилом
// плагина. fetch идёт первым, worktree создаётся только после fetch.
test("HD-011: правило fetch -> worktree зафиксировано в плагине", () => {
  assert.equal(typeof FETCH_BEFORE_WORKTREE_RULE, "string")
  assert.match(FETCH_BEFORE_WORKTREE_RULE, /fetch/i)
  assert.match(FETCH_BEFORE_WORKTREE_RULE, /worktree/i)
})

test("HD-011: worktree до fetch проваливает проверку порядка", () => {
  // Правильный порядок проходит: fetch затем worktree.
  assert.equal(checkFetchBeforeWorktree(["fetch", "worktree"]).ok, true)
  assert.equal(checkFetchBeforeWorktree([
    "git fetch origin main", "git worktree add -b fix/hd011 x origin/main",
  ]).ok, true)
  assert.equal(checkFetchBeforeWorktree([{ op: "fetch" }, { op: "worktree" }]).ok, true)

  // Нарушенный порядок проваливает карточку.
  const violated = checkFetchBeforeWorktree(["worktree", "fetch"])
  assert.equal(violated.ok, false)
  assert.equal(violated.reason, "worktree before fetch")
  assert.equal(checkFetchBeforeWorktree(["worktree"]).ok, false)
  // Нет worktree — нарушать нечего.
  assert.equal(checkFetchBeforeWorktree([]).ok, true)
})
