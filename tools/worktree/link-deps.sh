#!/usr/bin/env bash
# T3.15. Общий кэш зависимостей для worktree (ADR 0030 п5, ADR 0021).
#
# Скрипт создаёт в worktree символические ссылки на общее хранилище кэша.
# Хранилище лежит вне worktree, поэтому все агенты видят одни и те же
# каталоги и не устанавливают зависимости повторно. Пути ссылок перечислены
# в .gitignore и не попадают в diff.
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
  --self-test          Проверить, что два worktree видят один кэш, в tmp.
  --help|-h            Показать эту справку.

Переменные окружения: LINK_DEPS_CACHE, LINK_DEPS_DIRS (список через запятую).

Пример:
  bash tools/worktree/link-deps.sh --worktree worktrees/w72
  bash tools/worktree/link-deps.sh --all
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
DIRS=()
sandbox=""

while [ "$#" -gt 0 ]; do
    case "$1" in
        --worktree) WORKTREE="${2:?}"; shift 2 ;;
        --cache) CACHE_ROOT="${2:?}"; shift 2 ;;
        --cache-dir) DIRS+=("${2:?}"); shift 2 ;;
        --all) ALL=1; shift ;;
        --self-test) SELF_TEST=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) printf 'неизвестный флаг: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
done

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

    log "self-test ok: wt1 и wt2 видят один каталог $cache/$name (inode $inode1), ссылки игнорируются git"
}

for tool in git mkdir ln readlink stat mktemp cp; do
    command -v "$tool" >/dev/null 2>&1 || die "нет команды $tool"
done

if [ "$SELF_TEST" = 1 ]; then
    self_test
    exit 0
fi

if [ -z "$CACHE_ROOT" ]; then
    CACHE_ROOT="$(repo_root)/.deps-cache"
fi

if [ "$ALL" = 1 ]; then
    link_all "$CACHE_ROOT"
else
    link_one "${WORKTREE:-$PWD}" "$CACHE_ROOT"
fi
