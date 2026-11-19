#!/usr/bin/env bash
# T3.15. Общий кэш зависимостей для worktree (ADR 0030 п5, ADR 0021).
# T3.69. Тёплая песочница после выхода агента (ADR 0040 п4).
#
# Скрипт создаёт в worktree символические ссылки на общее хранилище кэша.
# Хранилище лежит вне worktree, поэтому все агенты видят одни и те же
# каталоги и не устанавливают зависимости повторно. Пути ссылок перечислены
# в .gitignore и не попадают в diff.
#
# Тёплая песочница это состояние кэша, привязанное к владельцу и дереву задач.
# Режим --warm переиспользует его при повторном запуске без повторной загрузки
# окружения. Состояние переживает выход одного агента и снимается явным
# --release при снятии задачи. Граница по владельцу и задаче не даёт одному
# агенту переиспользовать песочницу другого (ADR 0040 п4, ADR 0021).
#
# Скрипт не запускает go test и не является локальным гейтом (ADR 0022).
# Единственная проверка карточки: bash tools/worktree/link-deps.sh --self-test.

set -euo pipefail

usage() {
    cat <<'EOF'
Использование: bash tools/worktree/link-deps.sh [флаги]

  --worktree PATH      Worktree для линковки. По умолчанию текущий каталог.
  --cache PATH         Общее хранилище кэша. По умолчанию .deps-cache в корне репозитория.
  --cache-dir NAME     Имя каталога кэша. Можно повторять. По умолчанию node_modules.
  --all                Линкнуть все worktree репозитория из git worktree list.
  --warm               Переиспользовать тёплую песочницу владельца и задачи.
  --release            Явно освободить тёплую песочницу владельца и задачи.
  --owner NAME         Владелец песочницы. Требуется с --warm и --release.
  --task KEY           Ключ дерева задач. Требуется с --warm и --release.
  --self-test          Проверить общий кэш и тёплую песочницу в tmp.
  --help|-h            Показать эту справку.

Переменные окружения: LINK_DEPS_CACHE, LINK_DEPS_DIRS (список через запятую).

Пример:
  bash tools/worktree/link-deps.sh --worktree worktrees/w72
  bash tools/worktree/link-deps.sh --all
  bash tools/worktree/link-deps.sh --warm --owner agent-a --task T3.69
  bash tools/worktree/link-deps.sh --release --owner agent-a --task T3.69
  bash tools/worktree/link-deps.sh --self-test
EOF
}

log() { printf '[link-deps] %s\n' "$*" >&2; }
die() { printf '[link-deps] ОШИБКА: %s\n' "$*" >&2; exit 2; }

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

WORKTREE=""
CACHE_ROOT="${LINK_DEPS_CACHE:-}"
ALL=0
SELF_TEST=0
WARM=0
RELEASE=0
OWNER=""
TASK=""
DIRS=()
sandbox=""
WARM_SEQ=0
WARM_STATE=".warm-sandbox"

while [ "$#" -gt 0 ]; do
    case "$1" in
        --worktree) WORKTREE="${2:?}"; shift 2 ;;
        --cache) CACHE_ROOT="${2:?}"; shift 2 ;;
        --cache-dir) DIRS+=("${2:?}"); shift 2 ;;
        --all) ALL=1; shift ;;
        --warm) WARM=1; shift ;;
        --release) RELEASE=1; shift ;;
        --owner) OWNER="${2:?}"; shift 2 ;;
        --task) TASK="${2:?}"; shift 2 ;;
        --self-test) SELF_TEST=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) printf 'неизвестный флаг: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
done

if [ "$WARM" = 1 ] && [ "$RELEASE" = 1 ]; then
    die "--warm и --release несовместимы"
fi

if [ "${#DIRS[@]}" -eq 0 ]; then
    if [ -n "${LINK_DEPS_DIRS:-}" ]; then
        IFS=',' read -r -a DIRS <<< "$LINK_DEPS_DIRS"
    else
        DIRS=(node_modules)
    fi
fi

clean_dirs=()
for name in "${DIRS[@]}"; do
    name="$(printf '%s' "$name" | tr -d '[:space:]')"
    [ -n "$name" ] || continue
    [[ "$name" =~ ^[A-Za-z0-9._-]+$ ]] || die "недопустимое имя каталога кэша: $name"
    [ "$name" != . ] && [ "$name" != .. ] || die "недопустимое имя каталога кэша: $name"
    clean_dirs+=("$name")
done
[ "${#clean_dirs[@]}" -gt 0 ] || die "не задан ни один каталог кэша"
DIRS=("${clean_dirs[@]}")

# Корень основного worktree берём из общего git-каталога: в linked worktree
# git rev-parse --show-toplevel вернул бы сам linked worktree, а не main.
repo_root() {
    local common
    common="$(git -C "$ROOT" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
    if [ -z "$common" ]; then
        die "не удалось найти общий git-каталог"
    fi
    (cd "$ROOT" && dirname "$common")
}

abs_path() {
    local path="$1"
    if [ -d "$path" ]; then
        (cd "$path" && pwd -P)
    else
        (cd "$(dirname "$path")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$path")")
    fi
}

