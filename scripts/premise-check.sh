#!/usr/bin/env bash
# Premise-check до кодового PR. Три доказательства, один вердикт.
#   landed    - коммит задачи уже достижим из базовой ветки.
#   duplicate - есть открытый PR-дубль или коммит задачи вне базы.
#   clean     - дубля нет, можно открывать PR.
# Карточка обязана быть в store (docs/tasks/<CARD>.md) либо явно взята из
# бэклога через --from-backlog. Формат доказательства закрытия описан в
# docs/runbooks/CLOSE-PROCEDURE.md, а whitelist заголовков коммитов
# лежит в разделе «Whitelist шаблонов заголовков коммитов» (TCG-18) там же.
set -euo pipefail

usage() {
  echo "usage: $0 <CARD> [--from-backlog <PATH>] [--base <REF>]" >&2
  exit 2
}

die() {
  echo "ERROR: $*" >&2
  exit 2
}

verdict() {
  local result="$1"
  shift
  echo "verdict: $result"
  local line
  for line in "$@"; do
    echo "$line"
  done
  case "$result" in
    clean) exit 0 ;;
    landed | duplicate) exit 1 ;;
    *) exit 2 ;;
  esac
}

CARD="${1:-}"
[[ -n "$CARD" && "$CARD" != -* ]] || usage
shift

BACKLOG=""
BASE="origin/main"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from-backlog)
      BACKLOG="${2:-}"
      shift 2
      ;;
    --base)
      BASE="${2:-}"
      shift 2
      ;;
    *) usage ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "не git-worktree: $ROOT"

if [[ "$BASE" == origin/* ]]; then
  git fetch "${BASE%%/*}" "${BASE#*/}" >/dev/null 2>&1 || die "git fetch $BASE не удался"
fi
BASE_SHA="$(git rev-parse --verify "${BASE}^{commit}" 2>/dev/null)" || die "плохая база: $BASE"

# Происхождение карточки: store или явный бэклог. Без этого проверка не имеет
# смысла: агент мог получить задачу, которой нет ни в store, ни в бэклоге.
SOURCE=""
if [[ -f "docs/tasks/$CARD.md" ]]; then
  SOURCE="store: docs/tasks/$CARD.md"
elif [[ -n "$BACKLOG" ]]; then
  [[ -f "$BACKLOG" ]] || die "файл бэклога не найден: $BACKLOG"
  SOURCE="backlog: $BACKLOG"
elif command -v beacon >/dev/null 2>&1; then
  STORE_JSON="$(beacon task list --format json 2>/dev/null || true)"
  if printf '%s' "$STORE_JSON" | python3 -c '
import json, sys
try:
    cards = json.load(sys.stdin)
except Exception:
    sys.exit(1)
sys.exit(0 if any(c.get("id") == sys.argv[1] for c in cards) else 1)
' "$CARD"; then
    SOURCE="store: beacon task list --format json -> id=$CARD"
  fi
fi
[[ -n "$SOURCE" ]] || die "карточка $CARD не materialized в store. Передайте --from-backlog <PATH> и укажите источник в теле PR."

# Коммит с задачей: в базе даёт landed, только в другой ветке даёт duplicate.
LANDED=""
FOREIGN=""
CARD_LC="${CARD,,}"
while IFS= read -r hit; do
  [[ -z "$hit" ]] && continue
  sha="${hit%% *}"
  subj="${hit#* }"
  # --grep ищет и в теле коммита. Карточка может упоминаться в теле чужого PR,
  # поэтому дублем считаем только совпадение в заголовке коммита.
  [[ "${subj,,}" == *"$CARD_LC"* ]] || continue
  if git merge-base --is-ancestor "$sha" "$BASE_SHA" 2>/dev/null; then
    LANDED+="  $sha  $subj"$'\n'
  else
    FOREIGN+="  $sha  (вне $BASE)  $subj"$'\n'
  fi
done < <(git log --all --format='%H %s' -i --grep="$CARD")

if [[ -n "$LANDED" ]]; then
  verdict landed \
    "commit с $CARD уже достижим из $BASE_SHA:" \
    "${LANDED%$'\n'}" \
    "проверка: git merge-base --is-ancestor <SHA> $BASE -> exit 0" \
    "дубль кода не открывать; доказательство закрытия: docs/runbooks/CLOSE-PROCEDURE.md"
fi

# Открытый PR-дубль. Поиск gh смотрит и в теле PR, поэтому дублем считаем только
# совпадение в заголовке. Если gh отсутствует, доказательство агент приносит вручную.
PR_SUMMARY=""
if command -v gh >/dev/null 2>&1; then
  PR_JSON="$(gh pr list --state open --search "$CARD" --limit 20 --json number,title,headRefName 2>/dev/null || true)"
  if [[ -n "$PR_JSON" && "$PR_JSON" != "[]" ]]; then
    PR_SUMMARY="$(printf '%s' "$PR_JSON" | python3 -c '
import json, sys
try:
    prs = json.load(sys.stdin)
except Exception:
    sys.exit(0)
needle = sys.argv[1].lower()
for p in prs:
    title = p.get("title") or ""
    if needle not in title.lower():
        continue
    print("  #%s %s (%s)" % (p.get("number"), title, p.get("headRefName")))
' "$CARD" 2>/dev/null || true)"
  fi
fi

if [[ -n "$FOREIGN" || -n "$PR_SUMMARY" ]]; then
  EVIDENCE="дубль для $CARD:"
  if [[ -n "$FOREIGN" ]]; then
    EVIDENCE+=$'\n'"коммиты вне $BASE:"$'\n'"${FOREIGN%$'\n'}"
  fi
  if [[ -n "$PR_SUMMARY" ]]; then
    EVIDENCE+=$'\n'"открытые PR:"$'\n'"$PR_SUMMARY"
  fi
  verdict duplicate "$EVIDENCE" "дубль кода не открывать; доказательство: номер PR и полный SHA"
fi

verdict clean \
  "card: $CARD ($SOURCE)" \
  "base: $BASE_SHA" \
  "commit с $CARD в HEAD: нет" \
  "открытый PR-дубль: нет" \
  "можно открывать кодовый PR; вставить этот вывод в тело PR"
