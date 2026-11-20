// Адаптер-тест T3.178: проектный контекст сессий.
// Запуск: node --test tests/adapter/beacon-project-context.test.js
//
// Источники: карточка T3.178, ADR 0069 (решение 4), документация OpenCode
// "Server plugin API / Sessions and Hooks". Тест читает исходники через node:fs
// и проверяет поведение чистых функций context.js на фиктивном ctx.

import { test } from "node:test"
import assert from "node:assert/strict"
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { basename, dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import {
  AGENTS_KEY,
  CARD_KEY,
  listLocationSessions,
  projectContext,
  readOpenCards,
  registerProjectHooks,
  sessionBelongsToLocation,
  syncSessionInstructions,
} from "../../.opencode/plugins/beacon-project/context.js"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const source = readFileSync(
  join(REPO_ROOT, ".opencode", "plugins", "beacon-project", "context.js"),
  "utf8",
)

function makeProject() {
  const root = mkdtempSync(join(tmpdir(), "beacon-t3178-"))
  mkdirSync(join(root, "docs", "tasks"), { recursive: true })
  writeFileSync(
    join(root, "docs", "tasks", "T3.900.md"),
    "# T3.900. Открытая карточка\nСтатус: todo\n",
  )
  writeFileSync(
    join(root, "docs", "tasks", "T3.901.md"),
    "# T3.901. Закрытая карточка\nСтатус: done\n",
  )
  writeFileSync(join(root, "AGENTS.md"), "правила проекта\n")
  return root
}

function fakeCtx(location) {
  return { location }
}

const location = {
  directory: "/srv/project/.worktree/w316",
  project: {
    id: "p_1",
    directory: "/srv/project",
    canonical: "/srv/project",
  },
}

test("T3.178: записи под фиксированными ключами через instructions.entry.put", () => {
  assert.equal(CARD_KEY, "beacon/open-cards")
  assert.equal(AGENTS_KEY, "beacon/agents-md")
  assert.match(source, /instructions\.entry\.put/)
  assert.match(source, /hook\("context"/)
  assert.match(source, /hook\("prompt"/)
  assert.match(source, /project\.canonical|ctx\.location/)
  for (const needle of ["/mnt", "/projects", "cwd: \"/"]) {
    assert.equal(source.includes(needle), false, needle)
  }
})

test("T3.178: открытые карточки и AGENTS.md читаются из канонического каталога", () => {
  const root = makeProject()
  try {
    const cards = readOpenCards(root)
    assert.match(cards, /T3\.900/)
    assert.equal(cards.includes("T3.901"), false)
    const entries = projectContext(root)
    assert.equal(entries[CARD_KEY], cards)
    assert.equal(entries[AGENTS_KEY], "правила проекта\n")
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test("T3.178: сессии другого проекта не получают контекст", async () => {
  const ctx = fakeCtx(location)
  assert.equal(
    sessionBelongsToLocation(ctx, { id: "own", directory: location.directory }),
    true,
  )
  assert.equal(
    sessionBelongsToLocation(ctx, { id: "child", directory: join(location.directory, "nested") }),
    true,
  )
  assert.equal(
    sessionBelongsToLocation(ctx, { id: "canonical", directory: location.project.canonical }),
    false,
  )
  assert.equal(
    sessionBelongsToLocation(ctx, { id: "other", directory: "/srv/another-project" }),
    false,
  )

  const root = makeProject()
  try {
    const puts = []
    const ctxWithAPI = {
      ...ctx,
      session: {
        instructions: {
          entry: {
            async put(input) {
              puts.push(input)
            },
          },
        },
        async get({ sessionID }) {
          return {
            own: { id: "own", directory: location.directory },
            other: { id: "other", directory: "/srv/another-project" },
          }[sessionID]
        },
      },
    }
    const sessions = await listLocationSessions({
      ...ctxWithAPI,
      session: {
        ...ctxWithAPI.session,
        async list(input) {
          assert.equal(input.directory, location.directory)
          return {
            sessions: [
              { id: "own", directory: location.directory },
              { id: "other", directory: "/srv/another-project" },
            ],
            cursor: { next: null },
          }
        },
      },
    })
    const written = await syncSessionInstructions(ctxWithAPI, root, sessions)
    assert.equal(written, 2)
    assert.deepEqual(
      puts.map((item) => item.key).sort(),
      [AGENTS_KEY, CARD_KEY].sort(),
    )
    assert.equal(puts.every((item) => item.sessionID === "own"), true)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test("T3.178: хуки context и prompt идемпотентны при повторе промпта", async () => {
  const root = makeProject()
  try {
    const hooks = {}
    const ctx = {
      ...fakeCtx(location),
      session: {
        async hook(name, handler) {
          hooks[name] = handler
          return { dispose: async () => {} }
        },
      },
    }
    await registerProjectHooks(ctx, root)

    const outgoing = {
      system: [{ type: "text", text: "core" }],
      messages: [{ role: "user" }],
    }
    hooks.context(outgoing)
    assert.equal(outgoing.system.length, 2)
    assert.equal(outgoing.system[1].text.includes(root), true)
    assert.equal(outgoing.messages.length, 1, "context-хук не меняет историю")

    const agentsName = basename(join(root, "AGENTS.md"))
    const event = { prompt: { text: "привет", files: [] } }
    hooks.prompt(event)
    hooks.prompt(event)
    assert.equal(event.prompt.files.length, 1)
    assert.equal(event.prompt.files[0].uri.endsWith("/" + agentsName), true)
    assert.equal(event.prompt.text, "привет")
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