# link_one WORKTREE CACHE_ROOT
# Идемпотентно: повторный вызов не меняет уже правильные символические ссылки.
link_one() {
    local wt="$1" cache="$2" name shared target existing
    [ -d "$wt" ] || die "нет каталога worktree: $wt"
    wt="$(abs_path "$wt")"
    mkdir -p "$cache"
    cache="$(abs_path "$cache")"

    for name in "${DIRS[@]}"; do
        shared="$cache/$name"
        target="$wt/$name"
        mkdir -p "$shared"
        if [ -L "$target" ]; then
            existing="$(readlink "$target")"
            if [ "$existing" != "$shared" ]; then
                die "symlink $target указывает на $existing, ожидалось $shared"
            fi
            log "уже линкнут: $target -> $shared"
        elif [ -e "$target" ]; then
            die "в $target лежит обычный файл или каталог, линковка заменила бы данные"
        else
            ln -s "$shared" "$target"
            log "создан symlink: $target -> $shared"
        fi
    done
}

link_all() {
    local cache="$1" line wt found=0
    while IFS= read -r line; do
        case "$line" in
            worktree\ *) wt="${line#worktree }" ;;
            *) continue ;;
        esac
        [ -d "$wt" ] || continue
        link_one "$wt" "$cache"
        found=$((found + 1))
    done < <(git -C "$ROOT" worktree list --porcelain)
    [ "$found" -gt 0 ] || die "git worktree list не вернул ни одного worktree"
}

# --- Тёплая песочница (ADR 0040 п4). ---
# Состояние хранится внутри общего кэша, поэтому переживает выход агента и
# остаётся доступным следующему запуску с тем же владельцем и задачей. Ключ
# владелец+задача задаёт границу: чужая песочница не переиспользуется, пока её
# явно не освободят --release. Снятие задачи снимает и песочницу.

check_warm_key() {
    [[ "$OWNER" =~ ^[A-Za-z0-9._@/-]+$ ]] || die "недопустимое имя владельца: $OWNER"
    [[ "$TASK" =~ ^[A-Za-z0-9._@/-]+$ ]] || die "недопустимый ключ задачи: $TASK"
}

# warm_field ФАЙЛ КЛЮЧ — прочитать поле из manifests без внешних утилит.
warm_field() {
    local file="$1" key="$2" line
    while IFS= read -r line; do
        case "$line" in
            "$key="*) printf '%s' "${line#*=}"; return 0 ;;
        esac
    done < "$file"
    return 1
}

# warm_release КЭШ — явное освобождение: снимает состояние задачи.
warm_release() {
    local cache state owner task
    cache="$1"
    state="$cache/$WARM_STATE"
    if [ ! -f "$state" ]; then
        log "тёплой песочницы нет, освобождать нечего"
        return 0
    fi
    owner="$(warm_field "$state" owner || true)"
    task="$(warm_field "$state" task || true)"
    if [ "$owner" != "$OWNER" ] || [ "$task" != "$TASK" ]; then
        die "песочница принадлежит $owner/$task, а не $OWNER/$TASK"
    fi
    rm -f -- "$state"
    log "тёплая песочница освобождена: $owner/$task"
}

# warm_apply КЭШ — создать или переиспользовать состояние владельца и задачи.
warm_apply() {
    local cache state owner task token tmp
    cache="$1"
    state="$cache/$WARM_STATE"
    if [ -f "$state" ]; then
        owner="$(warm_field "$state" owner || true)"
        task="$(warm_field "$state" task || true)"
        if [ "$owner" = "$OWNER" ] && [ "$task" = "$TASK" ]; then
            token="$(warm_field "$state" token || true)"
            log "тёплая песочница переиспользована: $owner/$task (token $token), повторной загрузки нет"
            return 0
        fi
        die "песочница занята $owner/$task; освободи её --release перед $OWNER/$TASK"
    fi

    token="${RANDOM}${RANDOM}$$${WARM_SEQ}"
    WARM_SEQ=$((WARM_SEQ + 1))
    tmp="$state.tmp.$$"
    {
        printf 'owner=%s\n' "$OWNER"
        printf 'task=%s\n' "$TASK"
        printf 'token=%s\n' "$token"
        printf 'created=%s\n' "${EPOCHREALTIME:-$$}"
    } > "$tmp"
    mv -f -- "$tmp" "$state"
    log "тёплая песочница создана: $OWNER/$TASK (token $token)"
}

