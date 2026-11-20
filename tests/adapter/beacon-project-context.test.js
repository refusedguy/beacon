// Адаптер-тест W1-E: компактный проектный контекст сессий.
// Запуск: node --test tests/adapter/beacon-project-context.test.js
//
// Источники: ADR 0069 (решение 4), ADR 0072, требование W1-E. Тест читает
// исходники через node:fs и проверяет, что плагин больше не загружает карточки
// и не пишет состояние сессий. Остаётся только метка проекта и AGENTS.md.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { basename, dirname, join, resolve } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"
import {
  agentsFileURI,
  installProjectContext,
  registerProjectHooks,
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

test("W1-E: нет загрузки карточек, записи в сессии и слежения за файлами", () => {
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
    "docs",
    "tasks",
  ]) {
    assert.equal(source.includes(needle), false, needle)
  }
  assert.equal(/\bwatch\s*\(/.test(source), false, "fs.watch")
  assert.match(source, /hook\("context"/)
  assert.match(source, /hook\("prompt"/)
  for (const needle of ["/mnt", "/projects", "cwd: \"/"]) {
    assert.equal(source.includes(needle), false, needle)
  }
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

test("W1-E: installProjectContext ставит оба хука и снимает их при unload", async () => {
  const ctx = fakeCtx()
  // Третий аргумент приходит из index.js как locationInfo(ctx).
  const dispose = await installProjectContext(ctx, root, { directory: root })

  assert.deepEqual(Object.keys(ctx.hooks).sort(), ["context", "prompt"])
  assert.deepEqual(ctx.disposed, [])

  await dispose()
  assert.deepEqual(ctx.disposed.sort(), ["context", "prompt"])
})
