#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Print each file path followed by its hash. The argument list is optional, so
# a sorted source list and a fixed set of module files share the same loop.
hash_files() {
  local file
  for file in "$@"; do
    printf '%s\n' "$file"
    sha256sum "$file"
  done
}

# Hash the real build inputs: Go sources under cmd, internal, pkg, harnesses and
# ui/tui, plus the module files. HEAD is deliberately excluded so a commit that
# touches none of these cannot make the binary look stale.
compute_build_hash() {
  {
    local dirs=()
    for dir in cmd internal pkg harnesses ui/tui; do
      if [ -d "$dir" ]; then dirs+=("$dir"); fi
    done
    local sources=()
    local file
    if [ ${#dirs[@]} -gt 0 ]; then
      while IFS= read -r file; do
        sources+=("$file")
      done < <(find "${dirs[@]}" -type f -name '*.go' -print | LC_ALL=C sort)
    fi
    hash_files "${sources[@]}"

    local modules=()
    for file in go.mod go.sum go.work; do
      if [ -f "$file" ]; then modules+=("$file"); fi
    done
    hash_files "${modules[@]}"
  } | sha256sum | cut -d' ' -f1
}

if [ "${1:-}" = "--hash" ]; then
  compute_build_hash
  exit 0
fi

mkdir -p .opencode/bin
go build -o .opencode/bin/beacon ./cmd/beacon
compute_build_hash > .opencode/bin/beacon.build-sha