self_test() {
    local base=/tmp/opencode
    [ -d "$base" ] || base="${TMPDIR:-/tmp}"
    sandbox="$(mktemp -d "$base/link-deps-w72.XXXXXX")"
    trap 'rm -rf -- "$sandbox"' EXIT

    local cache="$sandbox/cache" wt1="$sandbox/wt1" wt2="$sandbox/wt2"
    mkdir -p "$cache" "$wt1" "$wt2"
    git init -q "$sandbox"
    cp "$ROOT/.gitignore" "$sandbox/.gitignore"

    link_one "$wt1" "$cache"
    link_one "$wt2" "$cache"

    local name link1 link2 inode1 inode2
    name="${DIRS[0]}"
    link1="$wt1/$name"
    link2="$wt2/$name"
    [ -L "$link1" ] || die "self-test: $link1 не символическая ссылка"
    [ -L "$link2" ] || die "self-test: $link2 не символическая ссылка"
    [ "$(readlink "$link1")" = "$cache/$name" ] || die "self-test: неверная цель ссылки wt1"
    [ "$(readlink "$link2")" = "$cache/$name" ] || die "self-test: неверная цель ссылки wt2"

    printf '%s\n' 'общий-кэш' > "$link1/marker"
    [ "$(cat "$link2/marker")" = 'общий-кэш' ] || die "self-test: wt2 не видит запись wt1"

    inode1="$(stat -Lc '%d:%i' -- "$link1")"
    inode2="$(stat -Lc '%d:%i' -- "$link2")"
    [ "$inode1" = "$inode2" ] || die "self-test: worktree линкнуты на разные каталоги"

    # Повторный вызов не ломает уже созданные ссылки.
    link_one "$wt1" "$cache"
    link_one "$wt2" "$cache"
    [ "$(readlink "$link1")" = "$cache/$name" ] || die "self-test: ссылка wt1 изменилась при повторе"
    [ "$(readlink "$link2")" = "$cache/$name" ] || die "self-test: ссылка wt2 изменилась при повторе"

    # Пути кэша обязаны быть проигнорированы git, иначе они попадут в diff.
    (cd "$sandbox" && git check-ignore -q -- "wt1/$name") \
        || die "self-test: wt1/$name не проигнорирован .gitignore"
    (cd "$sandbox" && git check-ignore -q -- "wt2/$name") \
        || die "self-test: wt2/$name не проигнорирован .gitignore"
    (cd "$sandbox" && git check-ignore -q -- .deps-cache/) \
        || die "self-test: .deps-cache/ не проигнорирован .gitignore"

    # Тёплая песочница: состояние переживает выход агента и не пересоздаётся.
    OWNER="agent-a"
    TASK="T3.69"
    local state="$cache/$WARM_STATE" token1 token2 token3 created
    # Вызов без внешней переменной cache: ловит случайное заимствование
    # одноимённой переменной из вызывающего контекста.
    ( unset cache; warm_apply "$sandbox/cache" )
    [ -f "$state" ] || die "self-test: состояние песочницы не создано"
    token1="$(warm_field "$state" token || true)"
    created="$(warm_field "$state" created || true)"
    [ -n "$token1" ] || die "self-test: пустой token песочницы"
    [ -n "$created" ] || die "self-test: пустое created песочницы"

    # Повторный запуск с тем же владельцем и задачей не загружает окружение заново.
    warm_apply "$cache"
    token2="$(warm_field "$state" token || true)"
    [ "$token2" = "$token1" ] || die "self-test: повторный запуск пересоздал песочницу"

    # Чужой владелец или задача не переиспользуют и не освобождают песочницу.
    if ( OWNER="agent-b"; TASK="T3.70"; warm_apply "$cache" ) 2>/dev/null; then
        die "self-test: чужая задача переиспользовала песочницу"
    fi
    if ( OWNER="agent-b"; TASK="T3.70"; warm_release "$cache" ) 2>/dev/null; then
        die "self-test: чужой --release снял песочницу"
    fi
    [ -f "$state" ] || die "self-test: песочница пропала после чужого --release"
    [ "$(warm_field "$state" token || true)" = "$token1" ] \
        || die "self-test: чужой запуск изменил токен песочницы"

    # Явное освобождение при снятии задачи снимает состояние.
    warm_release "$cache"
    [ ! -f "$state" ] || die "self-test: --release не снял состояние песочницы"

    # После освобождения следующий запуск создаёт новое окружение.
    warm_apply "$cache"
    token3="$(warm_field "$state" token || true)"
    [ -n "$token3" ] || die "self-test: песочница не воссоздана после --release"
    [ "$token3" != "$token1" ] || die "self-test: после --release токен не изменился"

    log "self-test ok: wt1 и wt2 видят один каталог $cache/$name (inode $inode1); тёплая песочница $OWNER/$TASK переиспользуется без загрузки и снимается --release"
}

for tool in git mkdir ln readlink stat mktemp cp mv rm; do
    command -v "$tool" >/dev/null 2>&1 || die "нет команды $tool"
done

if [ "$SELF_TEST" = 1 ]; then
    self_test
    exit 0
fi

if [ -z "$CACHE_ROOT" ]; then
    CACHE_ROOT="$(repo_root)/.deps-cache"
fi

if [ "$RELEASE" = 1 ]; then
    check_warm_key
    warm_release "$CACHE_ROOT"
    exit 0
fi

if [ "$ALL" = 1 ]; then
    link_all "$CACHE_ROOT"
else
    link_one "${WORKTREE:-$PWD}" "$CACHE_ROOT"
fi

if [ "$WARM" = 1 ]; then
    check_warm_key
    warm_apply "$CACHE_ROOT"
fi
