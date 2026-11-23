#!/usr/bin/env bash
# Держит commit синхронизации реестра проверок в соответствии с покрываемыми путями.
set -euo pipefail

mode="${1:-}"
case "$mode" in
  --refresh|--check) ;;
  *)
    echo "usage: bash scripts/verification-registry.sh --refresh|--check" >&2
    exit 2
    ;;
esac

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="$repo/docs/runbooks/verification.md"
tick="$(printf '\x60')"

trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

mapfile -t lines < "$registry"
declare -a indexes=() checks=() expected=() recorded=()

in_registry=0
begin_seen=0
end_seen=0
for i in "${!lines[@]}"; do
  line="${lines[$i]}"
  if [[ "$line" == '<!-- BEGIN VERIFICATION REGISTRY -->' ]]; then
    in_registry=1
    begin_seen=1
    continue
  fi
  if [[ "$line" == '<!-- END VERIFICATION REGISTRY -->' ]]; then
    in_registry=0
    end_seen=1
    continue
  fi
  ((in_registry)) || continue
  IFS='|' read -r _ c1 c2 _ _ c5 _ <<< "$line"
  c1="$(trim "$c1")"
  [[ "$c1" == "$tick"*"$tick" ]] || continue
  [[ "$c2" =~ paths:[[:space:]]*$tick([^$tick]+)$tick ]] || {
    echo "registry:$((i + 1)): нет списка paths" >&2
    exit 1
  }
  pathspec="${BASH_REMATCH[1]}"
  IFS=',' read -r -a raw_paths <<< "$pathspec"
  paths=()
  for p in "${raw_paths[@]}"; do
    p="$(trim "$p")"
    [[ -n "$p" ]] || { echo "registry:$((i + 1)): пустой путь" >&2; exit 1; }
    paths+=("$p")
  done
  head="$(git -C "$repo" log -1 --format=%H -- "${paths[@]}")" || {
    echo "registry:$((i + 1)): git log не удался" >&2
    exit 1
  }
  [[ "$head" =~ ^[0-9a-f]{40}$ ]] || {
    echo "registry:$((i + 1)): нет commit по покрываемым путям" >&2
    exit 1
  }
  c5="$(trim "$c5")"
  c5="${c5//$tick/}"
  [[ "$c5" =~ ^[0-9a-f]{40}$ ]] || {
    echo "registry:$((i + 1)): некорректный commit синхронизации" >&2
    exit 1
  }
  indexes+=("$i")
  checks+=("$c1")
  expected+=("$head")
  recorded+=("$c5")
done

((begin_seen && end_seen)) || { echo "registry: нет маркеров BEGIN/END" >&2; exit 1; }
((${#indexes[@]})) || { echo "registry: строки реестра не найдены" >&2; exit 1; }

if [[ "$mode" == "--refresh" ]]; then
  changed=0
  for j in "${!indexes[@]}"; do
    if [[ "${recorded[$j]}" != "${expected[$j]}" ]]; then
      idx="${indexes[$j]}"
      lines[$idx]="${lines[$idx]/${recorded[$j]}/${expected[$j]}}"
      changed=$((changed + 1))
    fi
  done
  if ((changed)); then
    printf '%s\n' "${lines[@]}" > "$registry"
  fi
  echo "REFRESH updated=$changed"
  exit 0
fi

status=0
for j in "${!indexes[@]}"; do
  name="${checks[$j]//$tick/}"
  if [[ "${recorded[$j]}" != "${expected[$j]}" ]]; then
    echo "STALE $name recorded=${recorded[$j]} expected=${expected[$j]}" >&2
    status=1
  else
    echo "OK $name commit=${expected[$j]}"
  fi
done
exit "$status"
