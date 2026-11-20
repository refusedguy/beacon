// Адаптер-тест W1-E: компактный проектный контекст сессий.
// Запуск: node --test tests/adapter/beacon-project-context.test.js
//
// Источники: ADR 0069 (решение 4), ADR 0072, требование W1-E. Тест читает
// исходники через node:fs и проверяет, что плагин не загружает карточки и не
// следит за файлами. В сессию пишется только компактный ключ правил.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { basename, dirname, join, resolve } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"
import {
  SURVIVAL_KEY,
  SURVIVAL_RULES,
  agentsFileURI,
  installProjectContext,
  listSessions,
  registerCompactionHook,
  registerProjectHooks,
  syncSurvivalRules,
} from "../../.opencode/plugins/beacon-project/context.js"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const source = readFileSync(
  join(REPO_ROOT, ".opencode", "plugins", "beacon-project", "context.js"),
  "utf8",
)

const root = "/srv/project/.worktree/w316"

function fakeCtx() {
  const hooks = {}
  const disposed = []
  return {
    hooks,
    disposed,
    session: {
      async hook(name, handler) {
        hooks[name] = handler
        return {
          async dispose() {
            disposed.push(name)
          },
        }
      },
    },
  }
}

test("W1-E: нет дампа карточек и слежения за файлами, есть ключ правил", () => {
  for (const needle of [
    "readOpenCards",
    "readdirSync",
    "instructions.entry.put",
    "syncSessionInstructions",
    "listLocationSessions",
    "sessionBelongsToLocation",
    "CARD_KEY",
    "beacon/open-cards",
    "node:fs",
  ]) {
    assert.equal(source.includes(needle), false, needle)
  }
  assert.equal(/\bwatch\s*\(/.test(source), false, "fs.watch")
  assert.match(source, /hook\("context"/)
  assert.match(source, /hook\("prompt"/)
  assert.match(source, /hook\("compaction"/)
  assert.match(source, /beacon\/survival-rules/)
  assert.match(source, /entry\.put/)
  for (const needle of ["/mnt", "/projects", "cwd: \"/"]) {
    assert.equal(source.includes(needle), false, needle)
  }
})

test("W1-E: правила короткие и покрывают контракт выживания", () => {
  assert.equal(SURVIVAL_KEY, "beacon/survival-rules")
  const lines = SURVIVAL_RULES.split("\n")
  assert.ok(lines.length <= 10, "шпаргалка до 10 строк")
  for (const needle of [
    "worktree",
    "main",
    "go test ./...",
    "go build ./...",
    "beacon gates",
    "gofmt -w",
    "OwnerFiles",
    "docs/tasks/",
    "gh pr checks --watch",
    "sleep",
    "gh pr create",
    'role: "executor"',
    "ses_...",
    "waiting, <watchID>",
    "beacon_get_result",
    "failedChecks",
    'decision="blocked:not_mine"',
  ]) {
    assert.equal(SURVIVAL_RULES.includes(needle), true, needle)
  }
})

test("W1-E: стабильный ключ перезаписывается и идёт только своим сессиям", async () => {
  const location = { directory: root, project: { canonical: "/srv/project" } }
  const writes = []
  const ctx = {
    location,
    session: {
      instructions: {
        entry: {
          async put(input) {
            writes.push(input)
          },
        },
      },
      async get({ sessionID }) {
        return {
          own: { id: "own", directory: root },
          other: { id: "other", directory: "/srv/another-project" },
        }[sessionID]
      },
      async list() {
        return {
          sessions: [
            { id: "own", directory: root },
            { id: "other", directory: "/srv/another-project" },
          ],
          cursor: { next: null },
        }
      },
    },
  }

  const written = await syncSurvivalRules(ctx, await listSessions(ctx))
  assert.equal(written, 1)
  assert.deepEqual(writes, [
    { sessionID: "own", key: SURVIVAL_KEY, value: SURVIVAL_RULES },
  ])
})

test("W1-E: context-хук добавляет только метку проекта и не меняет историю", async () => {
  const ctx = fakeCtx()
  await registerProjectHooks(ctx, root)

  const event = {
    system: [{ type: "text", text: "core" }],
    messages: [{ role: "user" }],
  }
  ctx.hooks.context(event)

  assert.deepEqual(event.system, [
    { type: "text", text: "core" },
    { type: "text", text: `beacon project root=${root}` },
  ])
  assert.equal(event.messages.length, 1, "context-хук не меняет историю")
})

test("W1-E: prompt-хук один раз привязывает AGENTS.md файлом", async () => {
  const ctx = fakeCtx()
  await registerProjectHooks(ctx, root)

  const agentsURI = agentsFileURI(root)
  assert.equal(agentsURI, pathToFileURL(join(root, "AGENTS.md")).href)
  assert.equal(agentsURI.endsWith("/" + basename(join(root, "AGENTS.md"))), true)

  const event = { prompt: { text: "привет" } }
  ctx.hooks.prompt(event)
  ctx.hooks.prompt(event)

  assert.deepEqual(event.prompt.files, [{ uri: agentsURI }])
  assert.equal(event.prompt.text, "привет")
})

test("W1-E: installProjectContext ставит три хука и снимает их при unload", async () => {
  const ctx = fakeCtx()
  // Третий аргумент приходит из index.js как locationInfo(ctx).
  const dispose = await installProjectContext(ctx, root, { directory: root })

  assert.deepEqual(Object.keys(ctx.hooks).sort(), ["compaction", "context", "prompt"])
  assert.deepEqual(ctx.disposed, [])

  await dispose()
  assert.deepEqual(ctx.disposed.sort(), ["compaction", "context", "prompt"])
})

test("W1-E: compaction-хук перезаписывает тот же ключ и не ломается без хоста", async () => {
  const ctx = fakeCtx()
  const writes = []
  ctx.session.instructions = {
    entry: {
      async put(input) {
        writes.push(input)
      },
    },
  }
  await registerCompactionHook(ctx)

  await ctx.hooks.compaction({ sessionID: "own" })
  assert.deepEqual(writes, [
    { sessionID: "own", key: SURVIVAL_KEY, value: SURVIVAL_RULES },
  ])

  const oldHost = { session: { async hook() { throw new Error("unknown hook") } } }
  assert.equal(await registerCompactionHook(oldHost), null)
})
