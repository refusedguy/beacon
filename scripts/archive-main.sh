#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ref="${1:-origin/main}"
sha="$(git -C "$root" rev-parse --short "$ref")"
out="${2:-/tmp/opencode/beacon-main-${sha}.tar.gz}"

mkdir -p "$(dirname "$out")"

# git archive emits only files committed to the chosen ref, with no .git,
# runtime state, binaries, caches or other ignored files.
git -C "$root" archive --format=tar.gz --prefix="beacon-${sha}/" "$ref" -o "$out"

printf '%s\n' "$out"
