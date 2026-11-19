# Шаблон задания исполнителя

Шаблон задания для агента-исполнителя в цикле Beacon. Исполнитель регистрирует Watch, сдаёт PR, завершает ход фиксированным отчётом и просыпается только по трём вердиктам. Сон означает завершение хода, а не опрос в цикле. Основа: WORKER-PROMPT §4.7, ADR 0004, ADR 0019, ADR 0021.

## Входные данные

| Поле | Источник | Назначение |
| --- | --- | --- |
| cardID | карточка задачи в `docs/tasks/` | что делает исполнитель |
| repo, pr, headSHA, baseRef | факт сдачи PR | параметры Watch |
| watchID | ответ `beacon_watch_pr` | ключ ожидания и отчёта |
| verdict | `beacon_get_result` | решение о пробуждении |

## Цикл: register, push PR, finish turn

1. **register.** Вызвать `beacon_watch_pr` с параметрами PR и сохранить возвращённый `watchID`. Повторный вызов с теми же параметрами идемпотентен. Один агент может держать несколько Watch, ограничений на параллельность нет (ADR 0001).
2. **push PR.** Сделать изменения в своём worktree и своей ветке, запушить ветку, открыть PR в `main`. Проверки пользователь запускает своими правами. Beacon только наблюдает за PR и детектирует сброс suite, он read-only по CI (ADR 0004).
3. **finish turn.** Завершить ход с отчётом ровно `waiting, watchID`, подставив сохранённый `watchID`. Не опрашивать состояние в цикле и не ждать вручную. Дальше агент спит до вердикта, который разрешает пробуждение.

## Обязательный текст отчёта

Отчёт о завершении хода содержит ровно строку `waiting, watchID`, где `waiting` литерал, а `watchID` подставляется из шага register. Любая другая формулировка не является корректным завершением хода.

<!-- executor-task:report
waiting, watchID
executor-task:report -->

## Вердикты и пробуждение

Агент просыпается только на `keep_working`, `rebase` и `retry`. Остальные вердикты пробуждения не дают. Переход `watching` в `watching` не двигает revision и никого не будит.

| вердикт | пробуждение |
| --- | --- |
| keep_working | да |
| rebase | да |
| retry | да |
| ready | нет |
| watching | нет |
| merged | нет |
| closed | нет |
| timeout | нет |

<!-- executor-task:verdicts
wake: keep_working, rebase, retry
nowake: ready, watching, merged, closed, timeout
executor-task:verdicts -->

## Re-run проверок

Повтор проверок делает агент своими правами командой `gh run rerun --failed`. Beacon не выполняет эту команду от имени агента и не запускает проверки. При сбросе suite Beacon меняет вердикт на `watching` и увеличивает revision, после чего агент сам решает, повторить проверку или переделать код.

<!-- executor-task:rerun-owner
agent
executor-task:rerun-owner -->

## Нет TTL в цикле

У Watch нет жесткого TTL по времени. Watch закрывается по терминальному вердикту, по отмене или по смене headSHA (ADR 0019). Один агент может делать множество rebase в одной цепочке, число rebase не ограничено. Долгая цепочка rebase не обрывается по времени. Orphan с неактивным подписчиком обрабатывается эскалацией оркестратору, а не тихим удалением Watch.

<!-- executor-task:ttl
none
executor-task:ttl -->

## Запреты

- Не запускать опрос в цикле вместо завершения хода.
- Не выдавать готовность по idle-отчёту и по вердикту `ready` без решения оркестратора (T3.8).
- Не поручать Beacon команды, которые выполняет агент. Beacon только детектирует состояние и шлёт указатель на запись.
- Не читать внутренние логи и базы агента как источник результата (ADR 0027).

## Самопроверка шаблона

Тест ниже разбирает разметку этого файла и проверяет четыре условия карточки: `ready` и `watching` не будят, `keep_working`, `rebase` и `retry` будят, текст `waiting, watchID` присутствует, у Beacon нет команды от имени агента. Дополнительно проверяется, что re-run принадлежит агенту и TTL отсутствует.

Чтобы запустить, сохранить блок между маркерами `js-test:start` и `js-test:end` в файл `executor-task.test.js` рядом с этим шаблоном и выполнить `node --test docs/templates/executor-task.test.js`. Тест использует только встроенные модули Node.

<!-- js-test:start
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const templatePath = path.join(__dirname, "executor-task.md");
const text = fs.readFileSync(templatePath, "utf8");

function block(name) {
  const m = text.match(
    new RegExp(
      `<!--\\s*executor-task:${name}\\s*\\n([\\s\\S]*?)\\n\\s*executor-task:${name}\\s*-->`
    )
  );
  assert.ok(m, `missing block ${name}`);
  return m[1].trim();
}

function verdictSet(line) {
  return new Set(
    line.split(":")[1].split(",").map((v) => v.trim()).filter(Boolean)
  );
}

test("шаблон: три вердикта будят, остальные нет", () => {
  const b = block("verdicts");
  const wake = verdictSet(b.split("\n").find((l) => l.startsWith("wake:")));
  const nowake = verdictSet(b.split("\n").find((l) => l.startsWith("nowake:")));

  assert.deepEqual([...wake].sort(), ["keep_working", "rebase", "retry"]);
  for (const v of ["ready", "watching", "merged", "closed", "timeout"]) {
    assert.equal(wake.has(v), false, `${v} must not wake`);
    assert.equal(nowake.has(v), true, `${v} must be listed as nowake`);
  }
  for (const v of ["keep_working", "rebase", "retry"]) {
    assert.equal(nowake.has(v), false, `${v} must not be listed as nowake`);
  }
});

test("шаблон: отчёт содержит waiting, watchID", () => {
  assert.equal(block("report"), "waiting, watchID");
});

test("шаблон: re-run делает агент, у Beacon нет команды от его имени", () => {
  assert.equal(block("rerun-owner"), "agent");
  assert.match(text, /gh run rerun --failed/);
  const forbidden = new RegExp("beacon[^\\n]*" + "gh run" + " rerun --failed", "i");
  assert.equal(forbidden.test(text), false);
});

test("шаблон: TTL не является причиной выхода из цикла", () => {
  assert.equal(block("ttl"), "none");
});
js-test:end -->
