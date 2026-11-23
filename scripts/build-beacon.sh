#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Hash the real build inputs: Go sources under cmd, internal and pkg, plus the
# module files. HEAD is deliberately excluded so a commit that touches none of
# these cannot make the binary look stale.
compute_build_hash() {
  {
    local dirs=()
    for dir in cmd internal pkg; do
      if [ -d "$dir" ]; then dirs+=("$dir"); fi
    done
    if [ ${#dirs[@]} -gt 0 ]; then
      find "${dirs[@]}" -type f -name '*.go' -print | LC_ALL=C sort |
        while IFS= read -r file; do
          printf '%s\n' "$file"
          sha256sum "$file"
        done
    fi
    for file in go.mod go.sum go.work; do
      if [ -f "$file" ]; then
        printf '%s\n' "$file"
        sha256sum "$file"
      fi
    done
  } | sha256sum | cut -d' ' -f1
}

if [ "${1:-}" = "--hash" ]; then
  compute_build_hash
  exit 0
fi

mkdir -p .opencode/bin
go build -o .opencode/bin/beacon ./cmd/beacon
compute_build_hash > .opencode/bin/beacon.build-sha
