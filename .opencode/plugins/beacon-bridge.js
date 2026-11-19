// Плагин-мост OpenCode. Один JS-файл, без package.json и без SDK хоста.
// T3.1 задаёт только каркас: id, подписку на события и fail-open cleanup.
// Поведение S2, prompt, экранирование и квитанции приходят в T3.2-T3.6.
import { existsSync, readdirSync, readFileSync } from "node:fs"
import { homedir } from "node:os"
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
})

export default {
  id: ID,

  setup(ctx) {
    const ac = new AbortController()

    void (async () => {
      try {
        for await (const _event of ctx.event.subscribe({
          types: ["session.execution.started"],
          signal: ac.signal,
        })) {
          // T3.1: наблюдатель без действий. Обработка появится в T3.2+.
        }
      } catch {
        // fail-open: ошибка адаптера не ломает сессию (ADR 0006).
      }
    })()

    return () => ac.abort()
  },
}

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
      // Префикс собран из частей, чтобы сам тест не содержал запрещённый литерал.
      const sdkPrefix = ["@open", "code/"].join("")
      assert.equal(source.includes(`from "${sdkPrefix}`), false)
      assert.equal(source.includes(`require("${sdkPrefix}`), false)
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
      // На этом каркасе плагин ничего не пишет. Записи появятся в T3.6
      // только через adapterPaths.receipts.
      const source = readFileSync(SELF_PATH, "utf8")
      assert.equal(/writeFileSync\(|appendFileSync\(|mkdirSync\(|\bopen\(/.test(source), false)
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
